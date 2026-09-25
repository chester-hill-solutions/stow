package s3api

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (s *Server) handleListBuckets(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	buckets, err := s.store.ListBuckets(ctx)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"))
		return
	}
	items := make([]bucketEntry, 0, len(buckets))
	for _, b := range buckets {
		items = append(items, bucketEntry{Name: b.Name, CreationDate: formatTime(b.CreationDate)})
	}
	writeXML(w, r, http.StatusOK, newListBucketsResult(items))
}

func (s *Server) handleCreateBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	if !validBucketName(bucket) {
		writeError(w, r, s3Error{Code: "InvalidBucketName", Message: "Invalid bucket name", Resource: "/" + bucket, StatusCode: http.StatusBadRequest})
		return
	}
	err := s.store.CreateBucket(ctx, bucket)
	if err != nil && err != storage.ErrBucketExists {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHeadBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.store.HeadBucket(ctx, bucket)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	err := s.store.DeleteBucket(ctx, bucket)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListObjectsV2(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	continuation := q.Get("continuation-token")
	startAfter := q.Get("start-after")
	encodingType := q.Get("encoding-type")
	maxKeys := 1000
	if mk := q.Get("max-keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n > 0 {
			maxKeys = n
		}
	}

	result, err := s.store.ListObjectsV2(ctx, bucket, storage.ListOptions{
		Prefix:            prefix,
		Delimiter:         delimiter,
		ContinuationToken: continuation,
		MaxKeys:           maxKeys,
		StartAfter:        startAfter,
	})
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}

	encodeURL := encodingType == "url"
	respPrefix := prefix
	respDelimiter := delimiter
	respContinuation := result.ContinuationToken
	respNextContinuation := result.NextContinuationToken
	if encodeURL {
		respPrefix = urlEncodeKey(respPrefix)
		respDelimiter = urlEncodeKey(respDelimiter)
		respContinuation = urlEncodeKey(respContinuation)
		respNextContinuation = urlEncodeKey(respNextContinuation)
	}
	resp := listBucketResult{
		Xmlns:                 xmlNS,
		Name:                  bucket,
		Prefix:                respPrefix,
		KeyCount:              result.KeyCount,
		MaxKeys:               maxKeys,
		IsTruncated:           result.IsTruncated,
		ContinuationToken:     respContinuation,
		NextContinuationToken: respNextContinuation,
		Delimiter:             respDelimiter,
	}
	if encodeURL {
		resp.EncodingType = "url"
	}
	for _, o := range result.Objects {
		resp.Contents = append(resp.Contents, objectToEntry(objectMeta{
			Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.LastModified,
		}, encodeURL))
	}
	for _, cp := range result.CommonPrefixes {
		p := cp
		if encodeURL {
			p = urlEncodeKey(cp)
		}
		resp.CommonPrefixes = append(resp.CommonPrefixes, commonPrefix{Prefix: p})
	}
	writeXML(w, r, http.StatusOK, resp)
}

