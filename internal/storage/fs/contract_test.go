package fs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
)

func newContractStore(t *testing.T) *fs.FilesystemStore {
	t.Helper()
	store, err := fs.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestFilesystemStoreBucketContract(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "source"); err != nil {
		t.Fatalf("create source bucket: %v", err)
	}
	if err := store.CreateBucket(ctx, "destination"); err != nil {
		t.Fatalf("create destination bucket: %v", err)
	}
	if err := store.CreateBucket(ctx, "source"); !errors.Is(err, storage.ErrBucketExists) {
		t.Fatalf("duplicate bucket error = %v", err)
	}
	if _, err := store.HeadBucket(ctx, "source"); err != nil {
		t.Fatalf("head bucket: %v", err)
	}
	buckets, err := store.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("list buckets = %+v, want 2", buckets)
	}
	if err := store.DeleteBucket(ctx, "source"); err != nil {
		t.Fatalf("delete empty source bucket: %v", err)
	}
}

func TestFilesystemStoreObjectContract(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "source"); err != nil {
		t.Fatalf("create source bucket: %v", err)
	}
	if err := store.CreateBucket(ctx, "destination"); err != nil {
		t.Fatalf("create destination bucket: %v", err)
	}
	first, err := store.PutObject(ctx, "source", "one", strings.NewReader("one"), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put first object: %v", err)
	}
	if _, err := store.PutObject(ctx, "source", "two", strings.NewReader("two"), storage.PutOptions{}); err != nil {
		t.Fatalf("put second object: %v", err)
	}
	meta, err := store.HeadObject(ctx, "source", "one")
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	if meta.VersionID == "" {
		t.Fatal("expected object version")
	}
	if _, err := store.CopyObject(ctx, "source", "one", "destination", "copy"); err != nil {
		t.Fatalf("copy object: %v", err)
	}
	deleted, err := store.DeleteObjects(ctx, "source", []string{"two", "missing"})
	if err != nil {
		t.Fatalf("delete objects: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "two" {
		t.Fatalf("delete objects = %v, want [two]", deleted)
	}
	list, err := store.ListObjectsV2(ctx, "source", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(list.Objects) != 1 {
		t.Fatalf("list objects = %+v, want one", list)
	}
	if err := store.DeleteObject(ctx, "source", "one"); err != nil {
		t.Fatalf("delete remaining object: %v", err)
	}
	if first.ETag == "" {
		t.Fatal("expected object ETag")
	}
}

func TestFilesystemStoreMultipartContract(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "source"); err != nil {
		t.Fatalf("create source bucket: %v", err)
	}
	upload, err := store.CreateMultipartUpload(ctx, "source", "multipart.bin")
	if err != nil {
		t.Fatalf("create multipart upload: %v", err)
	}
	if err := store.ValidateMultipartUpload(ctx, upload.UploadID, upload.Bucket, upload.Key); err != nil {
		t.Fatalf("validate multipart upload: %v", err)
	}
	if err := store.ValidateMultipartUpload(ctx, upload.UploadID, "source", "wrong"); !errors.Is(err, storage.ErrNoSuchUpload) {
		t.Fatalf("invalid multipart validation = %v", err)
	}
	part, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("part"))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if parts, err := store.ListParts(ctx, upload.UploadID); err != nil || len(parts) != 1 || parts[0].ETag != part.ETag {
		t.Fatalf("list parts = %+v, err = %v", parts, err)
	}
	if uploads, err := store.ListMultipartUploads(ctx, "source", storage.MultipartListOptions{}); err != nil || len(uploads.Uploads) != 1 {
		t.Fatalf("list uploads = %+v, err = %v", uploads, err)
	}
	if err := store.DeleteBucket(ctx, "source"); !errors.Is(err, storage.ErrBucketNotEmpty) {
		t.Fatalf("delete active upload bucket = %v", err)
	}
	if err := store.AbortMultipartUpload(ctx, upload.UploadID); err != nil {
		t.Fatalf("abort multipart upload: %v", err)
	}
	if err := store.AbortMultipartUpload(ctx, upload.UploadID); !errors.Is(err, storage.ErrUploadNotFound) {
		t.Fatalf("abort missing upload = %v", err)
	}
	if err := store.DeleteBucket(ctx, "source"); err != nil {
		t.Fatalf("delete empty source bucket: %v", err)
	}
}
