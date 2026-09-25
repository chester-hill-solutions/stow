package fs_test

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
)

func TestFilesystemStoreSingleOwnerLock(t *testing.T) {
	dir := t.TempDir()
	first, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if _, err := fs.NewFilesystemStore(dir); err == nil {
		t.Fatal("expected second store to fail while lock is held")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	second, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("second store after close: %v", err)
	}
	_ = second.Close()
}

func TestFilesystemStoreObjectLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
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

func TestFilesystemStoreRemovesStaleAtomicTempsOnOpen(t *testing.T) {
	dir := t.TempDir()
	objectTemp := filepath.Join(dir, "buckets", "bucket", "objects", ".tmp-stale")
	multipartTemp := filepath.Join(dir, ".multipart", ".tmp-stale")
	for _, path := range []string{objectTemp, multipartTemp} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir temp parent: %v", err)
		}
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatalf("write temp: %v", err)
		}
	}
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	for _, path := range []string{objectTemp, multipartTemp} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale temp still exists at %s: %v", path, err)
		}
	}
}

func TestFilesystemStoreDoesNotCleanTempsBeforeOwningLock(t *testing.T) {
	dir := t.TempDir()
	first, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	tempPath := filepath.Join(dir, ".multipart", ".tmp-active")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		_ = first.Close()
		t.Fatalf("mkdir temp parent: %v", err)
	}
	if err := os.WriteFile(tempPath, []byte("in progress"), 0o600); err != nil {
		_ = first.Close()
		t.Fatalf("write temp: %v", err)
	}

	if _, err := fs.NewFilesystemStore(dir); err == nil {
		_ = first.Close()
		t.Fatal("expected second store to fail while lock is held")
	}
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("active temp was changed by a store that did not own the lock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("second store after close: %v", err)
	}
	_ = second.Close()
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("stale temp still exists after owner cleanup: %v", err)
	}
}

func TestFilesystemStoreRecoversStaleLockBeforeCleanup(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "buckets", "bucket", "objects", ".tmp-stale")
	lockPath := filepath.Join(dir, ".stow.lock")
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		t.Fatalf("mkdir temp parent: %v", err)
	}
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age stale lock: %v", err)
	}

	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store should recover stale lock: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("stale temp still exists after recovery: %v", err)
	}
}

func TestFilesystemStoreMultipartCompletionReportsCleanupFailureAndCanRecover(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateBucket(ctx, "data"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	upload, err := store.CreateMultipartUpload(ctx, "data", "big.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	part, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}

	uploadDir := filepath.Join(dir, ".multipart", upload.UploadID)
	if err := os.Chmod(uploadDir, 0o555); err != nil {
		t.Fatalf("protect upload directory: %v", err)
	}
	defer func() { _ = os.Chmod(uploadDir, 0o755) }()
	if _, err := store.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part}); err == nil {
		t.Fatal("completion ignored staging cleanup failure")
	}
	if _, err := os.Stat(uploadDir); err != nil {
		t.Fatalf("upload staging was not retained for recovery: %v", err)
	}
	if _, err := store.GetMultipartUpload(ctx, upload.UploadID); err != nil {
		t.Fatalf("upload lookup after cleanup failure: %v", err)
	}
	if rc, _, err := store.GetObject(ctx, "data", "big.bin"); err != nil {
		t.Fatalf("committed object after cleanup failure: %v", err)
	} else {
		_ = rc.Close()
	}

	if err := os.Chmod(uploadDir, 0o755); err != nil {
		t.Fatalf("restore upload directory: %v", err)
	}
	if _, err := store.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part}); err != nil {
		t.Fatalf("retry completion after restoring cleanup permissions: %v", err)
	}
	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Fatalf("upload staging still exists after successful recovery: %v", err)
	}
}

func TestFilesystemStoreUsesSingleObjectRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
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

	reopened, err := fs.NewFilesystemStore(dir)
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
	store, err := fs.NewFilesystemStore(dir)
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
	store, err := fs.NewFilesystemStore(dir)
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
	store, _ := fs.NewFilesystemStore(dir)
	_ = store.CreateBucket(ctx, "bucket")
	_, _ = store.PutObject(ctx, "bucket", "k", strings.NewReader("x"), storage.PutOptions{})

	if err := store.DeleteBucket(ctx, "bucket"); err != storage.ErrBucketNotEmpty {
		t.Fatalf("delete bucket = %v, want ErrBucketNotEmpty", err)
	}
}
