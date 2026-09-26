package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestStoreAdapterCoreContract(t *testing.T) {
	ctx := context.Background()
	instance, err := OpenWithStore(Options{Backend: BackendFilesystem, MaxBytes: 32, MaxObjects: 3}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open with store: %v", err)
	}
	adapter, err := NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	assertAdapterObjects(t, ctx, adapter)
	assertAdapterGet(t, ctx, adapter)
	assertAdapterCopyAndDelete(t, ctx, adapter, instance)
}

func assertAdapterObjects(t *testing.T, ctx context.Context, adapter *StoreAdapter) {
	t.Helper()
	if err := adapter.CreateBucket(ctx, "adapter"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if info, err := adapter.HeadBucket(ctx, "adapter"); err != nil || info.Name != "adapter" {
		t.Fatalf("head bucket = %+v, err = %v", info, err)
	}
	if _, err := adapter.PutObject(ctx, "adapter", "dir/one", bytes.NewBufferString("one"), storage.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"key": "value"}}); err != nil {
		t.Fatalf("put one: %v", err)
	}
	if _, err := adapter.PutObject(ctx, "adapter", "dir/two", bytes.NewBufferString("two"), storage.PutOptions{}); err != nil {
		t.Fatalf("put two: %v", err)
	}
	assertAdapterPages(t, ctx, adapter)
}

func assertAdapterPages(t *testing.T, ctx context.Context, adapter *StoreAdapter) {
	t.Helper()
	page, err := adapter.ListObjectsV2(ctx, "adapter", storage.ListOptions{Prefix: "dir/", Delimiter: "/", MaxKeys: 1})
	if err != nil || len(page.Objects) != 1 || page.KeyCount != 1 || !page.IsTruncated || page.NextContinuationToken == "" {
		t.Fatalf("first page = %+v, err = %v", page, err)
	}
	page, err = adapter.ListObjectsV2(ctx, "adapter", storage.ListOptions{Prefix: "dir/", Delimiter: "/", ContinuationToken: page.NextContinuationToken, MaxKeys: 1})
	if err != nil || len(page.Objects) != 1 || page.ContinuationToken == "" || page.IsTruncated || page.Objects[0].Key != "dir/two" {
		t.Fatalf("second page = %+v, err = %v", page, err)
	}
}

func assertAdapterGet(t *testing.T, ctx context.Context, adapter *StoreAdapter) {
	t.Helper()
	reader, meta, err := adapter.GetObject(ctx, "adapter", "dir/one")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, []byte("one")) || meta.ContentType != "text/plain" || meta.Metadata["key"] != "value" {
		t.Fatalf("get data = %q, meta = %+v, readErr = %v, closeErr = %v", data, meta, readErr, closeErr)
	}
}

