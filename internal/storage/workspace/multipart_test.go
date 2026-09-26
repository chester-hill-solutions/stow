package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// The multipart semantics themselves — ordering, ETags, composite ETags, part
// validation — are covered by the shared contract suite in
// internal/storage/backend_contract_test.go, which runs against this backend
// too. What is tested here is only what is specific to a workspace: where the
// bytes physically go, and what a caller can see while an upload is in flight.

// TestAnUploadIsInvisibleUntilItCompletes is the workspace-specific guarantee.
// A half-finished upload must not appear as an object, because a workspace is a
// directory a person browses and staged parts are not part of their work.
func TestAnUploadIsInvisibleUntilItCompletes(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	upload, err := store.CreateMultipartUpload(ctx, bucket, "output/big.bin")
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	if _, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("first half ")); err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	if _, err := store.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("second half")); err != nil {
		t.Fatalf("UploadPart: %v", err)
	}

	if _, _, err := store.GetObject(ctx, bucket, "output/big.bin"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("an incomplete upload is visible as an object: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "output", "big.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an incomplete upload produced a file at the key's path: %v", err)
	}
	// Staged parts live under the reserved internal directory, so they are
	// stow's bookkeeping rather than the caller's work.
	staged := filepath.Join(root, ".stow", "multipart", upload.UploadID)
	if _, err := os.Lstat(staged); err != nil {
		t.Fatalf("expected staged parts at %s: %v", staged, err)
	}

	parts, err := store.ListParts(ctx, upload.UploadID)
	if err != nil {
		t.Fatalf("ListParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("ListParts returned %d parts, want 2", len(parts))
	}
	if _, err := store.CompleteMultipartUpload(ctx, upload.UploadID, parts); err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}

	if got := get(t, store, "output/big.bin"); got != "first half second half" {
		t.Errorf("assembled object = %q, want the parts in order", got)
	}
	// And the completed object is a real file, like any other.
	if _, err := os.Lstat(filepath.Join(root, "output", "big.bin")); err != nil {
		t.Errorf("the completed object is not a file: %v", err)
	}
	// The staging area is cleaned up, so an abandoned upload leaves nothing.
	if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging survived completion: %v", err)
	}
}

// TestAbortingAnUploadLeavesNothing covers the abandoned case, which is the one
// a workspace has to get right: a caller that gives up must not leave debris in
// a directory a person is looking at.
func TestAbortingAnUploadLeavesNothing(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	upload, err := store.CreateMultipartUpload(ctx, bucket, "abandoned.bin")
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	if _, err := store.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("bytes")); err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	if err := store.AbortMultipartUpload(ctx, upload.UploadID); err != nil {
		t.Fatalf("AbortMultipartUpload: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".stow", "multipart", upload.UploadID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory survived an abort: %v", err)
	}
	if _, _, err := store.GetObject(ctx, bucket, "abandoned.bin"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("an aborted upload produced an object: %v", err)
	}
	// The upload is unknown afterwards, which is what lets the bucket be removed.
	if _, err := store.GetMultipartUpload(ctx, upload.UploadID); !errors.Is(err, storage.ErrUploadNotFound) {
		t.Errorf("GetMultipartUpload after abort = %v, want ErrUploadNotFound", err)
	}
}

// TestBucketCreateHeadAndList covers the create, inspect, and list half of the
// bucket lifecycle: a second bucket is namespaced, is visible, and is idempotent
// to create.
func TestBucketCreateHeadAndList(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	const other = "scratch"

	if err := store.CreateBucket(ctx, other); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := store.CreateBucket(ctx, other); err != nil {
		t.Errorf("CreateBucket is not idempotent: %v", err)
	}
	info, err := store.HeadBucket(ctx, other)
	if err != nil {
		t.Fatalf("HeadBucket: %v", err)
	}
	if info.Name != other {
		t.Errorf("HeadBucket name = %q, want %q", info.Name, other)
	}
	if _, err := store.HeadBucket(ctx, "absent"); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("HeadBucket on a missing bucket = %v, want ErrBucketNotFound", err)
	}

	buckets, err := store.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	names := map[string]bool{}
	for _, listed := range buckets {
		names[listed.Name] = true
	}
	if !names[bucket] || !names[other] {
		t.Errorf("ListBuckets = %v, want both the workspace bucket and %q", names, other)
	}
}

// TestBucketDeletionRefusesWorkInFlight covers the removal half, and the case
// that made it worth writing: a bucket is not empty while a caller is midway
// through writing into it, so it cannot be removed out from under them.
func TestBucketDeletionRefusesWorkInFlight(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	const other = "scratch"

	if err := store.CreateBucket(ctx, other); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	upload, err := store.CreateMultipartUpload(ctx, other, "pending.bin")
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	if err := store.DeleteBucket(ctx, other); !errors.Is(err, storage.ErrBucketNotEmpty) {
		t.Errorf("DeleteBucket with an active upload = %v, want ErrBucketNotEmpty", err)
	}
	if err := store.ValidateMultipartUpload(ctx, upload.UploadID, upload.Bucket, upload.Key); err != nil {
		t.Errorf("the rejected deletion disturbed the upload: %v", err)
	}
	if err := store.AbortMultipartUpload(ctx, upload.UploadID); err != nil {
		t.Fatalf("AbortMultipartUpload: %v", err)
	}
	if err := store.DeleteBucket(ctx, other); err != nil {
		t.Errorf("DeleteBucket on an emptied bucket: %v", err)
	}
	if err := store.DeleteBucket(ctx, other); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("second DeleteBucket = %v, want ErrBucketNotFound", err)
	}
}

// TestDeleteBucketRefusesTheWorkspaceBucket pins the rule that the workspace's
// own bucket cannot be deleted: it is the directory the caller is working in,
// not a container stow owns.
func TestDeleteBucketRefusesTheWorkspaceBucket(t *testing.T) {
	store, _ := newStore(t)
	if err := store.DeleteBucket(context.Background(), bucket); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("DeleteBucket on the workspace bucket = %v, want ErrBucketNotFound", err)
	}
}

// TestCopyAndBatchDelete covers the two object operations the round-trip cases
// do not reach.
func TestCopyAndBatchDelete(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	put(t, store, "source.txt", "the original bytes")
	meta, err := store.CopyObject(ctx, bucket, "source.txt", bucket, "copy.txt")
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if meta.Size != int64(len("the original bytes")) {
		t.Errorf("copied Size = %d, want %d", meta.Size, len("the original bytes"))
	}
	if got := get(t, store, "copy.txt"); got != "the original bytes" {
		t.Errorf("copy = %q, want the source bytes", got)
	}
	// The copy is a real file, not an alias: editing one must not edit the other.
	if err := os.WriteFile(filepath.Join(root, "copy.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("overwrite the copy: %v", err)
	}
	if got := get(t, store, "source.txt"); got != "the original bytes" {
		t.Errorf("the source changed when the copy was edited: %q", got)
	}

	// DeleteObjects confirms every key it was asked to delete, the one that was
	// never there included: S3 reports a missing key as deleted. The assertion
	// below also checks the deletion really happened, so the list is not
	// satisfied by echoing the request back.
	confirmed, err := store.DeleteObjects(ctx, bucket, []string{"copy.txt", "never-existed.txt"})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if !slices.Equal(confirmed, []string{"copy.txt", "never-existed.txt"}) {
		t.Errorf("DeleteObjects confirmed = %v, want both keys", confirmed)
	}
	if _, _, err := store.GetObject(ctx, bucket, "copy.txt"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("the deleted key is still readable: %v", err)
	}
}
