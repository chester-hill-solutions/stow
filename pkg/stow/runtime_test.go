package stow_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/chester-hill-solutions/stow/pkg/stow"
)

func newPublicRuntime(t *testing.T) (context.Context, *stow.Runtime) {
	t.Helper()
	runtime, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return context.Background(), runtime
}

func TestRuntimeDirectObjectLifecycle(t *testing.T) {
	ctx, runtime := newPublicRuntime(t)
	if err := runtime.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	put, err := runtime.PutObject(ctx, "assets", "images/logo.bin", []byte("logo"), stow.PutOptions{
		ContentType: "image/png",
		Metadata:    map[string]string{"owner": "test"},
	})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if put.Size != 4 || put.ETag == "" || put.ContentType != "image/png" {
		t.Fatalf("put metadata = %+v", put)
	}
	got, err := runtime.GetObject(ctx, "assets", "images/logo.bin")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if !bytes.Equal(got.Data, []byte("logo")) || got.Metadata["owner"] != "test" {
		t.Fatalf("object = %+v", got)
	}
	head, err := runtime.HeadObject(ctx, "assets", "images/logo.bin")
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	if len(head.Data) != 0 || head.Size != 4 {
		t.Fatalf("head object = %+v", head)
	}
	objects, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{})
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(objects.Objects) != 1 || objects.Objects[0].Key != "images/logo.bin" {
		t.Fatalf("objects = %+v", objects)
	}
}

func TestRuntimeObjectDeletion(t *testing.T) {
	ctx, runtime := newPublicRuntime(t)
	if err := runtime.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "assets", "images/logo.bin", []byte("logo"), stow.PutOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if err := runtime.DeleteObject(ctx, "assets", "images/logo.bin"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	_, err := runtime.GetObject(ctx, "assets", "images/logo.bin")
	if !errors.Is(err, stow.ErrObjectNotFound) {
		t.Fatalf("get after delete error = %v, want ErrObjectNotFound", err)
	}
}

func TestRuntimeEnforcesQuotas(t *testing.T) {
	ctx := context.Background()
	runtime, err := stow.Open(stow.Options{MaxBytes: 5, MaxObjects: 1})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "assets", "one", []byte("123"), stow.PutOptions{}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "assets", "two", []byte("456"), stow.PutOptions{}); !errors.Is(err, stow.ErrQuotaExceeded) {
		t.Fatalf("second put error = %v, want ErrQuotaExceeded", err)
	}
	usage := runtime.Usage()
	if usage.Objects != 1 || usage.Bytes != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestRuntimeInstancesAreIsolatedAndResettable(t *testing.T) {
	ctx := context.Background()
	first, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open first runtime: %v", err)
	}
	defer first.Close()
	second, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open second runtime: %v", err)
	}
	defer second.Close()

	if err := first.CreateBucket(ctx, "private"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := first.PutObject(ctx, "private", "secret", []byte("value"), stow.PutOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	objects, err := second.ListObjects(ctx, "private", stow.ListOptions{})
	if !errors.Is(err, stow.ErrBucketNotFound) {
		t.Fatalf("isolated list error = %v, want ErrBucketNotFound", err)
	}
	if len(objects.Objects) != 0 {
		t.Fatalf("isolated objects = %+v", objects)
	}

	if err := first.Reset(ctx); err != nil {
		t.Fatalf("reset runtime: %v", err)
	}
	buckets, err := first.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("list buckets after reset: %v", err)
	}
	if len(buckets) != 0 {
		t.Fatalf("buckets after reset = %+v", buckets)
	}
	if first.Usage() != (stow.Usage{}) {
		t.Fatalf("usage after reset = %+v", first.Usage())
	}
}

func TestRuntimeListObjectsReturnsCursorPage(t *testing.T) {
	ctx := context.Background()
	runtime, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	defer runtime.Close()
	if err := runtime.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, err := runtime.PutObject(ctx, "assets", key, []byte(key), stow.PutOptions{}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	assertRuntimeCursorPages(t, runtime, ctx)
	if _, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: -1}); !errors.Is(err, stow.ErrInvalidListLimit) {
		t.Fatalf("negative limit error = %v, want ErrInvalidListLimit", err)
	}
}

func assertRuntimeCursorPages(t *testing.T, runtime *stow.Runtime, ctx context.Context) {
	t.Helper()
	first, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: 1})
	if err != nil || len(first.Objects) != 1 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	second, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Objects) != 1 || second.Objects[0].Key != "b" || !second.Truncated || second.NextCursor == "" {
		t.Fatalf("second page = %+v, err = %v", second, err)
	}
	third, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: 1, Cursor: second.NextCursor})
	if err != nil || len(third.Objects) != 1 || third.Objects[0].Key != "c" || third.Truncated || third.NextCursor != "" {
		t.Fatalf("third page = %+v, err = %v", third, err)
	}
}

func TestOpenRejectsUnsupportedBackend(t *testing.T) {
	_, err := stow.Open(stow.Options{Backend: stow.Backend("filesystem")})
	if !errors.Is(err, stow.ErrUnsupportedBackend) {
		t.Fatalf("open error = %v, want ErrUnsupportedBackend", err)
	}
}
