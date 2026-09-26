package storage

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"unicode/utf8"
)

// Reserved service prefixes must not be used for caller-created buckets.
var reservedBucketPrefixes = [...]string{
	"xn--",
	"sthree-",
	"amzn-s3-demo-",
	"amzn_s3_demo_",
}

// ValidateBucketName validates a bucket name for all storage backends.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 || strings.Contains(name, "..") {
		return ErrInvalidBucketName
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return ErrInvalidBucketName
	}
	if net.ParseIP(name) != nil {
		return ErrInvalidBucketName
	}
	for _, prefix := range reservedBucketPrefixes {
		if strings.HasPrefix(name, prefix) {
			return ErrInvalidBucketName
		}
	}
	return nil
}

func ValidBucketName(name string) bool {
	return ValidateBucketName(name) == nil
}

// ValidateKey validates an object key for all storage backends.
func ValidateKey(key string) error {
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || strings.IndexByte(key, 0) >= 0 {
		return ErrInvalidKey
	}
	return nil
}

// NewRecordVersion returns an opaque immutable object version identifier.
func NewRecordVersion() (string, error) {
	var version [16]byte
	if _, err := rand.Read(version[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(version[:]), nil
}

// NewUploadID returns an opaque multipart upload identifier.
func NewUploadID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// ETagForBytes returns the storage ETag for a byte slice.
func ETagForBytes(data []byte) string {
	sum := md5.Sum(data)
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(sum[:]))
}

// ETagEqual compares two storage ETags.
func ETagEqual(left, right string) bool {
	return strings.Trim(left, "\"") == strings.Trim(right, "\"")
}

// CompositeETag returns the ETag for a completed multipart upload.
func CompositeETag(partETags []string) string {
	h := md5.New()
	for _, etag := range partETags {
		raw, err := hex.DecodeString(strings.Trim(etag, "\""))
		if err == nil {
			_, _ = h.Write(raw)
		}
	}
	return fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(h.Sum(nil)), len(partETags))
}

// ValidateMultipartPartNumbers validates that completion parts are in strictly
// ascending order, with no duplicate part numbers, and that there is at least
// one of them.
//
// The empty case is here rather than in each backend because it was written out
// in two of the three and missing from the third, so the same request against
// two stores produced a 500 on one and a zero-byte object on the other. It
// returns ErrInvalidPart because that is the sentinel the S3 surface maps; the
// unmapped ErrInvalidUpload fell through to a 500, which misreports a malformed
// request as a server fault and invites a retry that can never succeed.
func ValidateMultipartPartNumbers(parts []PartInfo) error {
	if len(parts) == 0 {
		return ErrInvalidPart
	}
	previous := 0
	for _, part := range parts {
		if part.PartNumber < 1 || part.PartNumber > 10000 {
			return ErrInvalidPart
		}
		if part.PartNumber <= previous {
			return ErrInvalidPart
		}
		previous = part.PartNumber
	}
	return nil
}

// CompletionETag is the ETag of a completed multipart object.
//
// A completion with exactly one part produced a single-part object, and S3 gives
// it that part's own ETag verbatim. The `-{partCount}` suffix belongs to
// genuinely multipart objects, where the digest covers the concatenated part
// digests. So one part is not "a multipart object with a count of one": it is
// the part.
//
// This rule lived in three places and was correct in one of them, with two
// backends returning md5(md5(body))-1 where S3 returns md5(body). A client
// hands that value back on its next conditional write, so the error is not
// cosmetic — it is a self-inflicted 412 on every subsequent request.
func CompletionETag(partETags []string) string {
	if len(partETags) == 1 {
		return partETags[0]
	}
	return CompositeETag(partETags)
}

// ETagForReader reads an object and returns its ETag and bytes.
func ETagForReader(r io.Reader) (string, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", nil, err
	}
	return ETagForBytes(data), data, nil
}

