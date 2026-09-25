package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/storage"
	"github.com/chester-hill-solutions/stow/internal/storage/fs"
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
				store, err := fs.NewFilesystemStore(t.TempDir())
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

func TestStoreMissingBucketErrorsAgreeForObjectOperations(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if _, _, err := store.GetObject(ctx, "missing", "key"); !errors.Is(err, storage.ErrBucketNotFound) {
			t.Fatalf("get error = %v, want ErrBucketNotFound", err)
		}
		if _, err := store.HeadObject(ctx, "missing", "key"); !errors.Is(err, storage.ErrBucketNotFound) {
			t.Fatalf("head error = %v, want ErrBucketNotFound", err)
		}
		if err := store.DeleteObject(ctx, "missing", "key"); !errors.Is(err, storage.ErrBucketNotFound) {
			t.Fatalf("delete error = %v, want ErrBucketNotFound", err)
		}
	})
}

func TestStoreMetadataMapsDoNotAliasStoredState(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "metadata"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}

		input := map[string]string{"owner": "original"}
		putMeta, err := store.PutObject(ctx, "metadata", "object", strings.NewReader("payload"), storage.PutOptions{
			Metadata: input,
		})
		if err != nil {
			t.Fatalf("put object: %v", err)
		}
		input["owner"] = "changed input"
		putMeta.Metadata["owner"] = "changed put result"

		headMeta, err := store.HeadObject(ctx, "metadata", "object")
		if err != nil {
			t.Fatalf("head object: %v", err)
		}
		headMeta.Metadata["owner"] = "changed head result"

		rc, getMeta, err := store.GetObject(ctx, "metadata", "object")
		if err != nil {
			t.Fatalf("get object: %v", err)
		}
		_ = rc.Close()
		getMeta.Metadata["owner"] = "changed get result"

		list, err := store.ListObjectsV2(ctx, "metadata", storage.ListOptions{})
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		if len(list.Objects) != 1 {
			t.Fatalf("objects = %+v, want one", list.Objects)
		}
		list.Objects[0].Metadata["owner"] = "changed list result"

		finalMeta, err := store.HeadObject(ctx, "metadata", "object")
		if err != nil {
			t.Fatalf("head object after metadata mutations: %v", err)
		}
		if got := finalMeta.Metadata["owner"]; got != "original" {
			t.Fatalf("stored metadata = %q, want %q", got, "original")
		}
	})
}

func TestStoreAssignsImmutableVersionForEqualContent(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "versions"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		first, err := store.PutObject(ctx, "versions", "object", strings.NewReader("same bytes"), storage.PutOptions{
			Metadata: map[string]string{"generation": "one"},
		})
		if err != nil {
			t.Fatalf("first put: %v", err)
		}
		second, err := store.PutObject(ctx, "versions", "object", strings.NewReader("same bytes"), storage.PutOptions{
			Metadata: map[string]string{"generation": "two"},
		})
		if err != nil {
			t.Fatalf("second put: %v", err)
		}
		if first.VersionID == "" || second.VersionID == "" {
			t.Fatalf("versions = %q, %q; want non-empty", first.VersionID, second.VersionID)
		}
		if first.VersionID == second.VersionID {
			t.Fatalf("version IDs reused for separate commits: %q", first.VersionID)
		}
	})
}

func TestStoreMultipartLookup(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := store.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		got, err := store.GetMultipartUpload(ctx, upload.UploadID)
		if err != nil {
			t.Fatalf("get upload: %v", err)
		}
		if got.Bucket != upload.Bucket || got.Key != upload.Key {
			t.Fatalf("upload = %+v, want %+v", got, upload)
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

func TestStoreRejectsUnsortedMultipartCompletionParts(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := store.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		first, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("first"))
		if err != nil {
			t.Fatalf("upload first part: %v", err)
		}
		second, err := store.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("second"))
		if err != nil {
			t.Fatalf("upload second part: %v", err)
		}
		parts := []storage.PartInfo{*second, *first}
		if _, err := store.CompleteMultipartUpload(ctx, upload.UploadID, parts); !errors.Is(err, storage.ErrInvalidPart) {
			t.Fatalf("complete error = %v, want ErrInvalidPart", err)
		}
		if parts[0].PartNumber != 2 {
			t.Fatalf("completion parts were reordered: %+v", parts)
		}
	})
}

type pagedPartLister interface {
	ListPartsPage(context.Context, string, storage.ListPartsOptions) (*storage.ListPartsResult, error)
}

func requirePagedPartLister(t *testing.T, store storage.Store) pagedPartLister {
	t.Helper()
	lister, ok := store.(pagedPartLister)
	if !ok {
		t.Fatal("store does not expose paginated ListParts")
	}
	return lister
}

func seedPartUpload(t *testing.T, store storage.Store) string {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "uploads"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	upload, err := store.CreateMultipartUpload(ctx, "uploads", "object.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	for _, partNumber := range []int{1, 2, 3} {
		if _, err := store.UploadPart(ctx, upload.UploadID, partNumber, strings.NewReader("part")); err != nil {
			t.Fatalf("upload part %d: %v", partNumber, err)
		}
	}
	return upload.UploadID
}

type partPageExpectation struct {
	part      int
	marker    int
	next      int
	maxParts  int
	truncated bool
}

func assertPartPage(t *testing.T, page *storage.ListPartsResult, want partPageExpectation) {
	t.Helper()
	if len(page.Parts) != 1 {
		t.Fatalf("parts = %+v, want one", page.Parts)
	}
	if page.Parts[0].PartNumber != want.part {
		t.Fatalf("part number = %d, want %d", page.Parts[0].PartNumber, want.part)
	}
	if page.PartNumberMarker != want.marker {
		t.Fatalf("part marker = %d, want %d", page.PartNumberMarker, want.marker)
	}
	if page.NextPartNumberMarker != want.next {
		t.Fatalf("next part marker = %d, want %d", page.NextPartNumberMarker, want.next)
	}
	if page.MaxParts != want.maxParts {
		t.Fatalf("max parts = %d, want %d", page.MaxParts, want.maxParts)
	}
	if page.IsTruncated != want.truncated {
		t.Fatalf("truncated = %v, want %v", page.IsTruncated, want.truncated)
	}
}

func testListPartsPaginationMarkers(t *testing.T, store storage.Store) {
	ctx := context.Background()
	lister := requirePagedPartLister(t, store)
	uploadID := seedPartUpload(t, store)
	first, err := lister.ListPartsPage(ctx, uploadID, storage.ListPartsOptions{PartNumberMarker: 1, MaxParts: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assertPartPage(t, first, partPageExpectation{part: 2, marker: 1, next: 2, maxParts: 1, truncated: true})

	last, err := lister.ListPartsPage(ctx, uploadID, storage.ListPartsOptions{PartNumberMarker: 2, MaxParts: 1})
	if err != nil {
		t.Fatalf("last page: %v", err)
	}
	assertPartPage(t, last, partPageExpectation{part: 3, marker: 2, maxParts: 1})
}

func TestStoreListPartsPaginationMarkers(t *testing.T) {
	withStores(t, testListPartsPaginationMarkers)
}
