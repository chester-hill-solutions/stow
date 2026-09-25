package runtime

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

// recordingStore captures what the runtime hands to the underlying store.
type recordingStore struct {
	storage.Store
	seen io.Reader
}

func (s *recordingStore) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	s.seen = body
	return s.Store.PutObject(ctx, bucket, key, body, opts)
}

// TestRuntimeNeverExposesCallerBytesToTheStore pins the safety property that
// lets the runtime skip its defensive copy of a caller's buffer.
//
// The runtime used to copy the body before calling the store, which was
// redundant because every store copies too. Removing the copy is only safe while
// the runtime cannot hand the store something that exposes those bytes: if it
// ever passed a reader implementing storage.ByteReader, a store that adopted
// those bytes would retain the caller's buffer, and a caller reusing that buffer
// would silently corrupt a stored object.
//
// The guarantee is currently incidental, resting on bytes.Reader not having a
// Bytes method. This test makes it explicit, so a later optimisation that
// exposes the bytes fails here instead of corrupting data in production.
func TestRuntimeNeverExposesCallerBytesToTheStore(t *testing.T) {
	recorder := &recordingStore{Store: storage.NewMemoryStore()}
	instance, err := OpenWithStore(Options{Backend: BackendMemory}, recorder, nil)
	if err != nil {
		t.Fatalf("open instance: %v", err)
	}
	defer func() { _ = instance.Close() }()

	ctx := context.Background()
	if err := instance.CreateBucket(ctx, "guard"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	caller := []byte("payload")
	if _, err := instance.PutObject(ctx, "guard", "key", caller, PutOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if recorder.seen == nil {
		t.Fatal("the store was never called")
	}
	if _, ok := recorder.seen.(storage.ByteReader); ok {
		t.Fatal("the runtime handed the store a reader that exposes the caller's bytes; " +
			"a store that adopted them would alias a buffer the caller may reuse")
	}
	if _, ok := recorder.seen.(*bytes.Reader); !ok {
		t.Logf("the store received %T, which cannot expose bytes; the guard still holds", recorder.seen)
	}
}
