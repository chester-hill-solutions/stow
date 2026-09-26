package storage_test

// The completion contract: what a client gets back when it finishes a multipart
// upload, and what it is told when it gets it wrong.
//
// These cases are in one file because they are one concern. They were spread
// across the backend contract suite and this file's neighbours, which is how a
// rule that all three backends share ended up written out three times and
// correct in one of them.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// completeOnePart uploads a single part of body and completes with it, returning
// the completed object's ETag.
func completeOnePart(t *testing.T, store storage.Store, body []byte) string {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "uploads"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	multi := requireMultipart(t, store)
	upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	part, err := multi.UploadPart(ctx, upload.UploadID, 1, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	meta, err := multi.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	return meta.ETag
}

// A completion with exactly one part is a single-part object, and S3 gives it a
// plain MD5 — the part's own ETag, verbatim. The `-{partCount}` suffix belongs to
// genuinely multipart objects, where it is the digest of the concatenated part
// digests. A single-part completion suffixed `-1` is a different value from what
// S3 returns, and it is the value the client carries into every conditional
// write it attempts afterwards.
//
// This case did not exist, which is why two of the three backends returned
// md5(md5(body))-1 here and only the third returned the part's ETag. The rule
// was in three places and correct in one of them.
func TestStoreSinglePartCompletionETagIsThePartETag(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		body := []byte("a single part body")
		want := storage.ETagForBytes(body)
		if got := completeOnePart(t, store, body); got != want {
			t.Fatalf("single-part completion ETag = %s, want the part ETag %s", got, want)
		}
	})
}

// The counterpart, pinning the case that was already right: two or more parts
// produce the composite digest, with the part count in the suffix. Without this,
// "fixing" the single-part case by always using CompositeETag would break the
// multipart case silently.
func TestStoreMultiPartCompletionETagIsComposite(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		multi := requireMultipart(t, store)
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		first, err := multi.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("first"))
		if err != nil {
			t.Fatalf("upload first part: %v", err)
		}
		second, err := multi.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("second"))
		if err != nil {
			t.Fatalf("upload second part: %v", err)
		}
		meta, err := multi.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*first, *second})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		want := storage.CompositeETag([]string{first.ETag, second.ETag})
		if meta.ETag != want {
			t.Fatalf("two-part completion ETag = %s, want %s", meta.ETag, want)
		}
	})
}

// An ETag is a function of the object's bytes, not of the path taken to store
// them. The same body uploaded as one part and uploaded as a whole are the same
// object, so they carry the same ETag. This is what makes an ETag portable
// between a PutObject and a CompleteMultipartUpload, and it is what the compat
// contract claims when it says single-part objects carry a quoted hex MD5.
func TestStoreCompletionETagMatchesTheSameBytesPutDirectly(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "contract"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		body := []byte("the same bytes either way")
		direct, err := store.PutObject(ctx, "contract", "direct", bytes.NewReader(body), storage.PutOptions{})
		if err != nil {
			t.Fatalf("put object: %v", err)
		}
		if got := completeOnePart(t, store, body); got != direct.ETag {
			t.Fatalf("completion ETag = %s, PutObject ETag = %s; the ETag must depend on the bytes, not the write path", got, direct.ETag)
		}
	})
}

// Completing with no parts assembles nothing and publishes a zero-byte object
// under the caller's key, silently discarding whatever was uploaded. S3 rejects
// it, and the rejection belongs to all three backends.
//
// This case did not exist, which is why the emptiness check was written out
// twice — in the memory and filesystem backends — and the third backend had
// none. A client completing an upload with an empty part list therefore got a
// 500 on two stores and a zero-byte object on the third: the same request, three
// answers, none of them S3's.
func TestStoreRejectsEmptyMultipartCompletion(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		multi := requireMultipart(t, store)
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		if _, err := multi.CompleteMultipartUpload(ctx, upload.UploadID, nil); err == nil {
			t.Fatal("completing with no parts succeeded; want a rejection, because it publishes a zero-byte object under the caller's key")
		}
	})
}

func TestStoreRejectsDuplicateMultipartParts(t *testing.T) {
	withStores(t, func(t *testing.T, store storage.Store) {
		ctx := context.Background()
		if err := store.CreateBucket(ctx, "uploads"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		multi := requireMultipart(t, store)
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		part, err := multi.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("part"))
		if err != nil {
			t.Fatalf("upload part: %v", err)
		}
		_, err = multi.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part, *part})
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
		multi := requireMultipart(t, store)
		upload, err := multi.CreateMultipartUpload(ctx, "uploads", "object.bin")
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		first, err := multi.UploadPart(ctx, upload.UploadID, 1, strings.NewReader("first"))
		if err != nil {
			t.Fatalf("upload first part: %v", err)
		}
		second, err := multi.UploadPart(ctx, upload.UploadID, 2, strings.NewReader("second"))
		if err != nil {
			t.Fatalf("upload second part: %v", err)
		}
		parts := []storage.PartInfo{*second, *first}
		if _, err := multi.CompleteMultipartUpload(ctx, upload.UploadID, parts); !errors.Is(err, storage.ErrInvalidPart) {
			t.Fatalf("complete error = %v, want ErrInvalidPart", err)
		}
		if parts[0].PartNumber != 2 {
			t.Fatalf("completion parts were reordered: %+v", parts)
		}
	})
}
