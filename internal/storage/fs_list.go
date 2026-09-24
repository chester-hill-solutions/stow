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
		if entry.IsDir() || strings.HasSuffix(entry.Name(), metaSuffix) {
			continue
		}
		key, ok := objectKeyFromFilename(entry.Name())
		if !ok {
			continue
		}
		if opts.Prefix != "" && !strings.HasPrefix(key, opts.Prefix) {
			continue
		}
		p := filepath.Join(root, entry.Name())
		st, err := entry.Info()
		if err != nil {
			return nil, err
		}
		sidecar, _ := readObjectSidecar(p + metaSuffix)
		items = append(items, ObjectMeta{
			Bucket:       bucket,
			Key:          key,
			Size:         st.Size(),
			LastModified: st.ModTime().UTC(),
			ContentType:  sidecar.ContentType,
			Metadata:     cloneMetadata(sidecar.Metadata),
			ETag:         sidecar.ETag,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return PaginateObjects(items, opts), nil
}
