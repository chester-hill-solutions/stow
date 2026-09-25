package runtime_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func newMemoryInstance(t *testing.T) *runtime.Instance {
	t.Helper()
	instance, err := runtime.Open(runtime.Options{Backend: runtime.BackendMemory})
	if err != nil {
		t.Fatalf("open instance: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

// TestPutObjectDoesNotAliasCallerBytes is the contract that lets the runtime
// hand a caller's buffer to the store without copying it first. If a store ever
// retained the caller's slice, a caller reusing its buffer would silently
// corrupt a stored object, so this is asserted rather than assumed.
func TestPutObjectDoesNotAliasCallerBytes(t *testing.T) {
	instance := newMemoryInstance(t)
	ctx := context.Background()
	if err := instance.CreateBucket(ctx, "aliasing"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	caller := []byte("original bytes")
	if _, err := instance.PutObject(ctx, "aliasing", "key", caller, runtime.PutOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}

	// The caller reuses its buffer, which is exactly what would corrupt a stored
	// object if the store had retained the slice.
	for i := range caller {
		caller[i] = 'x'
	}

	got, err := instance.GetObject(ctx, "aliasing", "key")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if got.Data == nil {
		t.Fatal("stored data is nil")
	}
	if string(got.Data) != "original bytes" {
		t.Fatalf("stored data = %q, want it unchanged after the caller overwrote its buffer", got.Data)
	}
}

// TestStoreAdapterAdoptsAByteReaderWithoutCopying proves the adapter takes the
// bytes a reader already holds instead of reading it into a second buffer.
func TestStoreAdapterAdoptsAByteReaderWithoutCopying(t *testing.T) {
	instance := newMemoryInstance(t)
	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new store adapter: %v", err)
	}
	defer func() { _ = adapter.Close() }()
	ctx := context.Background()
	if err := adapter.CreateBucket(ctx, "adopt"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	held := []byte("payload")
	reader := &holderReader{data: held}
	meta, err := adapter.PutObject(ctx, "adopt", "key", reader, storage.PutOptions{})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if meta == nil {
		t.Fatal("put returned no metadata")
	}
	if reader.reads != 0 {
		t.Fatalf("the adapter read the body %d times; it should have adopted the bytes", reader.reads)
	}
}

func TestStoreAdapterStillReadsAPlainReader(t *testing.T) {
	instance := newMemoryInstance(t)
	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new store adapter: %v", err)
	}
	defer func() { _ = adapter.Close() }()
	ctx := context.Background()
	if err := adapter.CreateBucket(ctx, "plain"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// A reader that does not expose its bytes must still work, because the store
	// interface is io.Reader and most callers are exactly this shape.
	if _, err := adapter.PutObject(ctx, "plain", "key", strings.NewReader("payload"), storage.PutOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	reader, _, err := adapter.GetObject(ctx, "plain", "key")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want %q", body, "payload")
	}
}

func TestStoreAdapterAcceptsANilBody(t *testing.T) {
	instance := newMemoryInstance(t)
	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("new store adapter: %v", err)
	}
	defer func() { _ = adapter.Close() }()
	ctx := context.Background()
	if err := adapter.CreateBucket(ctx, "empty"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := adapter.PutObject(ctx, "empty", "key", nil, storage.PutOptions{}); err != nil {
		t.Fatalf("put with a nil body: %v", err)
	}
}

// holderReader is a storage.ByteReader that counts how often it was read.
type holderReader struct {
	data  []byte
	reads int
}

func (h *holderReader) Read(p []byte) (int, error) {
	h.reads++
	return bytes.NewReader(h.data).Read(p)
}

func (h *holderReader) Bytes() []byte { return h.data }

var _ storage.ByteReader = (*holderReader)(nil)
