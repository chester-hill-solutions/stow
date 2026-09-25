package storage_test

import (
	"context"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
)

func runMultipartListContract(t *testing.T, store storage.Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "uploads"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	upload, err := store.CreateMultipartUpload(ctx, "uploads", "dir/object.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}

	result, err := store.ListMultipartUploads(ctx, "uploads", storage.MultipartListOptions{})
	if err != nil {
		t.Fatalf("list uploads: %v", err)
	}
	if len(result.Uploads) != 1 {
		t.Fatalf("uploads = %+v, want one", result.Uploads)
	}
	if result.Uploads[0].UploadID != upload.UploadID || result.Uploads[0].Key != upload.Key {
		t.Fatalf("upload = %+v, want %+v", result.Uploads[0], upload)
	}
}

func TestPaginateMultipartUploadsMarkers(t *testing.T) {
	uploads := []storage.MultipartUpload{
		{UploadID: "a", Key: "a.bin"},
		{UploadID: "b", Key: "b.bin"},
	}
	result := storage.PaginateMultipartUploads(uploads, storage.MultipartListOptions{MaxUploads: 1})
	if !result.IsTruncated {
		t.Fatal("expected truncated result")
	}
	if len(result.Uploads) != 1 || result.Uploads[0].UploadID != "a" {
		t.Fatalf("uploads = %+v", result.Uploads)
	}
	if result.NextKeyMarker != "a.bin" || result.NextUploadIDMarker != "a" {
		t.Fatalf("next markers = %q/%q, want a.bin/a", result.NextKeyMarker, result.NextUploadIDMarker)
	}
}

func TestMemoryStoreMultipartListContract(t *testing.T) {
	runMultipartListContract(t, storage.NewMemoryStore())
}

func TestFilesystemStoreMultipartListContract(t *testing.T) {
	runMultipartListContract(t, mustFilesystemStore(t))
}

func mustFilesystemStore(t *testing.T) *fs.FilesystemStore {
	t.Helper()
	store, err := fs.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("new filesystem store: %v", err)
	}
	return store
}
