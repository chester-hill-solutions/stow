package stow_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

// The multipart half of a caller-supplied store, kept apart because it is a
// separate optional interface and the tests that matter are about whether the
// environment DESCRIBES it correctly rather than about serving an upload.
//
// A store that implements this is advertised as supporting multipart; one that
// does not is advertised as not supporting it, and the runtime reconciles
// in-flight uploads at open time, so the read half matters as much as the write
// half.

type multipartStore struct {
	*memoryStore
	mu      sync.Mutex
	uploads map[string]stow.MultipartUpload
	parts   map[string][]stow.Part
}

func newMultipartStore() *multipartStore {
	return &multipartStore{
		memoryStore: newMemoryStore(),
		uploads:     map[string]stow.MultipartUpload{},
		parts:       map[string][]stow.Part{},
	}
}

func (m *multipartStore) CreateMultipartUpload(_ context.Context, bucket, key string) (stow.MultipartUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upload := stow.MultipartUpload{
		UploadID:  "upload-1",
		Bucket:    bucket,
		Key:       key,
		Initiated: time.Now().UTC(),
	}
	m.uploads[upload.UploadID] = upload
	m.parts[upload.UploadID] = nil
	return upload, nil
}

func (m *multipartStore) UploadPart(_ context.Context, uploadID string, partNumber int, data []byte) (stow.Part, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	part := stow.Part{PartNumber: partNumber, ETag: "etag-" + uploadID, Size: int64(len(data))}
	m.parts[uploadID] = append(m.parts[uploadID], part)
	return part, nil
}

func (m *multipartStore) CompleteMultipartUpload(_ context.Context, uploadID string, parts []stow.Part) (stow.Object, error) {
	m.mu.Lock()
	upload, ok := m.uploads[uploadID]
	m.mu.Unlock()
	if !ok {
		return stow.Object{}, stow.ErrObjectNotFound
	}
	return stow.Object{
		Bucket: upload.Bucket, Key: upload.Key,
		ETag: "composite", Size: int64(len(parts)),
	}, nil
}

func (m *multipartStore) AbortMultipartUpload(_ context.Context, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, uploadID)
	delete(m.parts, uploadID)
	return nil
}

func (m *multipartStore) GetMultipartUpload(_ context.Context, uploadID string) (stow.MultipartUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upload, ok := m.uploads[uploadID]
	if !ok {
		return stow.MultipartUpload{}, stow.ErrObjectNotFound
	}
	return upload, nil
}

func (m *multipartStore) ListParts(_ context.Context, uploadID string) ([]stow.Part, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.uploads[uploadID]; !ok {
		return nil, stow.ErrObjectNotFound
	}
	return append([]stow.Part(nil), m.parts[uploadID]...), nil
}

func (m *multipartStore) ListMultipartUploads(_ context.Context, bucket string) ([]stow.MultipartUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []stow.MultipartUpload
	for _, upload := range m.uploads {
		if upload.Bucket == bucket {
			out = append(out, upload)
		}
	}
	return out, nil
}

func TestAStoreWithMultipartIsAdvertisedAsSupportingIt(t *testing.T) {
	runtime, err := stow.Open(stow.Options{Store: newMultipartStore()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if !runtime.Capabilities().Multipart {
		t.Error("a store implementing MultipartStore was advertised as not supporting multipart")
	}
}

// A store that does not implement multipart must still serve everything else.
// Turning the optional interface into a requirement would make the smallest
// useful store the largest one.
func TestAStoreWithoutMultipartStillServesEverythingElse(t *testing.T) {
	ctx := context.Background()
	runtime, err := stow.Open(stow.Options{Store: newMemoryStore()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if err := runtime.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "bucket", "k", []byte("x"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := runtime.DeleteBucket(ctx, "bucket"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
}

// Authority and quotas are enforced on top of a supplied store, not bypassed by
// it. A caller that supplies storage has not thereby supplied its own rules.
// A store that serves no multipart must still OPEN when it already holds a
// bucket. This is the case that found a real bug: multipart reconciliation ran
// unconditionally at open time, so the capability gated the operations and not
// the initialization. An empty store hid it, because the failing case needs a
// bucket and a fresh store has none.
