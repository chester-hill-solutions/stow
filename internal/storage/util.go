package storage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"path"
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

// objectRelPath returns the relative path under a bucket's objects directory.
func objectRelPath(key string) string {
	return path.Clean("/" + key)[1:]
}
