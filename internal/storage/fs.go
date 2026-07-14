package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func (s *FilesystemStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error) {
	meta, err := s.HeadObject(context.Background(), bucket, key)
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
		LastModified:   st.ModTime().UTC(),
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
	var deleted []string
	for _, key := range keys {
		if err := s.DeleteObject(context.Background(), bucket, key); err != nil {
			if err == ErrObjectNotFound {
				continue
			}
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

func (s *FilesystemStore) ListObjectsV2(_ context.Context, bucket string, opts ListOptions) (*ListResult, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}

	type item struct {
		key  string
		meta ObjectMeta
	}
	var items []item
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
		items = append(items, item{
			key: key,
			meta: ObjectMeta{
				Bucket:       bucket,
				Key:          key,
				Size:         st.Size(),
				LastModified: st.ModTime().UTC(),
				ContentType:  sidecar.ContentType,
				Metadata:     cloneMetadata(sidecar.Metadata),
				ETag:         sidecar.ETag,
			},
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	startAfter := listStartAfter(opts)
	maxKeys := maxKeysOrDefault(opts.MaxKeys)
	result := &ListResult{}
	prefixSet := map[string]struct{}{}

	for _, it := range items {
		if startAfter != "" && it.key <= startAfter {
			continue
		}
		if cp := commonPrefixFor(it.key, opts.Prefix, opts.Delimiter); cp != "" {
			if _, ok := prefixSet[cp]; !ok {
				prefixSet[cp] = struct{}{}
				result.CommonPrefixes = append(result.CommonPrefixes, cp)
			}
			continue
		}
		if len(result.Objects) >= maxKeys {
			result.IsTruncated = true
			result.NextContinuationToken = it.key
			break
		}
		result.Objects = append(result.Objects, it.meta)
	}
	sort.Strings(result.CommonPrefixes)
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	if opts.ContinuationToken != "" {
		result.ContinuationToken = opts.ContinuationToken
	}
	return result, nil
}

type multipartManifest struct {
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	Initiated time.Time `json:"initiated"`
}

func (s *FilesystemStore) CreateMultipartUpload(_ context.Context, bucket, key string) (*MultipartUpload, error) {
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

	uploadID, err := newUploadID()
	if err != nil {
		return nil, err
	}
	dir := s.multipartDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	manifest := multipartManifest{
		Bucket:    bucket,
		Key:       key,
		Initiated: time.Now().UTC(),
	}
	if err := writeJSONAtomic(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &MultipartUpload{
		UploadID:  uploadID,
		Bucket:    bucket,
		Key:       key,
		Initiated: manifest.Initiated,
	}, nil
}

func (s *FilesystemStore) UploadPart(_ context.Context, uploadID string, partNumber int, body io.Reader) (*PartInfo, error) {
	if partNumber < 1 || partNumber > 10000 {
		return nil, ErrInvalidPart
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, ErrUploadNotFound
	}
	etag, data, err := etagForReader(body)
	if err != nil {
		return nil, err
	}
	partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", partNumber))
	if err := writeBytesAtomic(partPath, data); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &PartInfo{
		PartNumber:   partNumber,
		ETag:         etag,
		Size:         int64(len(data)),
		LastModified: now,
	}, nil
}

func (s *FilesystemStore) CompleteMultipartUpload(_ context.Context, uploadID string, parts []PartInfo) (*ObjectMeta, error) {
	if len(parts) == 0 {
		return nil, ErrInvalidUpload
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	manifest, err := readMultipartManifest(dir)
	if err != nil {
		return nil, ErrUploadNotFound
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	var combined []byte
	for _, p := range parts {
		partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", p.PartNumber))
		data, err := os.ReadFile(partPath)
		if err != nil {
			return nil, ErrInvalidPart
		}
		combined = append(combined, data...)
	}

	objPath := s.objectPath(manifest.Bucket, manifest.Key)
	if err := writeBytesAtomic(objPath, combined); err != nil {
		return nil, err
	}
	etag := etagForBytes(combined)
	sidecar := objectSidecar{ETag: etag}
	if err := writeJSONAtomic(s.metaPath(manifest.Bucket, manifest.Key), sidecar); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(dir)

	now := time.Now().UTC()
	return &ObjectMeta{
		Bucket:       manifest.Bucket,
		Key:          manifest.Key,
		Size:         int64(len(combined)),
		ETag:         etag,
		LastModified:   now,
	}, nil
}

func (s *FilesystemStore) AbortMultipartUpload(_ context.Context, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ErrUploadNotFound
	}
	return os.RemoveAll(dir)
}

func (s *FilesystemStore) ListParts(_ context.Context, uploadID string) ([]PartInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, ErrUploadNotFound
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var parts []PartInfo
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "part-") {
			continue
		}
		var num int
		if _, err := fmt.Sscanf(e.Name(), "part-%05d", &num); err != nil {
			continue
		}
		st, err := e.Info()
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		parts = append(parts, PartInfo{
			PartNumber:   num,
			ETag:         etagForBytes(data),
			Size:         st.Size(),
			LastModified: st.ModTime().UTC(),
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

func writeBytesAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, data)
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

func newUploadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
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

func readMultipartManifest(dir string) (multipartManifest, error) {
	var m multipartManifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}
