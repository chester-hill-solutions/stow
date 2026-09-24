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
	"time"
)

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
	partETags := make([]string, 0, len(parts))
	for _, p := range parts {
		partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", p.PartNumber))
		data, err := os.ReadFile(partPath)
		if err != nil {
			return nil, ErrInvalidPart
		}
		storedETag := etagForBytes(data)
		if p.ETag == "" || !etagEqual(p.ETag, storedETag) {
			return nil, ErrInvalidPart
		}
		partETags = append(partETags, storedETag)
		combined = append(combined, data...)
	}
	etag := compositeETag(partETags)
	objPath := s.objectPath(manifest.Bucket, manifest.Key)
	if err := writeBytesAtomic(objPath, combined); err != nil {
		return nil, err
	}
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
		LastModified: now,
	}, nil
}

func (s *FilesystemStore) ListMultipartUploads(_ context.Context, bucket string, opts MultipartListOptions) (*MultipartListResult, error) {
	if err := validateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, ".multipart"))
	if err != nil {
		if os.IsNotExist(err) {
			return PaginateMultipartUploads(nil, opts), nil
		}
		return nil, err
	}
	uploads := make([]MultipartUpload, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readMultipartManifest(filepath.Join(s.dataDir, ".multipart", entry.Name()))
		if err != nil || manifest.Bucket != bucket {
			continue
		}
		uploads = append(uploads, MultipartUpload{
			UploadID:  entry.Name(),
			Bucket:    manifest.Bucket,
			Key:       manifest.Key,
			Initiated: manifest.Initiated,
		})
	}
	return PaginateMultipartUploads(uploads, opts), nil
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

func newUploadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
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
