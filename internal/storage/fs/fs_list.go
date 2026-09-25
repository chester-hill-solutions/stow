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
	items := make([]storage.ObjectMeta, 0)
	// Keys longer than 127 bytes are sharded across nested directories, so the
	// walk descends. A directory is a shard and contributes the leading part of
	// the encoded key; the record file is the leaf and contributes the rest.
	// A flat key is the degenerate case of exactly one segment, so both layouts
	// are handled by the same pass and a file is never mistaken for a shard.
	var walk func(dir string, segments []string) error
	walk = func(dir string, segments []string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			entryPath := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				// Only descend into a shard this package wrote. A directory that
				// is not shard-named holds no object, and treating it as one
				// would decode the names beneath it into a key that never
				// existed.
				if !isShardName(entry.Name()) {
					continue
				}
				// Copied rather than appended in place: sibling directories
				// share this slice's backing array, and appending to it would
				// let one sibling overwrite the path another is about to read.
				child := append(append(make([]string, 0, len(segments)+1), segments...), entry.Name())
				if err := walk(entryPath, child); err != nil {
					return err
				}
				continue
			}
			if strings.HasSuffix(entry.Name(), legacyMetaSuffix) {
				continue
			}
			key, ok := objectKeyFromSegments(append(segments, entry.Name()))
			if !ok {
				continue
			}
			if opts.Prefix != "" && !strings.HasPrefix(key, opts.Prefix) {
				continue
			}
			record, err := readObjectRecord(entryPath)
			if err != nil {
				return err
			}
			items = append(items, record.meta(bucket, key))
		}
		return nil
	}
	if err := walk(root, nil); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return storage.PaginateObjects(items, opts), nil
}
