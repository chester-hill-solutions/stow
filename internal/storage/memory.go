package storage

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

type memObject struct {
	data []byte
	meta ObjectMeta
}

type memPart struct {
	info PartInfo
	data []byte
}

type memMultipart struct {
	upload MultipartUpload
	parts  map[int]memPart
}

type memBucket struct {
	info      BucketInfo
	objects   map[string]*memObject
	multipart map[string]*memMultipart
}

// MemoryStore is an in-memory Store implementation for tests.
type MemoryStore struct {
	mu      sync.RWMutex
	buckets map[string]*memBucket
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{buckets: make(map[string]*memBucket)}
}

func (s *MemoryStore) bucket(name string) (*memBucket, error) {
	b, ok := s.buckets[name]
	if !ok {
		return nil, ErrBucketNotFound
	}
	return b, nil
}

func (s *MemoryStore) CreateBucket(_ context.Context, name string) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.buckets[name]; ok {
		return ErrBucketExists
	}
	now := time.Now().UTC()
	s.buckets[name] = &memBucket{
		info:      BucketInfo{Name: name, CreationDate: now},
		objects:   make(map[string]*memObject),
		multipart: make(map[string]*memMultipart),
	}
	return nil
}

func (s *MemoryStore) DeleteBucket(_ context.Context, name string) error {
	if err := validateBucketName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.buckets[name]
	if !ok {
		return ErrBucketNotFound
	}
	if len(b.objects) > 0 {
		return ErrBucketNotEmpty
	}
	delete(s.buckets, name)
	return nil
}

func (s *MemoryStore) HeadBucket(_ context.Context, name string) (*BucketInfo, error) {
	if err := validateBucketName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.buckets[name]
	if !ok {
		return nil, ErrBucketNotFound
	}
	info := b.info
	return &info, nil
}

func (s *MemoryStore) ListBuckets(_ context.Context) ([]BucketInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]BucketInfo, 0, len(s.buckets))
	for _, b := range s.buckets {
		out = append(out, b.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemoryStore) PutObject(_ context.Context, bucket, key string, body io.Reader, opts PutOptions) (*ObjectMeta, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	etag, data, err := etagForReader(body)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	var existing *ObjectMeta
	if object, exists := b.objects[key]; exists {
		copy := object.meta
		existing = &copy
	}
	if err := checkWritePreconditions(opts, existing); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	meta := ObjectMeta{
		Bucket:            bucket,
		Key:               key,
		Size:              int64(len(data)),
		ETag:              etag,
		ContentType:       opts.ContentType,
		LastModified:      now,
		Metadata:          cloneMetadata(opts.Metadata),
		ChecksumAlgorithm: opts.ChecksumAlgorithm,
		ChecksumValue:     opts.ChecksumValue,
	}
	b.objects[key] = &memObject{data: data, meta: meta}
	out := meta
	return &out, nil
}

func (s *MemoryStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, nil, ErrBucketNotFound
	}
	obj, ok := b.objects[key]
	if !ok {
		return nil, nil, ErrObjectNotFound
	}
	meta := obj.meta
	return io.NopCloser(bytes.NewReader(obj.data)), &meta, nil
}

func (s *MemoryStore) HeadObject(_ context.Context, bucket, key string) (*ObjectMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	obj, ok := b.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	meta := obj.meta
	return &meta, nil
}

func (s *MemoryStore) DeleteObject(_ context.Context, bucket, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return ErrBucketNotFound
	}
	if _, ok := b.objects[key]; !ok {
		return ErrObjectNotFound
	}
	delete(b.objects, key)
	return nil
}

func (s *MemoryStore) DeleteObjects(_ context.Context, bucket string, keys []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	var deleted []string
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return deleted, err
		}
		if _, ok := b.objects[key]; !ok {
			continue
		}
		delete(b.objects, key)
		deleted = append(deleted, key)
	}
	return deleted, nil
}

