package storage

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *FilesystemStore) ListObjectsV2(_ context.Context, bucket string, opts ListOptions) (*ListResult, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}

	root := s.objectsDir(bucket)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return PaginateObjects(nil, opts), nil
		}
		return nil, err
	}
	items := make([]ObjectMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), legacyMetaSuffix) {
			continue
		}
		key, ok := objectKeyFromFilename(entry.Name())
		if !ok {
			continue
		}
		if opts.Prefix != "" && !strings.HasPrefix(key, opts.Prefix) {
			continue
		}
		record, err := readObjectRecord(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		items = append(items, record.meta(bucket, key))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return PaginateObjects(items, opts), nil
}