func (s *Server) handlePutObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Read the body once and share it with every check below. Each of these used
	// to read the whole body itself, which meant a separate full-size copy per
	// check for a single request.
	body, bodyErr := requestBody(r)
	if bodyErr != nil {
		writeError(w, r, bodyReadError(bodyErr, resourcePath(bucket, key), s.config.MaxRequestBytes))
		return
	}
	if err := enforceContentLength(r, body); err != nil {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	if err := verifyContentMD5(r, body); err != nil {
		code := "InvalidArgument"
		if errors.Is(err, storage.ErrMD5Mismatch) {
			code = "BadDigest"
		}
		writeError(w, r, s3Error{Code: code, Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	checksumAlgorithm, checksumValue, err := checksumFromRequest(r, body)
	if err != nil {
		code := "InvalidArgument"
		if errors.Is(err, storage.ErrChecksumMismatch) {
			code = "BadDigest"
		}
		writeError(w, r, s3Error{Code: code, Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	cl := r.ContentLength
	if cl < 0 {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Content-Length required", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	metadata := extractMetadata(r.Header)
	if len(metadata) > 0 {
		total := metadataSize(metadata)
		if total > 2048 {
			writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Metadata too large", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
			return
		}
	}

	meta, err := s.store.PutObject(ctx, bucket, key, r.Body, storage.PutOptions{
		ContentType:       contentType,
		Metadata:          metadata,
		ChecksumAlgorithm: checksumAlgorithm,
		ChecksumValue:     checksumValue,
		IfMatch:           r.Header.Get("If-Match"),
		IfNoneMatch:       r.Header.Get("If-None-Match"),
	})
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("ETag", meta.ETag)
	setChecksumHeader(w, meta)
	writeXML(w, r, http.StatusOK, putObjectResult{ETag: meta.ETag})
}

func (s *Server) handleGetObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	rc, meta, err := s.store.GetObject(ctx, bucket, key)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	defer rc.Close()
	if err := checkReadPreconditions(r.Header, meta); err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}

	rangeHdr := r.Header.Get("Range")
	if rangeHdr != "" {
		s.serveRange(w, r, rc, meta, rangeHdr, bucket, key)
		return
	}

	setObjectHeaders(w, meta)
	setCORS(w, r)
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleHeadObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, err := s.store.HeadObject(ctx, bucket, key)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	if err := checkReadPreconditions(r.Header, meta); err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	setObjectHeaders(w, meta)
	setCORS(w, r)
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	err := s.store.DeleteObject(ctx, bucket, key)
	if err != nil && err != storage.ErrObjectNotFound {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteObjects(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	// DeleteObjects decodes XML straight from the body, so it needs the same
	// single read every other handler does.
	body, bodyErr := requestBody(r)
	if bodyErr != nil {
		writeError(w, r, bodyReadError(bodyErr, "/"+bucket, s.config.MaxRequestBytes))
		return
	}
	if err := enforceContentLength(r, body); err != nil {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: err.Error(), Resource: "/" + bucket, StatusCode: http.StatusBadRequest})
		return
	}
	var req deleteObjectsRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, s3Error{Code: "MalformedXML", Message: "Malformed XML", Resource: "/" + bucket, StatusCode: http.StatusBadRequest})
		return
	}
	keys := make([]string, 0, len(req.Objects))
	for _, o := range req.Objects {
		keys = append(keys, o.Key)
	}
	deleted, err := s.store.DeleteObjects(ctx, bucket, keys)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	resp := deleteResult{}
	if !req.Quiet {
		for _, k := range deleted {
			resp.Deleted = append(resp.Deleted, deletedEntry{Key: k})
		}
	}
	writeXML(w, r, http.StatusOK, resp)
}

func (s *Server) handleCopyObject(ctx context.Context, w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
	srcBucket, srcKey, err := parseCopySource(copySource)
	if err != nil {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: err.Error(), Resource: resourcePath(dstBucket, dstKey), StatusCode: http.StatusBadRequest})
		return
	}

	srcMeta, err := s.store.HeadObject(ctx, srcBucket, srcKey)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}

	if err := checkCopyPreconditions(r.Header, srcMeta); err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}

	var meta *storage.ObjectMeta
	directive := strings.ToUpper(strings.TrimSpace(r.Header.Get("x-amz-metadata-directive")))
	switch directive {
	case "", "COPY":
		meta, err = s.store.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	case "REPLACE":
		rc, _, openErr := s.store.GetObject(ctx, srcBucket, srcKey)
		if openErr != nil {
			writeError(w, r, mapStorageError(openErr, resourcePath(dstBucket, dstKey)))
			return
		}
		defer rc.Close()
		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		meta, err = s.store.PutObject(ctx, dstBucket, dstKey, rc, storage.PutOptions{
			ContentType: contentType,
			Metadata:    extractMetadata(r.Header),
		})
	default:
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Invalid metadata directive", Resource: resourcePath(dstBucket, dstKey), StatusCode: http.StatusBadRequest})
		return
	}
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}
	writeXML(w, r, http.StatusOK, copyObjectResult{
		LastModified: formatTime(meta.LastModified),
		ETag:         meta.ETag,
	})
}

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
	w.Header().Set("ETag", meta.ETag)
	setChecksumHeader(w, meta)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.Metadata {
		w.Header().Set(k, v)
	}
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

func validBucketName(name string) bool {
	return storage.ValidBucketName(name)
}

func parseCopySource(src string) (bucket, key string, err error) {
	src = strings.TrimPrefix(src, "/")
	parts := strings.SplitN(src, "/", 2)
	if len(parts) != 2 {
		return "", "", errInvalidCopySource
	}
	return parts[0], parts[1], nil
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

func checkReadPreconditions(h http.Header, meta *storage.ObjectMeta) error {
	if match := h.Get("If-Match"); match != "" && !etagHeaderMatchesStrong(match, meta.ETag) {
		return storage.ErrPreconditionFailed
	}
	if noneMatch := h.Get("If-None-Match"); noneMatch != "" && etagHeaderMatchesWeak(noneMatch, meta.ETag) {
		return storage.ErrPreconditionFailed
	}
	if raw := h.Get("If-Modified-Since"); raw != "" {
		when, err := time.Parse(http.TimeFormat, raw)
		if err != nil {
			return storage.ErrPreconditionFailed
		}
		if !meta.LastModified.After(when) {
			return storage.ErrPreconditionFailed
		}
	}
	if raw := h.Get("If-Unmodified-Since"); raw != "" {
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

func checkCopyPreconditions(h http.Header, meta *storage.ObjectMeta) error {
	if match := h.Get("x-amz-copy-source-if-match"); match != "" && !etagHeaderMatchesStrong(match, meta.ETag) {
		return storage.ErrPreconditionFailed
	}
	if noneMatch := h.Get("x-amz-copy-source-if-none-match"); noneMatch != "" && etagHeaderMatchesWeak(noneMatch, meta.ETag) {
		return storage.ErrPreconditionFailed
	}
	if raw := h.Get("x-amz-copy-source-if-modified-since"); raw != "" {
		when, err := time.Parse(http.TimeFormat, raw)
		if err != nil {
			return storage.ErrPreconditionFailed
		}
		if !meta.LastModified.After(when) {
			return storage.ErrPreconditionFailed
		}
	}
	if raw := h.Get("x-amz-copy-source-if-unmodified-since"); raw != "" {
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
