package storage

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const metaSuffix = ".stowmeta"

type objectSidecar struct {
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	ETag        string            `json:"etag,omitempty"`
}

// FilesystemStore persists object bytes on disk with atomic writes.
// Object metadata is stored alongside each object as a JSON sidecar (.stowmeta).
type FilesystemStore struct {
	dataDir string
	mu      sync.RWMutex
}

// NewFilesystemStore creates a filesystem-backed store rooted at dataDir.
func NewFilesystemStore(dataDir string) (*FilesystemStore, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "buckets"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, ".multipart"), 0o755); err != nil {
		return nil, err
	}
	return &FilesystemStore{dataDir: dataDir}, nil
}

func (s *FilesystemStore) bucketDir(bucket string) string {
	return filepath.Join(s.dataDir, "buckets", bucket)
}

func (s *FilesystemStore) objectsDir(bucket string) string {
	return filepath.Join(s.bucketDir(bucket), "objects")
}

func (s *FilesystemStore) objectPath(bucket, key string) string {
	return filepath.Join(s.objectsDir(bucket), objectRelPath(key))
}

func (s *FilesystemStore) metaPath(bucket, key string) string {
	return s.objectPath(bucket, key) + metaSuffix
}

func (s *FilesystemStore) multipartDir(uploadID string) string {
	return filepath.Join(s.dataDir, ".multipart", uploadID)
}

func (s *FilesystemStore) CreateBucket(_ context.Context, name string) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.bucketDir(name)
	if _, err := os.Stat(dir); err == nil {
		return ErrBucketExists
	}
	if err := os.MkdirAll(s.objectsDir(name), 0o755); err != nil {
		return err
	}
	created := time.Now().UTC()
	return writeJSONAtomic(filepath.Join(dir, "bucket.json"), map[string]time.Time{"created_at": created})
}

func (s *FilesystemStore) DeleteBucket(_ context.Context, name string) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.bucketDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ErrBucketNotFound
	}
	if hasObjects, err := bucketHasObjects(s.objectsDir(name)); err != nil {
		return err
	} else if hasObjects {
		return ErrBucketNotEmpty
	}
	return os.RemoveAll(dir)
}

func (s *FilesystemStore) HeadBucket(_ context.Context, name string) (*BucketInfo, error) {
	if err := validateBucketName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.bucketDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}
	created, err := readBucketCreated(dir)
	if err != nil {
		return nil, err
	}
	return &BucketInfo{Name: name, CreationDate: created}, nil
}

func (s *FilesystemStore) ListBuckets(_ context.Context) ([]BucketInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	root := filepath.Join(s.dataDir, "buckets")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BucketInfo, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		created, err := readBucketCreated(filepath.Join(root, e.Name()))
		if err != nil {
			created = time.Now().UTC()
		}
		out = append(out, BucketInfo{Name: e.Name(), CreationDate: created})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *FilesystemStore) PutObject(_ context.Context, bucket, key string, body io.Reader, opts PutOptions) (*ObjectMeta, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}

	etag, data, err := etagForReader(body)
	if err != nil {
		return nil, err
	}
	objPath := s.objectPath(bucket, key)
	if err := writeBytesAtomic(objPath, data); err != nil {
		return nil, err
	}
	sidecar := objectSidecar{
		ContentType: opts.ContentType,
		Metadata:    cloneMetadata(opts.Metadata),
		ETag:        etag,
	}
	if err := writeJSONAtomic(s.metaPath(bucket, key), sidecar); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &ObjectMeta{
		Bucket:       bucket,
		Key:          key,
		Size:         int64(len(data)),
		ETag:         etag,
		ContentType:  opts.ContentType,
		LastModified: now,
		Metadata:     cloneMetadata(opts.Metadata),
	}, nil
}

func (s *FilesystemStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error) {
	meta, err := s.HeadObject(ctx, bucket, key)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(s.objectPath(bucket, key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrObjectNotFound
		}
		return nil, nil, err
	}
	return f, meta, nil
}

func (s *FilesystemStore) HeadObject(_ context.Context, bucket, key string) (*ObjectMeta, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	objPath := s.objectPath(bucket, key)
	st, err := os.Stat(objPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}
	sidecar, _ := readObjectSidecar(s.metaPath(bucket, key))
	meta := &ObjectMeta{
		Bucket:       bucket,
		Key:          key,
		Size:         st.Size(),
		LastModified: st.ModTime().UTC(),
		ContentType:  sidecar.ContentType,
		Metadata:     cloneMetadata(sidecar.Metadata),
		ETag:         sidecar.ETag,
	}
	if meta.ETag == "" {
		data, err := os.ReadFile(objPath)
		if err != nil {
			return nil, err
		}
		meta.ETag = etagForBytes(data)
	}
	return meta, nil
}

func (s *FilesystemStore) DeleteObject(_ context.Context, bucket, key string) error {
	if err := validateBucketName(bucket); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	objPath := s.objectPath(bucket, key)
	if _, err := os.Stat(objPath); os.IsNotExist(err) {
		return ErrObjectNotFound
	}
	_ = os.Remove(s.metaPath(bucket, key))
	return os.Remove(objPath)
}

func (s *FilesystemStore) DeleteObjects(_ context.Context, bucket string, keys []string) ([]string, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted []string
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return deleted, err
		}
		objPath := s.objectPath(bucket, key)
		if _, err := os.Stat(objPath); os.IsNotExist(err) {
			continue
		}
		_ = os.Remove(s.metaPath(bucket, key))
		if err := os.Remove(objPath); err != nil {
			return deleted, err
		}
		deleted = append(deleted, key)
	}
	return deleted, nil
}

func (s *FilesystemStore) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*ObjectMeta, error) {
	rc, meta, err := s.GetObject(ctx, srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return s.PutObject(ctx, dstBucket, dstKey, rc, PutOptions{
		ContentType: meta.ContentType,
		Metadata:    cloneMetadata(meta.Metadata),
	})
}

func readObjectSidecar(path string) (objectSidecar, error) {
	var sc objectSidecar
	data, err := os.ReadFile(path)
	if err != nil {
		return sc, err
	}
	err = json.Unmarshal(data, &sc)
	return sc, err
}

func readBucketCreated(dir string) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(dir, "bucket.json"))
	if err != nil {
		return time.Time{}, err
	}
	var payload struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return time.Time{}, err
	}
	return payload.CreatedAt.UTC(), nil
}

func bucketHasObjects(root string) (bool, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return false, nil
	}
	found := false
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
		found = true
		return filepath.SkipAll
	})
	return found, err
}
