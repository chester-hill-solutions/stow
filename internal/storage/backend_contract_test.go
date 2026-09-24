package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

type storeFactory struct {
	name string
	new  func(*testing.T) storage.Store
}

func backendFactories(t *testing.T) []storeFactory {
	t.Helper()
	return []storeFactory{
		{
			name: "memory",
			new: func(t *testing.T) storage.Store {
				t.Helper()
				return storage.NewMemoryStore()
			},
		},
		{
			name: "filesystem",
			new: func(t *testing.T) storage.Store {
				t.Helper()
				store, err := storage.NewFilesystemStore(t.TempDir())
				if err != nil {
					t.Fatalf("new filesystem store: %v", err)
				}
				return store
			},
		},
	}
}

func withStores(t *testing.T, test func(*testing.T, storage.Store)) {
	t.Helper()
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.new(t)
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close store: %v", err)
				}
			})
			test(t, store)
		})
	}
}

func TestStoreBatchDeleteRequiresBucket(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		deleted, err := store.DeleteObjects(context.Background(), "missing", []string{"key"})
		if !errors.Is(err, storage.ErrBucketNotFound) {
			t.Fatalf("delete error = %v, want ErrBucketNotFound", err)
		}
		if len(deleted) != 0 {
			t.Fatalf("deleted = %v, want none", deleted)
		}
	})
}

func TestStoreBucketDeletionRejectsActiveMultipartUpload(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := store.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		if err := store.DeleteBucket(ctx, "uploads"); !errors.Is(err, storage.ErrBucketNotEmpty) {
			t.Fatalf("delete bucket error = %v, want ErrBucketNotEmpty", err)
		}
		if err := store.ValidateMultipartUpload(ctx, upload.UploadID, upload.Bucket, upload.Key); err != nil {
			t.Fatalf("validate upload after rejected deletion: %v", err)
		}
	})
}

func TestStoreRejectsDuplicateMultipartParts(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := store.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		part, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("part"))
		if err != nil {
			t.Fatalf("upload part: %v", err)
		}
		_, err = store.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part, *part})
		if !errors.Is(err, storage.ErrInvalidPart) {
			t.Fatalf("complete error = %v, want ErrInvalidPart", err)
		}
	})
}
