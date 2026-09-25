package fs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	storage "github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (s *FilesystemStore) ListObjectsV2(_ context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	if err := storage.ValidateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, storage.ErrBucketNotFound
	}

	root := s.objectsDir(bucket)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return storage.PaginateObjects(nil, opts), nil
		}
		return nil, err
	}
	items := make([]storage.ObjectMeta, 0, len(entries))
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
	return storage.PaginateObjects(items, opts), nil
}
