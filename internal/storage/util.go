package storage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

func validateBucketName(name string) error {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return ErrBucketNotFound
	}
	return nil
}

func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return ErrInvalidKey
	}
	return nil
}

func etagForBytes(data []byte) string {
	sum := md5.Sum(data)
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(sum[:]))
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

// PaginateObjects applies ListObjectsV2 continuation/delimiter/max-keys semantics
// to a sorted slice of ObjectMeta. Callers must pre-filter by prefix and sort by key.
func PaginateObjects(items []ObjectMeta, opts ListOptions) *ListResult {
	startAfter := listStartAfter(opts)
	maxKeys := maxKeysOrDefault(opts.MaxKeys)
	result := &ListResult{}
	prefixSet := map[string]struct{}{}

	for _, meta := range items {
		key := meta.Key
		if startAfter != "" && key <= startAfter {
			continue
		}
		if cp := commonPrefixFor(key, opts.Prefix, opts.Delimiter); cp != "" {
			if _, ok := prefixSet[cp]; !ok {
				prefixSet[cp] = struct{}{}
				result.CommonPrefixes = append(result.CommonPrefixes, cp)
			}
			continue
		}
		if len(result.Objects) >= maxKeys {
			result.IsTruncated = true
			result.NextContinuationToken = key
			break
		}
		result.Objects = append(result.Objects, meta)
	}
	sort.Strings(result.CommonPrefixes)
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	if opts.ContinuationToken != "" {
		result.ContinuationToken = opts.ContinuationToken
	}
	return result
}

// objectRelPath returns the relative path under a bucket's objects directory.
func objectRelPath(key string) string {
	return path.Clean("/" + key)[1:]
}
