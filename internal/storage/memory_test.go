package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestMemoryStoreObjectLifecycle(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()

	if err := store.CreateBucket(ctx, "photos"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	meta, err := store.PutObject(ctx, "photos", "a/b.txt", strings.NewReader("hello"), storage.PutOptions{
		ContentType: "text/plain",
		Metadata:    map[string]string{"x-amz-meta-owner": "dev"},
	})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if meta.Size != 5 {
		t.Fatalf("size = %d, want 5", meta.Size)
	}

	got, err := store.HeadObject(ctx, "photos", "a/b.txt")
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	if got.ContentType != "text/plain" {
		t.Fatalf("content type = %q", got.ContentType)
	}

	rc, _, err := store.GetObject(ctx, "photos", "a/b.txt")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", string(data))
	}

	list, err := store.ListObjectsV2(ctx, "photos", storage.ListOptions{Prefix: "a/"})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(list.Objects) != 1 || list.Objects[0].Key != "a/b.txt" {
		t.Fatalf("unexpected list: %+v", list)
	}

	if err := store.DeleteObject(ctx, "photos", "a/b.txt"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	if _, err := store.HeadObject(ctx, "photos", "a/b.txt"); err != storage.ErrObjectNotFound {
		t.Fatalf("head after delete = %v", err)
	}
}

func TestMemoryStoreMultipart(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	if err := store.CreateBucket(ctx, "data"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	upload, err := store.CreateMultipartUpload(ctx, "data", "big.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}

	p1, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("foo"))
	if err != nil {
		t.Fatalf("upload part 1: %v", err)
	}
	p2, err := store.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("bar"))
	if err != nil {
		t.Fatalf("upload part 2: %v", err)
	}

	meta, err := store.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*p1, *p2})
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	if meta.Size != 6 {
		t.Fatalf("size = %d, want 6", meta.Size)
	}

	rc, _, err := store.GetObject(ctx, "data", "big.bin")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "foobar" {
		t.Fatalf("data = %q", string(data))
	}
}

func TestMemoryStoreCopyObject(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	_ = store.CreateBucket(ctx, "src")
	_ = store.CreateBucket(ctx, "dst")
	_, _ = store.PutObject(ctx, "src", "file", strings.NewReader("payload"), storage.PutOptions{})

	_, err := store.CopyObject(ctx, "src", "file", "dst", "copy")
	if err != nil {
		t.Fatalf("copy object: %v", err)
	}
	rc, _, err := store.GetObject(ctx, "dst", "copy")
	if err != nil {
		t.Fatalf("get copy: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "payload" {
		t.Fatalf("copy data = %q", string(data))
	}
}