func assertAdapterCopyAndDelete(t *testing.T, ctx context.Context, adapter *StoreAdapter, instance *Instance) {
	t.Helper()
	if _, err := adapter.CopyObject(ctx, "adapter", "dir/one", "adapter", "copy"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	// Both keys are confirmed: "missing" was not there, which S3 reports as
	// deleted. Quota is only released for the one that existed.
	confirmed, err := adapter.DeleteObjects(ctx, "adapter", []string{"copy", "missing"})
	if err != nil || !equalStrings(confirmed, []string{"copy", "missing"}) {
		t.Fatalf("delete objects = %v, err = %v", confirmed, err)
	}
	if usage := instance.Usage(); usage.Bytes != 6 || usage.Objects != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestStoreAdapterPreservesChecksumsAndConditionalWrites(t *testing.T) {
	ctx := context.Background()
	instance, err := OpenWithStore(Options{
		Backend:    BackendMemory,
		MaxBytes:   32,
		MaxObjects: 2,
	}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open with store: %v", err)
	}
	adapter, err := NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.CreateBucket(ctx, "adapter"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	checksum, err := storage.ComputeChecksum("CRC32", []byte("one"))
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	meta, err := adapter.PutObject(ctx, "adapter", "one", bytes.NewBufferString("one"), storage.PutOptions{
		ChecksumAlgorithm: "CRC32",
		ChecksumValue:     checksum,
		IfNoneMatch:       "*",
	})
	if err != nil {
		t.Fatalf("conditional put: %v", err)
	}
	if meta.ChecksumAlgorithm != "CRC32" || meta.ChecksumValue != checksum {
		t.Fatalf("put metadata = %+v", meta)
	}
	_, err = adapter.PutObject(ctx, "adapter", "one", bytes.NewBufferString("two"), storage.PutOptions{IfNoneMatch: "*"})
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("second conditional put error = %v", err)
	}
	_, err = adapter.PutObject(ctx, "adapter", "one", bytes.NewBufferString("three"), storage.PutOptions{IfMatch: meta.ETag})
	if err != nil {
		t.Fatalf("matching conditional put: %v", err)
	}
}

func TestStoreAdapterMultipartUsesRuntimeQuotas(t *testing.T) {
	ctx := context.Background()
	instance, err := OpenWithStore(Options{Backend: BackendMemory, MaxBytes: 8, MaxObjects: 1}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open with store: %v", err)
	}
	adapter, err := NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	assertMultipartQuotaLifecycle(t, ctx, adapter, instance)
}

func assertMultipartQuotaLifecycle(t *testing.T, ctx context.Context, adapter *StoreAdapter, instance *Instance) {
	t.Helper()
	requireNoError(t, adapter.CreateBucket(ctx, "adapter"), "create bucket")
	upload, err := adapter.CreateMultipartUpload(ctx, "adapter", "object")
	requireNoError(t, err, "create multipart")
	_, err = adapter.UploadPart(ctx, upload.UploadID, 1, bytes.NewBufferString("1234"))
	requireNoError(t, err, "first part")
	page, err := adapter.ListPartsPage(ctx, upload.UploadID, storage.ListPartsOptions{MaxParts: 1})
	requirePartPage(t, page, err, 4)
	_, err = adapter.UploadPart(ctx, upload.UploadID, 1, bytes.NewBufferString("12345"))
	requireNoError(t, err, "replace first part")
	_, err = adapter.UploadPart(ctx, upload.UploadID, 2, bytes.NewBufferString("1"))
	requireNoError(t, err, "second part")
	_, err = adapter.UploadPart(ctx, upload.UploadID, 3, bytes.NewBufferString("xxx"))
	requireQuotaError(t, err, "multipart byte quota")
	parts, err := adapter.ListParts(ctx, upload.UploadID)
	requireParts(t, parts, err)
	_, err = adapter.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{{PartNumber: 1, ETag: parts[0].ETag}, {PartNumber: 2, ETag: parts[1].ETag}})
	requireNoError(t, err, "complete multipart")
	if usage := instance.Usage(); usage.Bytes != 6 || instance.reservedBytes != 0 || usage.Objects != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func requireNoError(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func requireQuotaError(t *testing.T, err error, label string) {
	t.Helper()
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("%s: %v", label, err)
	}
}

func requirePartPage(t *testing.T, page *storage.ListPartsResult, err error, size int64) {
	t.Helper()
	if err != nil || len(page.Parts) != 1 || page.Parts[0].Size != size {
		t.Fatalf("parts page = %+v, err = %v", page, err)
	}
}

func requireParts(t *testing.T, parts []storage.PartInfo, err error) {
	t.Helper()
	if err != nil || len(parts) != 2 || parts[0].Size != 5 {
		t.Fatalf("parts = %+v, err = %v", parts, err)
	}
}

func TestOpenWithStoreRejectsInvalidOptionsAndInitializesUsage(t *testing.T) {
	ctx := context.Background()
	backing := storage.NewMemoryStore()
	if err := backing.CreateBucket(ctx, "persisted"); err != nil {
		t.Fatalf("create persisted bucket: %v", err)
	}
	if _, err := backing.PutObject(ctx, "persisted", "key", bytes.NewBufferString("persist"), storage.PutOptions{}); err != nil {
		t.Fatalf("put persisted object: %v", err)
	}
	instance, err := OpenWithStore(Options{Backend: BackendFilesystem, MaxBytes: 8, MaxObjects: 2}, backing, nil)
	if err != nil {
		t.Fatalf("open existing store: %v", err)
	}
	defer instance.Close()
	if usage := instance.Usage(); usage.Bytes != int64(len("persist")) || usage.Objects != 1 {
		t.Fatalf("initialized usage = %+v", usage)
	}
	if capabilities := instance.Capabilities(); !capabilities.Persistent || !capabilities.Multipart {
		t.Fatalf("bound capabilities = %+v", capabilities)
	}
	if _, err := OpenWithStore(Options{Backend: Backend("unknown")}, storage.NewMemoryStore(), nil); !errors.Is(err, ErrUnsupportedBackend) {
		t.Fatalf("invalid bound backend error = %v", err)
	}
	if _, err := OpenWithStore(Options{Backend: BackendFilesystem, MaxBytes: 1}, backing, nil); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("existing over-quota error = %v", err)
	}
}

func TestOpenWithStoreLifecycleAndOwnership(t *testing.T) {
	backing := &closeTrackingStore{Store: storage.NewMemoryStore()}
	instance, err := OpenWithStore(Options{Backend: BackendFilesystem}, backing, nil)
	if err != nil {
		t.Fatalf("open with store: %v", err)
	}
	adapter, err := NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if err := instance.Reset(context.Background()); !errors.Is(err, ErrExternalResetUnsupported) {
		t.Fatalf("external reset error = %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close adapter: %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close adapter again: %v", err)
	}
	if backing.closes != 1 {
		t.Fatalf("backing close count = %d, want 1", backing.closes)
	}
	if _, err := adapter.ListBuckets(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed list error = %v", err)
	}
}

func TestOpenRemainsMemoryOnly(t *testing.T) {
	instance, err := Open(Options{Backend: BackendFilesystem})
	if !errors.Is(err, ErrUnsupportedBackend) {
		t.Fatalf("filesystem public open error = %v", err)
	}
	instance, err = Open(Options{})
	if err != nil {
		t.Fatalf("memory open: %v", err)
	}
	defer instance.Close()
	if capabilities := instance.Capabilities(); capabilities.Backend != BackendMemory || capabilities.Persistent {
		t.Fatalf("public capabilities = %+v", capabilities)
	}
}

// Multipart is read off the store, so the two constructors cannot disagree about
// one backend, and a store without the optional half is refused at the operation
// rather than at open time.
func TestMultipartCapabilityIsReadFromTheStoreNotTheConstructor(t *testing.T) {
	memory, err := Open(Options{})
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	defer memory.Close()

	bound, err := OpenWithStore(Options{Backend: BackendMemory}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open bound memory: %v", err)
	}
	defer bound.Close()

	if memory.Capabilities().Multipart != bound.Capabilities().Multipart {
		t.Fatalf("open reports multipart=%v, openWithStore reports %v for the same backend",
			memory.Capabilities().Multipart, bound.Capabilities().Multipart)
	}

	// A store with no multipart half: the object model is embedded, so the
	// optional interface is genuinely absent.
	ctx := context.Background()
	plain := &objectModelOnlyStore{Store: storage.NewMemoryStore()}
	if err := plain.CreateBucket(ctx, "already-here"); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	instance, err := OpenWithStore(Options{Backend: BackendMemory}, plain, nil)
	if err != nil {
		t.Fatalf("open a store that cannot serve multipart: %v", err)
	}
	defer instance.Close()
	if instance.Capabilities().Multipart {
		t.Fatal("capability claims multipart for a store that does not implement it")
	}
	if _, err := instance.CreateMultipartUpload(ctx, "already-here", "k"); !errors.Is(err, ErrMultipartUnsupported) {
		t.Fatalf("create upload = %v, want ErrMultipartUnsupported", err)
	}
}

// objectModelOnlyStore is a store with the object model and no multipart half,
// which is what a caller-supplied store that declined the optional interface
// looks like from in here.
type objectModelOnlyStore struct{ storage.Store }

// The persistence predicate covers the workspace backend, which the CLI cannot
// select. A claim computed from a flag string would have said otherwise, so the
// third backend is asserted here where the predicate lives.
func TestPersistenceIsReportedForEveryKnownBackend(t *testing.T) {
	for backend, want := range map[Backend]bool{
		BackendMemory:     false,
		BackendFilesystem: true,
		BackendWorkspace:  true,
	} {
		instance, err := OpenWithStore(Options{Backend: backend}, storage.NewMemoryStore(), nil)
		if err != nil {
			t.Fatalf("open %q: %v", backend, err)
		}
		if got := instance.Capabilities().Persistent; got != want {
			t.Errorf("%s: persistent = %v, want %v", backend, got, want)
		}
		if err := instance.Close(); err != nil {
			t.Fatalf("close %q: %v", backend, err)
		}
	}
}

type closeTrackingStore struct {
	storage.Store
	closes int
}

func (s *closeTrackingStore) Close() error {
	s.closes++
	return s.Store.Close()
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
