package storage

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

func validateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 || strings.Contains(name, "..") {
		return ErrInvalidBucketName
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return ErrInvalidBucketName
	}
	return nil
}

func ValidBucketName(name string) bool {
	return validateBucketName(name) == nil
}

func validateKey(key string) error {
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || strings.IndexByte(key, 0) >= 0 {
		return ErrInvalidKey
	}
	return nil
}

func newRecordVersion() (string, error) {
	var version [16]byte
	if _, err := rand.Read(version[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(version[:]), nil
}

func etagForBytes(data []byte) string {
	sum := md5.Sum(data)
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(sum[:]))
}

func etagEqual(left, right string) bool {
	return strings.Trim(left, "\"") == strings.Trim(right, "\"")
}

func compositeETag(partETags []string) string {
	h := md5.New()
	for _, etag := range partETags {
		raw, err := hex.DecodeString(strings.Trim(etag, "\""))
		if err == nil {
			_, _ = h.Write(raw)
		}
	}
	return fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(h.Sum(nil)), len(partETags))
}

func validateMultipartPartNumbers(parts []PartInfo) error {
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		if part.PartNumber < 1 || part.PartNumber > 10000 {
			return ErrInvalidPart
		}
		if _, ok := seen[part.PartNumber]; ok {
			return ErrInvalidPart
		}
		seen[part.PartNumber] = struct{}{}
	}
	return nil
}

func etagForReader(r io.Reader) (string, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", nil, err
	}
	return etagForBytes(data), data, nil
}

func cloneMetadata(m map[string]string) map[string]string {
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
			result.NextKeyMarker = upload.Key
			result.NextUploadIDMarker = upload.UploadID
			break
		}
		result.Uploads = append(result.Uploads, upload)
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
	for _, entry := range entries {
		if startAfter != "" && entry.value <= startAfter {
			continue
		}
		if len(result.Objects)+len(result.CommonPrefixes) >= maxKeys {
			result.IsTruncated = true
			result.NextContinuationToken = entry.value
			break
		}
		if entry.prefix {
			result.CommonPrefixes = append(result.CommonPrefixes, entry.value)
		} else {
			result.Objects = append(result.Objects, *entry.object)
		}
	}
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	return result
}

// objectRelPath returns a reversible, flat filesystem name for an object key.
// Encoding the complete key avoids path traversal and preserves empty, repeated,
// and dot path segments.
func objectRelPath(key string) string {
	return hex.EncodeToString([]byte(key))
}

func objectKeyFromFilename(name string) (string, bool) {
	decoded, err := hex.DecodeString(name)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
