package s3api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// Validators, preconditions, and response headers.
//
// These are the parts of the wire surface that decide whether a request is a
// cache hit, a refusal, or a write — kept apart from the handlers that call
// them, because the conditional-read rules changed underneath the handlers and
// the file they were buried in had grown past the size limit as a result.

func setChecksumHeader(w http.ResponseWriter, meta *storage.ObjectMeta) {
	if meta.ChecksumAlgorithm == "" || meta.ChecksumValue == "" {
		return
	}
	w.Header().Set("x-amz-checksum-"+strings.ToLower(meta.ChecksumAlgorithm), meta.ChecksumValue)
}

func setObjectHeaders(w http.ResponseWriter, meta *storage.ObjectMeta) {
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	setValidators(w, meta)
	setChecksumHeader(w, meta)
	for k, v := range meta.Metadata {
		w.Header().Set(k, v)
	}
}

// setValidators emits the caching validators and nothing else.
//
// It is separate from setObjectHeaders because a 304 must not carry the
// representation metadata. RFC 9110 15.4.5: a 304 repeats the fields that
// describe the *selected representation* — ETag, Last-Modified, Cache-Control,
// Vary — and no body. Emitting x-amz-checksum-* on a 304 is actively harmful
// rather than merely redundant: the SDK has no body to check the checksum
// against, hashes the empty one, and reports a mismatch on a response that is
// telling the truth. Content-Length has the same problem, claiming a length
// for bytes that are not being sent.
func setValidators(w http.ResponseWriter, meta *storage.ObjectMeta) {
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
}

func extractMetadata(h http.Header) map[string]string {
	out := make(map[string]string)
	for k, vals := range h {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-meta-") {
			out[k] = vals[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func metadataSize(m map[string]string) int {
	n := 0
	for k, v := range m {
		n += len(k) + len(v)
	}
	return n
}

// parseCopySource splits an x-amz-copy-source header into its bucket and key.
//
// The key is percent-encoded, so the split happens on the raw header and each
// half is decoded afterwards, never the other way round: decoding first would
// turn an encoded %2F inside a key into a separator and land the split inside the
// key, making a key with a real slash work and a key with an encoded one fail.
//
// It is PathUnescape and not QueryUnescape because in a path segment a plus is a
// literal plus rather than an encoded space, and the query form would rewrite a
// key spelled "a+b" into "a b" and copy the wrong object without complaining.
func parseCopySource(src string) (bucket, key string, err error) {
	src = strings.TrimPrefix(src, "/")
	parts := strings.SplitN(src, "/", 2)
	if len(parts) != 2 {
		return "", "", errInvalidCopySource
	}
	bucket, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", errInvalidCopySource
	}
	key, err = url.PathUnescape(parts[1])
	if err != nil {
		return "", "", errInvalidCopySource
	}
	return bucket, key, nil
}

var errInvalidCopySource = &copySourceError{"Invalid copy source"}

type copySourceError struct{ msg string }

func (e *copySourceError) Error() string { return e.msg }

func etagHeaderMatchesStrong(header, actual string) bool {
	return etagHeaderMatchesMode(header, actual, false)
}

func etagHeaderMatchesWeak(header, actual string) bool {
	return etagHeaderMatchesMode(header, actual, true)
}

func etagHeaderMatchesMode(header, actual string, weak bool) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidateValue := candidate
		if strings.HasPrefix(strings.ToLower(candidateValue), "w/") {
			if !weak {
				continue
			}
			candidateValue = candidateValue[2:]
		}
		if strings.EqualFold(strings.Trim(candidateValue, "\""), strings.Trim(actual, "\"")) {
			return true
		}
	}
	return false
}

// errNotModified is a matching If-None-Match on a read: a successful answer
// carrying no body, which is 304 and not an error. It is deliberately distinct
// from storage.ErrPreconditionFailed so the handler can render the two
// differently instead of reporting "not modified" as a failed request.
var errNotModified = errors.New("not modified")

// checkPreconditions applies the RFC 9110 conditional headers.
//
// A read and a copy-source ask the same four questions of the same
// representation under different header names, so they are one function: the
// names are the only thing that differs, and a second copy of these conditions
// is a second place for the rule to drift.
//
// notModifiedIsSuccess is the one real difference. On GET and HEAD a matching
// If-None-Match is 304 Not Modified, a successful answer with no body; the
// copy-source headers have no 304 equivalent, so there a match is 412. Folding
// them together without this would put issue #11's distinction back.
func checkPreconditions(h http.Header, meta *storage.ObjectMeta, prefix string, notModifiedIsSuccess bool) error {
	noneMatchError := error(storage.ErrPreconditionFailed)
	if notModifiedIsSuccess {
		noneMatchError = errNotModified
	}

	if match := h.Get(prefix + "If-Match"); match != "" && !etagHeaderMatchesStrong(match, meta.ETag) {
		return storage.ErrPreconditionFailed
	}
	if noneMatch := h.Get(prefix + "If-None-Match"); noneMatch != "" && etagHeaderMatchesWeak(noneMatch, meta.ETag) {
		return noneMatchError
	}
	if raw := h.Get(prefix + "If-Modified-Since"); raw != "" {
		when, err := time.Parse(http.TimeFormat, raw)
		if err != nil {
			return storage.ErrPreconditionFailed
		}
		if !meta.LastModified.After(when) {
			return storage.ErrPreconditionFailed
		}
	}
	if raw := h.Get(prefix + "If-Unmodified-Since"); raw != "" {
		when, err := time.Parse(http.TimeFormat, raw)
		if err != nil {
			return storage.ErrPreconditionFailed
		}
		if meta.LastModified.After(when) {
			return storage.ErrPreconditionFailed
		}
	}
	return nil
}

func checkReadPreconditions(h http.Header, meta *storage.ObjectMeta) error {
	return checkPreconditions(h, meta, "", true)
}

func checkCopyPreconditions(h http.Header, meta *storage.ObjectMeta) error {
	return checkPreconditions(h, meta, "x-amz-copy-source-", false)
}
