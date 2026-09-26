package runtime

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestMultipartUploadReservesOneObjectSlotPerTarget(t *testing.T) {
	ctx := context.Background()
	// Multipart is a native compatibility capability, so it needs a bound
	// store rather than the public memory-only profile.
	instance, err := OpenWithStore(Options{Backend: BackendMemory, MaxBytes: 64, MaxObjects: 1}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open with store: %v", err)
	}
	defer instance.Close()
	if err := instance.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// The first upload for a missing object spends the only object slot.
	first, err := instance.CreateMultipartUpload(ctx, "bucket", "key")
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	// A second upload for the same target reuses that slot instead of
	// demanding another one.
	if _, err := instance.CreateMultipartUpload(ctx, "bucket", "key"); err != nil {
		t.Fatalf("second upload for the same key: %v", err)
	}
	// A different target has no slot left.
	if _, err := instance.CreateMultipartUpload(ctx, "bucket", "other"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("upload for a new key error = %v, want quota exceeded", err)
	}
	if usage := instance.Usage(); usage.Objects != 0 || usage.Bytes != 0 {
		t.Fatalf("in-flight uploads must not count as usage: %+v", usage)
	}

	part, err := instance.UploadPart(ctx, first.UploadID, 1, bytes.NewBufferString("value"))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if _, err := instance.CompleteMultipartUpload(ctx, first.UploadID, []storage.PartInfo{*part}); err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	if usage := instance.Usage(); usage.Objects != 1 || usage.Bytes != 5 {
		t.Fatalf("usage after completion = %+v, want one five-byte object", usage)
	}
}

func TestOpenNormalizesOptions(t *testing.T) {
	instance, err := Open(Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer instance.Close()
	if instance.options.MaxBytes != DefaultMaxBytes || instance.options.MaxObjects != DefaultMaxObjects {
		t.Fatalf("defaults = %+v", instance.options)
	}
	capabilities := instance.Capabilities()
	if capabilities.Backend != BackendMemory || capabilities.Persistent || capabilities.Upstream {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if instance.Usage() != (Usage{}) {
		t.Fatalf("initial usage = %+v", instance.Usage())
	}
	if _, err := Open(Options{Backend: Backend("filesystem")}); !errors.Is(err, ErrUnsupportedBackend) {
		t.Fatalf("unsupported backend error = %v", err)
	}
	if _, err := Open(Options{MaxBytes: -1}); err == nil {
		t.Fatal("expected negative quota error")
	}
}

func newTestInstance(t *testing.T) *Instance {
	t.Helper()
	instance, err := Open(Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

func TestInstanceBucketLifecycle(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t)
	if err := instance.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	buckets, err := instance.ListBuckets(ctx)
	if err != nil || len(buckets) != 1 || buckets[0].Name != "assets" {
		t.Fatalf("buckets = %+v, err = %v", buckets, err)
	}
	if err := instance.DeleteBucket(ctx, "assets"); err != nil {
		t.Fatalf("delete empty bucket: %v", err)
	}
}

func TestInstanceObjectReadLifecycle(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t)
	if err := instance.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	put, err := instance.PutObject(ctx, "assets", "one", []byte("one"), PutOptions{ContentType: "text/plain", Metadata: map[string]string{"x": "y"}})
	if err != nil || put.Size != 3 || put.ETag == "" {
		t.Fatalf("put = %+v, err = %v", put, err)
	}
	got, err := instance.GetObject(ctx, "assets", "one")
	if err != nil || !bytes.Equal(got.Data, []byte("one")) || got.Metadata["x"] != "y" {
		t.Fatalf("get = %+v, err = %v", got, err)
	}
	head, err := instance.HeadObject(ctx, "assets", "one")
	if err != nil || len(head.Data) != 0 || head.Size != 3 {
		t.Fatalf("head = %+v, err = %v", head, err)
	}
	objects, err := instance.ListObjects(ctx, "assets", ListOptions{Prefix: "o", Limit: 1})
	if err != nil || len(objects.Objects) != 1 || objects.Objects[0].Key != "one" {
		t.Fatalf("list = %+v, err = %v", objects, err)
	}
	if err := instance.DeleteObject(ctx, "assets", "one"); err != nil {
		t.Fatalf("delete source: %v", err)
	}
}

func TestInstanceCopyLifecycle(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t)
	if err := instance.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := instance.PutObject(ctx, "assets", "one", []byte("one"), PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	copyObject, err := instance.CopyObject(ctx, "assets", "one", "assets", "two")
	if err != nil || copyObject.Key != "two" {
		t.Fatalf("copy = %+v, err = %v", copyObject, err)
	}
	if usage := instance.Usage(); usage.Objects != 2 || usage.Bytes != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	if err := instance.DeleteObject(ctx, "assets", "two"); err != nil {
		t.Fatalf("delete copy: %v", err)
	}
	if err := instance.DeleteObject(ctx, "assets", "one"); err != nil {
		t.Fatalf("delete source: %v", err)
	}
}

func TestInstanceQuotaAndErrorPaths(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(Options{MaxBytes: 4, MaxObjects: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer instance.Close()
	if err := instance.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := instance.PutObject(ctx, "assets", "one", []byte("1234"), PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := instance.PutObject(ctx, "assets", "two", []byte("1"), PutOptions{}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("object quota error = %v", err)
	}
	if _, err := instance.PutObject(ctx, "assets", "one", []byte("12345"), PutOptions{}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("byte quota error = %v", err)
	}
	if _, err := instance.GetObject(ctx, "missing", "key"); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Fatalf("missing bucket get error = %v", err)
	}
	if _, err := instance.HeadObject(ctx, "assets", "missing"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("missing head error = %v", err)
	}
	if _, err := instance.ListObjects(ctx, "missing", ListOptions{}); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Fatalf("missing list error = %v", err)
	}
	if err := instance.DeleteObject(ctx, "assets", "missing"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("missing delete error = %v", err)
	}
	if _, err := instance.CopyObject(ctx, "assets", "missing", "assets", "copy"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("missing copy error = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := instance.PutObject(cancelled, "assets", "cancelled", nil, PutOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled put error = %v", err)
	}
}

func TestInstanceResetAndClosedErrors(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := instance.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := instance.PutObject(ctx, "bucket", "key", []byte("value"), PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := instance.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if instance.Usage() != (Usage{}) {
		t.Fatalf("usage after reset = %+v", instance.Usage())
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := instance.CreateBucket(ctx, "bucket"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed create error = %v", err)
	}
	if _, err := instance.ListBuckets(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed list error = %v", err)
	}
	if err := instance.Reset(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed reset error = %v", err)
	}
}

func TestRuntimeHelpers(t *testing.T) {
	instance, err := Open(Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer instance.Close()
	if err := instance.checkContext(nil); err != nil {
		t.Fatalf("nil context error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := instance.checkContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
	if err := instance.checkOpen(); err != nil {
		t.Fatalf("open check error = %v", err)
	}
	if object := objectFromMeta(nil, []byte("data")); !bytes.Equal(object.Data, []byte("data")) {
		t.Fatalf("nil metadata object = %+v", object)
	}
	if metadata := storage.CloneMetadata(map[string]string{"a": "b"}); metadata["a"] != "b" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata := storage.CloneMetadata(nil); metadata != nil {
		t.Fatalf("nil metadata = %+v", metadata)
	}
}
