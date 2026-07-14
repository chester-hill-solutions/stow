package storage_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestFilesystemStoreObjectLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if err := store.CreateBucket(ctx, "photos"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	_, err = store.PutObject(ctx, "photos", "a/b.txt", strings.NewReader("hello"), storage.PutOptions{
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}

	objPath := filepath.Join(dir, "buckets", "photos", "objects", "a", "b.txt")
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("object file missing: %v", err)
	}

	rc, meta, err := store.GetObject(ctx, "photos", "a/b.txt")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", string(data))
	}
	if meta.ContentType != "text/plain" {
		t.Fatalf("content type = %q", meta.ContentType)
	}

	list, err := store.ListObjectsV2(ctx, "photos", storage.ListOptions{Prefix: "a/"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Objects) != 1 {
		t.Fatalf("objects = %+v", list.Objects)
	}

	if err := store.DeleteObject(ctx, "photos", "a/b.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(objPath); !os.IsNotExist(err) {
		t.Fatalf("object still exists after delete")
	}
}

func TestFilesystemStoreMultipart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	_ = store.CreateBucket(ctx, "data")

	upload, err := store.CreateMultipartUpload(ctx, "data", "big.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}

	p1, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("foo"))
	if err != nil {
		t.Fatalf("part 1: %v", err)
	}
	p2, err := store.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("bar"))
	if err != nil {
		t.Fatalf("part 2: %v", err)
	}

	_, err = store.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*p1, *p2})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".multipart", upload.UploadID)); !os.IsNotExist(err) {
		t.Fatalf("multipart staging dir should be removed")
	}

	rc, meta, err := store.GetObject(ctx, "data", "big.bin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "foobar" {
		t.Fatalf("data = %q", string(data))
	}
	if meta.Size != 6 {
		t.Fatalf("size = %d", meta.Size)
	}
}

func TestFilesystemStoreDeleteBucketNotEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, _ := storage.NewFilesystemStore(dir)
	_ = store.CreateBucket(ctx, "b")
	_, _ = store.PutObject(ctx, "b", "k", strings.NewReader("x"), storage.PutOptions{})

	if err := store.DeleteBucket(ctx, "b"); err != storage.ErrBucketNotEmpty {
		t.Fatalf("delete bucket = %v, want ErrBucketNotEmpty", err)
	}
}
