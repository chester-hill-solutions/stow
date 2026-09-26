package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// Multipart uploads stage their parts as real files under the reserved internal
// directory and are assembled into the key's own path on completion. Nothing
// here is visible to a caller browsing the workspace: a half-finished upload is
// stow's business, not the caller's.
const (
	multipartDir   = "multipart"
	multipartState = "upload.json"
)

// uploadState is the on-disk record of an in-progress upload.
type uploadState struct {
	UploadID  string             `json:"upload_id"`
	Bucket    string             `json:"bucket"`
	Key       string             `json:"key"`
	Initiated time.Time          `json:"initiated"`
	Parts     []storage.PartInfo `json:"parts"`
}

// uploadDir is where one upload's parts live.
func (s *Store) uploadDir(uploadID string) string {
	return InternalPath(s.root, multipartDir, uploadID)
}

// partPath is where one part's bytes live.
func (s *Store) partPath(uploadID string, partNumber int) string {
	return filepath.Join(s.uploadDir(uploadID), "part-"+strconv.Itoa(partNumber))
}

func (s *Store) loadUpload(uploadID string) (*uploadState, error) {
	raw, err := os.ReadFile(filepath.Join(s.uploadDir(uploadID), multipartState))
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrUploadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("workspace store: read upload: %w", err)
	}
	state, err := decodeUploadState(raw)
	if err != nil {
		return nil, storage.ErrInvalidUpload
	}
	return state, nil
}

func (s *Store) saveUpload(state *uploadState) error {
	raw, err := encodeUploadState(state)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.uploadDir(state.UploadID), multipartState), raw); err != nil {
		return fmt.Errorf("workspace store: save upload: %w", err)
	}
	return nil
}

// CreateMultipartUpload starts an upload. The object does not exist until
// CompleteMultipartUpload, which is what makes an abandoned upload leave
// nothing in the workspace a caller can see.
func (s *Store) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uploadID, err := storage.NewUploadID()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.uploadDir(uploadID), 0o755); err != nil {
		return nil, fmt.Errorf("workspace store: stage upload: %w", err)
	}
	state := &uploadState{UploadID: uploadID, Bucket: bucket, Key: key, Initiated: s.now().UTC()}
	if err := s.saveUpload(state); err != nil {
		return nil, err
	}
	return &storage.MultipartUpload{
		UploadID:  uploadID,
		Bucket:    bucket,
		Key:       key,
		Initiated: state.Initiated,
	}, nil
}

// GetMultipartUpload returns an in-progress upload.
func (s *Store) GetMultipartUpload(_ context.Context, uploadID string) (*storage.MultipartUpload, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	state, err := s.loadUpload(uploadID)
	if err != nil {
		return nil, err
	}
	return &storage.MultipartUpload{
		UploadID:  state.UploadID,
		Bucket:    state.Bucket,
		Key:       state.Key,
		Initiated: state.Initiated,
	}, nil
}

// ValidateMultipartUpload confirms an upload exists and belongs to the bucket
// and key it is being completed against.
func (s *Store) ValidateMultipartUpload(_ context.Context, uploadID, bucket, key string) error {
	state, err := s.loadUpload(uploadID)
	if err != nil {
		return err
	}
	if state.Bucket != bucket || state.Key != key {
		return storage.ErrInvalidUpload
	}
	return nil
}

// UploadPart stores one part's bytes and records its size and ETag.
func (s *Store) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if partNumber < 1 || partNumber > 10000 {
		return nil, storage.ErrInvalidPart
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadUpload(uploadID)
	if err != nil {
		return nil, err
	}
	data, err := storage.BytesOf(body)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(s.partPath(uploadID, partNumber), data); err != nil {
		return nil, fmt.Errorf("workspace store: write part: %w", err)
	}
	part := storage.PartInfo{
		PartNumber:   partNumber,
		ETag:         storage.ETagForBytes(data),
		Size:         int64(len(data)),
		LastModified: s.now().UTC(),
	}
	state.Parts = upsertPart(state.Parts, part)
	if err := s.saveUpload(state); err != nil {
		return nil, err
	}
	return &part, nil
}