func (s *MemoryStore) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*ObjectMeta, error) {
	rc, meta, err := s.GetObject(ctx, srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return s.PutObject(ctx, dstBucket, dstKey, rc, PutOptions{
		ContentType:       meta.ContentType,
		Metadata:          cloneMetadata(meta.Metadata),
		ChecksumAlgorithm: meta.ChecksumAlgorithm,
		ChecksumValue:     meta.ChecksumValue,
	})
}

func (s *MemoryStore) ListObjectsV2(_ context.Context, bucket string, opts ListOptions) (*ListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}

	items := make([]ObjectMeta, 0, len(b.objects))
	for key, obj := range b.objects {
		if opts.Prefix != "" && !strings.HasPrefix(key, opts.Prefix) {
			continue
		}
		items = append(items, obj.meta)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return PaginateObjects(items, opts), nil
}

func (s *MemoryStore) CreateMultipartUpload(_ context.Context, bucket, key string) (*MultipartUpload, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	uploadID, err := newUploadID()
	if err != nil {
		return nil, err
	}
	upload := MultipartUpload{
		UploadID:  uploadID,
		Bucket:    bucket,
		Key:       key,
		Initiated: time.Now().UTC(),
	}
	b.multipart[uploadID] = &memMultipart{upload: upload, parts: make(map[int]memPart)}
	out := upload
	return &out, nil
}

func (s *MemoryStore) UploadPart(_ context.Context, uploadID string, partNumber int, body io.Reader) (*PartInfo, error) {
	if partNumber < 1 || partNumber > 10000 {
		return nil, ErrInvalidPart
	}
	etag, data, err := etagForReader(body)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mp, _, ok := s.findMultipart(uploadID)
	if !ok {
		return nil, ErrUploadNotFound
	}
	now := time.Now().UTC()
	part := PartInfo{
		PartNumber:   partNumber,
		ETag:         etag,
		Size:         int64(len(data)),
		LastModified: now,
	}
	mp.parts[partNumber] = memPart{info: part, data: data}
	out := part
	return &out, nil
}

func (s *MemoryStore) CompleteMultipartUpload(_ context.Context, uploadID string, parts []PartInfo) (*ObjectMeta, error) {
	if len(parts) == 0 {
		return nil, ErrInvalidUpload
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mp, bucket, ok := s.findMultipart(uploadID)
	if !ok {
		return nil, ErrUploadNotFound
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	var combined []byte
	partETags := make([]string, 0, len(parts))
	for _, p := range parts {
		part, ok := mp.parts[p.PartNumber]
		if !ok {
			return nil, ErrInvalidPart
		}
		if p.ETag == "" || !etagEqual(p.ETag, part.info.ETag) {
			return nil, ErrInvalidPart
		}
		partETags = append(partETags, part.info.ETag)
		combined = append(combined, part.data...)
	}

	etag := compositeETag(partETags)
	now := time.Now().UTC()
	meta := ObjectMeta{
		Bucket:       mp.upload.Bucket,
		Key:          mp.upload.Key,
		Size:         int64(len(combined)),
		ETag:         etag,
		LastModified: now,
	}
	bucket.objects[mp.upload.Key] = &memObject{data: combined, meta: meta}
	delete(bucket.multipart, uploadID)
	out := meta
	return &out, nil
}

func (s *MemoryStore) AbortMultipartUpload(_ context.Context, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mp, bucket, ok := s.findMultipart(uploadID)
	if !ok {
		return ErrUploadNotFound
	}
	delete(bucket.multipart, mp.upload.UploadID)
	return nil
}

func (s *MemoryStore) ListParts(_ context.Context, uploadID string) ([]PartInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mp, _, ok := s.findMultipart(uploadID)
	if !ok {
		return nil, ErrUploadNotFound
	}
	nums := make([]int, 0, len(mp.parts))
	for n := range mp.parts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]PartInfo, 0, len(nums))
	for _, n := range nums {
		out = append(out, mp.parts[n].info)
	}
	return out, nil
}

func (s *MemoryStore) ValidateMultipartUpload(_ context.Context, uploadID, bucket, key string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mp, storedBucket, ok := s.findMultipart(uploadID)
	if !ok || storedBucket.info.Name != bucket || mp.upload.Key != key {
		return ErrNoSuchUpload
	}
	return nil
}

func (s *MemoryStore) Close() error {
	return nil
}

func (s *MemoryStore) ListMultipartUploads(_ context.Context, bucket string, opts MultipartListOptions) (*MultipartListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, ok := s.buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	uploads := make([]MultipartUpload, 0, len(b.multipart))
	for _, upload := range b.multipart {
		uploads = append(uploads, upload.upload)
	}
	return PaginateMultipartUploads(uploads, opts), nil
}

func (s *MemoryStore) findMultipart(uploadID string) (*memMultipart, *memBucket, bool) {
	for _, b := range s.buckets {
		if mp, ok := b.multipart[uploadID]; ok {
			return mp, b, true
		}
	}
	return nil, nil, false
}
