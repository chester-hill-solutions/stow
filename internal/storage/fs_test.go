package storage_test

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestFilesystemStoreSingleOwnerLock(t *testing.T) {
	dir := t.TempDir()
	first, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if _, err := storage.NewFilesystemStore(dir); err == nil {
		t.Fatal("expected second store to fail while lock is held")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	second, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("second store after close: %v", err)
	}
	_ = second.Close()
}

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

	objPath := filepath.Join(dir, "buckets", "photos", "objects", hex.EncodeToString([]byte("a/b.txt")))
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

func TestFilesystemStoreUsesSingleObjectRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.CreateBucket(ctx, "records"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	putMeta, err := store.PutObject(ctx, "records", "object", strings.NewReader("hello"), storage.PutOptions{
		ContentType: "text/plain",
		Metadata:    map[string]string{"owner": "dev"},
	})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	objectPath := filepath.Join(dir, "buckets", "records", "objects", hex.EncodeToString([]byte("object")))
	if _, err := os.Stat(objectPath + ".stowmeta"); !os.IsNotExist(err) {
		t.Fatalf("sidecar metadata still exists: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rc, gotMeta, err := reopened.GetObject(ctx, "records", "object")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q, want hello", data)
	}
	if gotMeta.ContentType != "text/plain" || gotMeta.Metadata["owner"] != "dev" || gotMeta.ETag != putMeta.ETag {
		t.Fatalf("metadata = %+v, put metadata = %+v", gotMeta, putMeta)
	}
}

func TestFilesystemStoreOpaqueKeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.CreateBucket(ctx, "opaque"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	keys := []string{"..", "a/../b", "a//b", "/leading"}
	for _, key := range keys {
		if _, err := store.PutObject(ctx, "opaque", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		rc, _, err := store.GetObject(ctx, "opaque", key)
		if err != nil {
			t.Fatalf("get %q: %v", key, err)
		}
		_ = rc.Close()
	}

	list, err := store.ListObjectsV2(ctx, "opaque", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Objects) != len(keys) {
		t.Fatalf("listed %d keys, want %d: %+v", len(list.Objects), len(keys), list.Objects)
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
	_ = store.CreateBucket(ctx, "bucket")
	_, _ = store.PutObject(ctx, "bucket", "k", strings.NewReader("x"), storage.PutOptions{})

	if err := store.DeleteBucket(ctx, "bucket"); err != storage.ErrBucketNotEmpty {
		t.Fatalf("delete bucket = %v, want ErrBucketNotEmpty", err)
	}
}