// upsertPart replaces a part with the same number, or appends it.
func upsertPart(parts []storage.PartInfo, part storage.PartInfo) []storage.PartInfo {
	for i := range parts {
		if parts[i].PartNumber == part.PartNumber {
			parts[i] = part
			return parts
		}
	}
	return append(parts, part)
}

// CompleteMultipartUpload assembles the staged parts into the key's own file,
// which is the same atomic write a single PutObject uses.
func (s *Store) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := storage.ValidateMultipartPartNumbers(parts); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.loadUpload(uploadID)
	if err != nil {
		return nil, err
	}
	assembled, etags, err := s.assemble(uploadID, parts)
	if err != nil {
		return nil, err
	}
	meta, err := s.putLocked(ctx, state.Bucket, state.Key, bytes.NewReader(assembled),
		storage.PutOptions{ContentType: detectContentTypeFromBytes(assembled)})
	if err != nil {
		return nil, err
	}
	// This backend already had the single-part rule right; it now shares the
	// rule with the other two rather than keeping the one correct copy of it.
	meta.ETag = storage.CompletionETag(etags)
	if err := os.RemoveAll(s.uploadDir(uploadID)); err != nil {
		return nil, fmt.Errorf("workspace store: clear upload: %w", err)
	}
	return meta, nil
}

// assemble concatenates the requested parts in order.
func (s *Store) assemble(uploadID string, parts []storage.PartInfo) ([]byte, []string, error) {
	var out []byte
	etags := make([]string, 0, len(parts))
	for _, part := range parts {
		data, err := os.ReadFile(s.partPath(uploadID, part.PartNumber))
		if err != nil {
			return nil, nil, storage.ErrInvalidPart
		}
		if !storage.ETagEqual(storage.ETagForBytes(data), part.ETag) {
			return nil, nil, storage.ErrChecksumMismatch
		}
		out = append(out, data...)
		etags = append(etags, part.ETag)
	}
	return out, etags, nil
}

// AbortMultipartUpload discards an upload and everything it staged.
func (s *Store) AbortMultipartUpload(_ context.Context, uploadID string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadUpload(uploadID); err != nil {
		return err
	}
	return os.RemoveAll(s.uploadDir(uploadID))
}

// ListPartsPage returns one page of an upload's staged parts. It is the
// paginated form the S3 layer uses; ListParts returns everything, which is what
// the storage.Store contract asks for.
func (s *Store) ListPartsPage(ctx context.Context, uploadID string, opts storage.ListPartsOptions) (*storage.ListPartsResult, error) {
	parts, err := s.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return storage.PaginateParts(parts, opts), nil
}

// ListParts returns an upload's staged parts.
func (s *Store) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	if _, err := s.GetMultipartUpload(ctx, uploadID); err != nil {
		return nil, err
	}
	state, err := s.loadUpload(uploadID)
	if err != nil {
		return nil, err
	}
	return state.Parts, nil
}

// ListMultipartUploads returns in-progress uploads for a bucket.
func (s *Store) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uploads, err := s.scanUploads(bucket)
	if err != nil {
		return nil, err
	}
	return storage.PaginateMultipartUploads(uploads, opts), nil
}

// scanUploads reads every staged upload for a bucket.
func (s *Store) scanUploads(bucket string) ([]storage.MultipartUpload, error) {
	entries, err := os.ReadDir(InternalPath(s.root, multipartDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace store: list uploads: %w", err)
	}
	var uploads []storage.MultipartUpload
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := s.loadUpload(entry.Name())
		if err != nil || state.Bucket != bucket {
			continue
		}
		uploads = append(uploads, storage.MultipartUpload{
			UploadID:  state.UploadID,
			Bucket:    state.Bucket,
			Key:       state.Key,
			Initiated: state.Initiated,
		})
	}
	sort.Slice(uploads, func(i, j int) bool { return uploads[i].Key < uploads[j].Key })
	return uploads, nil
}

// activeUploads reports whether a bucket has an upload in progress, which is
// what stops a bucket being deleted out from under one.
func (s *Store) activeUploads(bucket string) (bool, error) {
	uploads, err := s.scanUploads(bucket)
	if err != nil {
		return false, err
	}
	return len(uploads) > 0, nil
}
