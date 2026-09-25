package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	storage "github.com/chester-hill-solutions/stow/internal/storage"
)

type multipartManifest struct {
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	Initiated time.Time `json:"initiated"`
}

func (s *FilesystemStore) CreateMultipartUpload(_ context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	if err := storage.ValidateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, storage.ErrBucketNotFound
	}

	uploadID, err := storage.NewUploadID()
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
	return &storage.MultipartUpload{
		UploadID:  uploadID,
		Bucket:    bucket,
		Key:       key,
		Initiated: manifest.Initiated,
	}, nil
}

func (s *FilesystemStore) GetMultipartUpload(_ context.Context, uploadID string) (*storage.MultipartUpload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	manifest, err := readMultipartManifest(s.multipartDir(uploadID))
	if err != nil {
		return nil, storage.ErrUploadNotFound
	}
	return &storage.MultipartUpload{
		UploadID:  uploadID,
		Bucket:    manifest.Bucket,
		Key:       manifest.Key,
		Initiated: manifest.Initiated,
	}, nil
}

func (s *FilesystemStore) UploadPart(_ context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	if partNumber < 1 || partNumber > 10000 {
		return nil, storage.ErrInvalidPart
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, storage.ErrUploadNotFound
	}
	etag, data, err := storage.ETagForReader(body)
	if err != nil {
		return nil, err
	}
	partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", partNumber))
	if err := writeBytesAtomic(partPath, data); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &storage.PartInfo{
		PartNumber:   partNumber,
		ETag:         etag,
		Size:         int64(len(data)),
		LastModified: now,
	}, nil
}

func (s *FilesystemStore) CompleteMultipartUpload(_ context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	if len(parts) == 0 {
		return nil, storage.ErrInvalidUpload
	}
	if err := storage.ValidateMultipartPartNumbers(parts); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	manifest, err := readMultipartManifest(dir)
	if err != nil {
		return nil, storage.ErrUploadNotFound
	}

	var combined []byte
	partETags := make([]string, 0, len(parts))
	for _, p := range parts {
		partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", p.PartNumber))
		data, err := os.ReadFile(partPath)
		if err != nil {
			return nil, storage.ErrInvalidPart
		}
		storedETag := storage.ETagForBytes(data)
		if p.ETag == "" || !storage.ETagEqual(p.ETag, storedETag) {
			return nil, storage.ErrInvalidPart
		}
		partETags = append(partETags, storedETag)
		combined = append(combined, data...)
	}
	etag := storage.CompositeETag(partETags)
	recordVersion, err := storage.NewRecordVersion()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := objectRecord{RecordVersion: recordVersion, Data: combined, ETag: etag, LastModified: now}
	objPath := s.objectPath(manifest.Bucket, manifest.Key)
	if err := writeObjectRecord(objPath, record); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		// The object is already committed; retain the upload so callers can retry cleanup.
		return nil, fmt.Errorf("cleanup multipart upload: %w", err)
	}

	meta := record.meta(manifest.Bucket, manifest.Key)
	return &meta, nil
}

func (s *FilesystemStore) ValidateMultipartUpload(_ context.Context, uploadID, bucket, key string) error {
	manifest, err := readMultipartManifest(s.multipartDir(uploadID))
	if err != nil || manifest.Bucket != bucket || manifest.Key != key {
		return storage.ErrNoSuchUpload
	}
	return nil
}

func (s *FilesystemStore) ListMultipartUploads(_ context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	if err := storage.ValidateBucketName(bucket); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.bucketDir(bucket)); os.IsNotExist(err) {
		return nil, storage.ErrBucketNotFound
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, ".multipart"))
	if err != nil {
		if os.IsNotExist(err) {
			return storage.PaginateMultipartUploads(nil, opts), nil
		}
		return nil, err
	}
	uploads := make([]storage.MultipartUpload, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readMultipartManifest(filepath.Join(s.dataDir, ".multipart", entry.Name()))
		if err != nil || manifest.Bucket != bucket {
			continue
		}
		uploads = append(uploads, storage.MultipartUpload{
			UploadID:  entry.Name(),
			Bucket:    manifest.Bucket,
			Key:       manifest.Key,
			Initiated: manifest.Initiated,
		})
	}
	return storage.PaginateMultipartUploads(uploads, opts), nil
}

func (s *FilesystemStore) AbortMultipartUpload(_ context.Context, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return storage.ErrUploadNotFound
	}
	return os.RemoveAll(dir)
}

func (s *FilesystemStore) listParts(_ context.Context, uploadID string) ([]storage.PartInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.multipartDir(uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, storage.ErrUploadNotFound
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var parts []storage.PartInfo
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
		parts = append(parts, storage.PartInfo{
			PartNumber:   num,
			ETag:         storage.ETagForBytes(data),
			Size:         st.Size(),
			LastModified: st.ModTime().UTC(),
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

// ListPartsPage returns a marker-paginated page of uploaded parts.
func (s *FilesystemStore) ListPartsPage(ctx context.Context, uploadID string, opts storage.ListPartsOptions) (*storage.ListPartsResult, error) {
	parts, err := s.listParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return storage.PaginateParts(parts, opts), nil
}

func (s *FilesystemStore) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	return s.listParts(ctx, uploadID)
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
