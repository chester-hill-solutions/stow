package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (s *Store) bucketDir(bucket string) string {
	if bucket == s.manifest.Bucket {
		return s.root
	}
	return InternalPath(s.root, "buckets", bucket)
}

// bucketExists reports whether a bucket has a directory. The workspace bucket
// always exists, because the root does.
func (s *Store) bucketExists(bucket string) bool {
	if bucket == s.manifest.Bucket {
		return true
	}
	info, err := os.Stat(s.bucketDir(bucket))
	return err == nil && info.IsDir()
}

// foldKey qualifies a workspace-relative path with its bucket so one index
// serves every bucket.
func foldKey(bucket, relative string) string {
	return bucket + "\x00" + FoldPath(relative)
}

// index builds the case-folded path index by walking every bucket's tree. It
// runs once at open, and it walks the filesystem rather than the manifest
// because an adopted file with no manifest entry still occupies a path that a
// later key must not collide with.
func (s *Store) index() error {
	s.folded = map[string]string{}
	buckets, err := s.discoverBuckets()
	if err != nil {
		return err
	}
	for _, bucket := range buckets {
		root := s.bucketDir(bucket)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				// A file that vanished under us is not an error worth refusing
				// an open for; it is simply not in the index.
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			if IsInternal(relative) {
				return nil
			}
			s.folded[foldKey(bucket, relative)] = relative
			return nil
		})
		if err != nil {
			return fmt.Errorf("workspace store: index %s: %w", bucket, err)
		}
	}
	return nil
}

// discoverBuckets lists the workspace bucket plus every bucket directory found
// under the internal one.
func (s *Store) discoverBuckets() ([]string, error) {
	seen := map[string]bool{}
	if s.manifest.Bucket != "" {
		seen[s.manifest.Bucket] = true
	}
	entries, err := os.ReadDir(InternalPath(s.root, "buckets"))
	if errors.Is(err, os.ErrNotExist) {
		return keysOf(seen), nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace store: list buckets: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && storage.ValidBucketName(entry.Name()) {
			seen[entry.Name()] = true
		}
	}
	return keysOf(seen), nil
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// pathFor decides where a key is written. A key is stored at its own path when
// the layout allows it and no case-insensitively equal path is already taken;
// otherwise it is escaped to a digest under the internal directory.
func (s *Store) pathFor(bucket, key string) (string, Form) {
	if s.layout.IsNatural(key) {
		owner, taken := s.folded[foldKey(bucket, key)]
		if !taken || owner == key {
			return NaturalPath(s.bucketDir(bucket), key), FormNatural
		}
	}
	return EscapedPath(s.root, key), FormEscaped
}

// CreateBucket makes a bucket's directory. The workspace bucket needs none: the
// root is already there.

func (s *Store) CreateBucket(_ context.Context, name string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := storage.ValidateBucketName(name); err != nil {
		return err
	}
	if name == s.manifest.Bucket {
		return nil
	}
	if err := os.MkdirAll(s.bucketDir(name), 0o755); err != nil {
		return fmt.Errorf("workspace store: create bucket: %w", err)
	}
	return nil
}

// DeleteBucket removes an empty bucket. The workspace bucket cannot be removed:
// it is the directory the caller is working in.
func (s *Store) DeleteBucket(_ context.Context, name string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if name == s.manifest.Bucket {
		return storage.ErrBucketNotFound
	}
	dir := s.bucketDir(name)
	if !s.bucketExists(name) {
		return storage.ErrBucketNotFound
	}
	if err := s.requireEmptyBucket(name); err != nil {
		return err
	}
	return os.Remove(dir)
}

// requireEmptyBucket refuses to remove a bucket that still holds an object or
// an upload in progress. An upload counts: a bucket is not empty while a caller
// is midway through writing into it, and removing the directory under them
// would strand the upload with nowhere to complete.
func (s *Store) requireEmptyBucket(bucket string) error {
	objects, err := s.listAll(bucket)
	if err != nil {
		return err
	}
	if len(objects) > 0 {
		return storage.ErrBucketNotEmpty
	}
	active, err := s.activeUploads(bucket)
	if err != nil {
		return err
	}
	if active {
		return storage.ErrBucketNotEmpty
	}
	return nil
}

// HeadBucket returns a bucket's creation date, which for this backend is the
// modification time of its directory.
func (s *Store) HeadBucket(_ context.Context, name string) (*storage.BucketInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(name) {
		return nil, storage.ErrBucketNotFound
	}
	info, err := os.Stat(s.bucketDir(name))
	if err != nil {
		return nil, storage.ErrBucketNotFound
	}
	return &storage.BucketInfo{Name: name, CreationDate: info.ModTime().UTC()}, nil
}

// ListBuckets returns the workspace bucket and every other bucket found.
func (s *Store) ListBuckets(_ context.Context) ([]storage.BucketInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	names, err := s.discoverBuckets()
	if err != nil {
		return nil, err
	}
	out := make([]storage.BucketInfo, 0, len(names))
	for _, name := range names {
		if info, err := s.HeadBucket(context.Background(), name); err == nil {
			out = append(out, *info)
		}
	}
	return out, nil
}

// PutObject writes an object as a real file, atomically, and records what stow
// knows about it in the manifest.