// CloneMetadata returns an independent copy of object metadata.
func CloneMetadata(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func commonPrefixFor(key, prefix, delimiter string) string {
	if delimiter == "" {
		return ""
	}
	rest := strings.TrimPrefix(key, prefix)
	idx := strings.Index(rest, delimiter)
	if idx < 0 {
		return ""
	}
	return prefix + rest[:idx+len(delimiter)]
}

func listStartAfter(opts ListOptions) string {
	if opts.StartAfter != "" {
		return opts.StartAfter
	}
	return opts.ContinuationToken
}

func maxKeysOrDefault(max int) int {
	if max <= 0 {
		return 1000
	}
	return max
}

func maxUploadsOrDefault(max int) int {
	if max <= 0 {
		return 1000
	}
	return max
}

func maxPartsOrDefault(max int) int {
	if max <= 0 {
		return 1000
	}
	return max
}

// PaginateParts applies ListParts marker and max-parts semantics to parts.
// The input is sorted on a copy so callers retain their original ordering.
func PaginateParts(items []PartInfo, opts ListPartsOptions) *ListPartsResult {
	marker := opts.PartNumberMarker
	if marker < 0 {
		marker = 0
	}
	result := &ListPartsResult{
		Parts:            make([]PartInfo, 0),
		PartNumberMarker: marker,
		MaxParts:         maxPartsOrDefault(opts.MaxParts),
	}
	ordered := append([]PartInfo(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].PartNumber < ordered[j].PartNumber
	})
	for _, part := range ordered {
		if part.PartNumber <= marker {
			continue
		}
		if len(result.Parts) >= result.MaxParts {
			result.IsTruncated = true
			result.NextPartNumberMarker = result.Parts[len(result.Parts)-1].PartNumber
			break
		}
		result.Parts = append(result.Parts, part)
	}
	return result
}

func PaginateMultipartUploads(items []MultipartUpload, opts MultipartListOptions) *MultipartListResult {
	maxUploads := maxUploadsOrDefault(opts.MaxUploads)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Key != items[j].Key {
			return items[i].Key < items[j].Key
		}
		if !items[i].Initiated.Equal(items[j].Initiated) {
			return items[i].Initiated.Before(items[j].Initiated)
		}
		return items[i].UploadID < items[j].UploadID
	})

	result := &MultipartListResult{
		Prefix:         opts.Prefix,
		Delimiter:      opts.Delimiter,
		KeyMarker:      opts.KeyMarker,
		UploadIDMarker: opts.UploadIDMarker,
		MaxUploads:     maxUploads,
	}
	lastKey := ""
	lastUploadID := ""
	for _, upload := range items {
		if opts.Prefix != "" && !strings.HasPrefix(upload.Key, opts.Prefix) {
			continue
		}
		if opts.KeyMarker != "" {
			if upload.Key < opts.KeyMarker {
				continue
			}
			if upload.Key == opts.KeyMarker && opts.UploadIDMarker != "" && upload.UploadID <= opts.UploadIDMarker {
				continue
			}
		}
		if len(result.Uploads) >= maxUploads {
			result.IsTruncated = true
			if lastKey != "" {
				result.NextKeyMarker = lastKey
				result.NextUploadIDMarker = lastUploadID
			} else {
				result.NextKeyMarker = upload.Key
				result.NextUploadIDMarker = upload.UploadID
			}
			break
		}
		result.Uploads = append(result.Uploads, upload)
		lastKey = upload.Key
		lastUploadID = upload.UploadID
	}
	return result
}

// PaginateObjects applies ListObjectsV2 continuation/delimiter/max-keys semantics
// to a sorted slice of ObjectMeta. Callers must pre-filter by prefix and sort by key.
func PaginateObjects(items []ObjectMeta, opts ListOptions) *ListResult {
	maxKeys := maxKeysOrDefault(opts.MaxKeys)
	startAfter := opts.ContinuationToken
	if startAfter == "" {
		startAfter = opts.StartAfter
	}

	type logicalEntry struct {
		value  string
		object *ObjectMeta
		prefix bool
	}
	entries := make([]logicalEntry, 0, len(items))
	prefixes := make(map[string]struct{})
	for i := range items {
		meta := items[i]
		if cp := commonPrefixFor(meta.Key, opts.Prefix, opts.Delimiter); cp != "" {
			if _, seen := prefixes[cp]; !seen {
				prefixes[cp] = struct{}{}
				entries = append(entries, logicalEntry{value: cp, prefix: true})
			}
			continue
		}
		entries = append(entries, logicalEntry{value: meta.Key, object: &meta})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].value < entries[j].value })

	result := &ListResult{}
	if opts.ContinuationToken != "" {
		result.ContinuationToken = opts.ContinuationToken
	}
	lastValue := ""
	for _, entry := range entries {
		if startAfter != "" && entry.value <= startAfter {
			continue
		}
		if len(result.Objects)+len(result.CommonPrefixes) >= maxKeys {
			result.IsTruncated = true
			if lastValue != "" {
				result.NextContinuationToken = lastValue
			} else {
				result.NextContinuationToken = entry.value
			}
			break
		}
		if entry.prefix {
			result.CommonPrefixes = append(result.CommonPrefixes, entry.value)
		} else {
			result.Objects = append(result.Objects, *entry.object)
		}
		lastValue = entry.value
	}
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	return result
}
