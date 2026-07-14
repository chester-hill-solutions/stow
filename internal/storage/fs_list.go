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

	var items []ObjectMeta
	root := s.objectsDir(bucket)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), metaSuffix) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if opts.Prefix != "" && !strings.HasPrefix(key, opts.Prefix) {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return err
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
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return PaginateObjects(items, opts), nil
}
