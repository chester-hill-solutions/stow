package storage_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
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
		{
			// The workspace backend is a third implementation of the same
			// contract, so it runs the same suite as the other two. The
			// guarantees that make it more than a store — adoption, the
			// key-to-path encoding, the manifest — are covered in its own
			// package, not here.
			name: "workspace",
			new: func(t *testing.T) storage.Store {
				t.Helper()
				store, err := workspace.New(workspace.Options{
					Root:   t.TempDir(),
					Bucket: "contract",
				})
				if err != nil {
					t.Fatalf("new workspace store: %v", err)
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

// requireMultipart pins the optional half. MultipartStore is optional in the
// contract, but every backend this repository ships implements it, so the suite
// asserts that rather than assuming it.
func requireMultipart(t *testing.T, store storage.Store) storage.MultipartStore {
	t.Helper()
	multi, ok := store.(storage.MultipartStore)
	if !ok {
		t.Fatalf("%T does not implement storage.MultipartStore", store)
	}
	return multi
}

// A batch delete returns the keys it deleted.
//
// DeleteObjects returns a []string whose meaning is not visible in its
// signature, and the S3 handler emits it as <Deleted>, so a backend that returns
// the survivors produces a response naming objects still on disk and omitting
// the ones that are gone. A key that was not there is neither an error nor a
// deletion, which is S3's behaviour and why the two lists are not the same set
// in a different order.
func TestStoreBatchDeleteReturnsTheKeysItDeleted(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "batch"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		for _, key := range []string{"kept/one", "kept/two"} {
			if _, err := store.PutObject(ctx, "batch", key, strings.NewReader("body"), storage.PutOptions{}); err != nil {
				t.Fatalf("put %s: %v", key, err)
			}
		}

		deleted, err := store.DeleteObjects(ctx, "batch", []string{"kept/one", "absent", "kept/two"})
		if err != nil {
			t.Fatalf("delete objects: %v", err)
		}
		want := []string{"kept/one", "kept/two"}
		if len(deleted) != len(want) {
			t.Fatalf("deleted = %v, want %v", deleted, want)
		}
		for i, key := range want {
			if deleted[i] != key {
				t.Fatalf("deleted = %v, want %v in request order", deleted, want)
			}
		}

		// A key reported as deleted is gone, so the list cannot be satisfied by
		// reporting the complement.
		for _, key := range want {
			if _, err := store.HeadObject(ctx, "batch", key); !errors.Is(err, storage.ErrObjectNotFound) {
				t.Fatalf("head %s after reported deletion = %v, want ErrObjectNotFound", key, err)
			}
		}
		if _, err := store.HeadObject(ctx, "batch", "absent"); !errors.Is(err, storage.ErrObjectNotFound) {
			t.Fatalf("head absent = %v, want ErrObjectNotFound", err)
		}
	})
}

// A batch delete that fails partway keeps the keys it already deleted, so a
// caller retrying the remainder can skip what already succeeded. Without that
// list a recoverable partial failure becomes a second round of deletions
// against keys that are gone.
func TestStoreBatchDeleteReportsProgressBeforeAFailure(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "batch"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		if _, err := store.PutObject(ctx, "batch", "doomed", strings.NewReader("body"), storage.PutOptions{}); err != nil {
			t.Fatalf("put: %v", err)
		}

		// A key past the documented 1024-byte limit fails validation after the
		// first key has already been removed, which is the partial state.
		tooLong := strings.Repeat("k", 1025)
		deleted, err := store.DeleteObjects(ctx, "batch", []string{"doomed", tooLong})
		if !errors.Is(err, storage.ErrInvalidKey) {
			t.Fatalf("delete error = %v, want ErrInvalidKey", err)
		}
		if len(deleted) != 1 || deleted[0] != "doomed" {
			t.Fatalf("deleted = %v, want [doomed]; a caller retrying the remainder cannot skip what already succeeded without this", deleted)
		}
	})
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
		multi := requireMultipart(t, store)
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		got, err := multi.GetMultipartUpload(ctx, upload.UploadID)
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
		multi := requireMultipart(t, store)
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		if err := store.DeleteBucket(ctx, "uploads"); !errors.Is(err, storage.ErrBucketNotEmpty) {
			t.Fatalf("delete bucket error = %v, want ErrBucketNotEmpty", err)
		}
		if err := multi.ValidateMultipartUpload(ctx, upload.UploadID, upload.Bucket, upload.Key); err != nil {
			t.Fatalf("validate upload after rejected deletion: %v", err)
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
	multi := requireMultipart(t, store)
	if err := store.CreateBucket(ctx, "uploads"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	for _, partNumber := range []int{1, 2, 3} {
		if _, err := multi.UploadPart(ctx, upload.UploadID, partNumber, strings.NewReader("part")); err != nil {
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

// A caller-supplied checksum is an integrity claim about the body. Verifying it
// is part of the object model, not an optional extra, so it belongs in the
// contract rather than in one backend's own tests.
//
// This case did not exist, which is why the workspace backend verified
// checksums while memory and filesystem stored the algorithm and value verbatim
// and accepted a corrupt body. The gap was in the suite, not in its absence.
func TestStoreRejectsABodyThatContradictsItsChecksum(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "contract"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}

		body := []byte("the quick brown fox")
		// Deliberately lower case. A caller may write the algorithm either way,
		// so the contract is that the stored form is the normalized one; a
		// backend that echoes the caller's casing makes the same request return
		// different metadata depending on which store is underneath.
		const requested = "crc32"
		const algorithm = "CRC32"
		correct, err := storage.ComputeChecksum(requested, body)
		if err != nil {
			t.Fatalf("compute checksum: %v", err)
		}

		// The happy path: a correct checksum is accepted and recorded.
		meta, err := store.PutObject(ctx, "contract", "good", bytes.NewReader(body), storage.PutOptions{
			ChecksumAlgorithm: requested,
			ChecksumValue:     correct,
		})
		if err != nil {
			t.Fatalf("put with a correct checksum: %v", err)
		}
		if meta.ChecksumAlgorithm != algorithm {
			t.Errorf("ChecksumAlgorithm = %q, want the normalized %q", meta.ChecksumAlgorithm, algorithm)
		}
		if meta.ChecksumValue != correct {
			t.Errorf("ChecksumValue = %q, want %q", meta.ChecksumValue, correct)
		}

		// The case that matters: a body that contradicts the claim is refused,
		// and nothing is left behind under that key.
		_, err = store.PutObject(ctx, "contract", "bad", bytes.NewReader(body), storage.PutOptions{
			ChecksumAlgorithm: requested,
			ChecksumValue:     "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		})
		if !errors.Is(err, storage.ErrChecksumMismatch) {
			t.Fatalf("put with a wrong checksum: err = %v, want ErrChecksumMismatch", err)
		}
		if _, err := store.HeadObject(ctx, "contract", "bad"); !errors.Is(err, storage.ErrObjectNotFound) {
			t.Errorf("a rejected put left an object behind: err = %v, want ErrObjectNotFound", err)
		}
	})
}
