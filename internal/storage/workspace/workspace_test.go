package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// The cases here are WS-03 through WS-14 from docs/workspace-contract.md
// section 8. WS-01 and WS-02, the two that gate the whole product direction,
// live in internal/storage/samebytes_test.go because they compare the workspace
// against a store that cannot satisfy them.
//
// These live in the workspace package rather than beside the comparison so that
// they are measured against it: the coverage ratchet runs `go test ./...
// -coverprofile` with no -coverpkg, so a backend's coverage is only what its own
// tests exercise.

const bucket = "workspace"

func newStore(t *testing.T) (*workspace.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new workspace store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store, root
}

func put(t *testing.T, store *workspace.Store, key, body string) {
	t.Helper()
	if _, err := store.PutObject(context.Background(), bucket, key, strings.NewReader(body), storage.PutOptions{}); err != nil {
		t.Fatalf("PutObject(%q): %v", key, err)
	}
}

func get(t *testing.T, store *workspace.Store, key string) string {
	t.Helper()
	body, _, err := store.GetObject(context.Background(), bucket, key)
	if err != nil {
		t.Fatalf("GetObject(%q): %v", key, err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	return string(data)
}

// WS-03: a file stow did not write, with no manifest entry, is listed and
// readable. Adoption is a first-class operation, not an import step.
func TestWS03AdoptsAFileWithNoManifestEntry(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(root, "output"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := "written by a tool stow never saw"
	if err := os.WriteFile(filepath.Join(root, "output", "report.txt"), []byte(want), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := get(t, store, "output/report.txt"); got != want {
		t.Errorf("GetObject = %q, want %q", got, want)
	}
	listed, err := store.ListObjectsV2(ctx, bucket, storage.ListOptions{})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(listed.Objects) != 1 || listed.Objects[0].Key != "output/report.txt" {
		t.Fatalf("listed %+v, want the adopted file", listed.Objects)
	}
	if listed.Objects[0].Size != int64(len(want)) {
		t.Errorf("Size = %d, want %d", listed.Objects[0].Size, len(want))
	}
}

// WS-08: opening a directory with pre-existing files exposes them, with no
// import step. Turning the directory an agent is already in is the default case.
func TestWS08AdoptsAPreExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("already here"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("open existing directory: %v", err)
	}
	defer store.Close()

	if got := get(t, store, "notes.md"); got != "already here" {
		t.Errorf("GetObject = %q, want %q", got, "already here")
	}
}

// keysThatCannotBeNaturalPaths covers the cases the encoding exists for: a key
// long enough to exceed a path component, and keys whose segments are not
// usable as file names. Every one is legal S3 content and must round-trip.
func awkwardKeys() map[string]string {
	long := strings.Repeat("k", 1024)
	return map[string]string{
		"1024-byte key":       long,
		"parent traversal":    "dir/../name.txt",
		"current dir segment": "dir/./name.txt",
		"leading slash":       "/leading.txt",
		"escape marker":       "dir/na~me.txt",
		"windows device name": "dir/NUL.txt",
		"windows illegal":     "dir/what?.txt",
		"trailing space":      "dir/trailing ",
		"trailing dot":        "dir/trailing.",
		"reserved internal":   ".stow/impostor.txt",
	}
}

// WS-04 and WS-05: every key the S3 contract accepts round-trips, including the
// ones that cannot be a path. The encoding is total, so a key is never
// truncated, rejected, or silently rewritten.
func TestAwkwardKeysRoundTrip(t *testing.T) {
	for name, key := range awkwardKeys() {
		t.Run(name, func(t *testing.T) {
			store, _ := newStore(t)
			body := "payload for " + key
			put(t, store, key, body)
			if got := get(t, store, key); got != body {
				t.Errorf("GetObject(%q) = %q, want %q", key, got, body)
			}
		})
	}
}

// TestAwkwardKeysAreStoredInTheEscapedForm pins the half of the contract that
// says which form is used. A key that could be natural but lands escaped is
// still correct; a key that cannot be natural but lands natural is a bug that
// would corrupt a neighbouring file.
func TestAwkwardKeysAreStoredInTheEscapedForm(t *testing.T) {
	store, root := newStore(t)
	for name, key := range awkwardKeys() {
		t.Run(name, func(t *testing.T) {
			put(t, store, key, "x")
			natural := filepath.Join(root, filepath.FromSlash(key))
			if _, err := os.Lstat(natural); err == nil {
				// A traversal or leading-slash key would resolve outside the
				// workspace or to the workspace root, which is exactly what the
				// escaped form exists to prevent.
				if key != "dir/../name.txt" && key != "dir/./name.txt" && key != "/leading.txt" {
					t.Errorf("key %q was stored at its natural path %s", key, natural)
				}
			}
			escaped := filepath.Join(root, ".stow", "keys", workspace.Digest(key))
			if _, err := os.Lstat(escaped); err != nil {
				t.Errorf("key %q is not in the escaped form at %s: %v", key, escaped, err)
			}
		})
	}
}

// TestNaturalKeysStayNatural is the other half: a key that could be its own
// path must be, because that is the entire product claim. A workspace that
// escaped everything would pass every round-trip test and be useless.
func TestNaturalKeysStayNatural(t *testing.T) {
	store, root := newStore(t)
	put(t, store, "output/report.pdf", "bytes")
	if _, err := os.Lstat(filepath.Join(root, "output", "report.pdf")); err != nil {
		t.Errorf("a key that could be a path was not stored there: %v", err)
	}
}

// WS-06: two keys differing only in case are two distinct objects. On a
// case-insensitive filesystem they are one file, and serving both from it loses
// one silently.
func TestKeysDifferingOnlyInCaseAreDistinct(t *testing.T) {
	store, _ := newStore(t)
	put(t, store, "Report.pdf", "upper")
	put(t, store, "report.pdf", "lower")

	if got := get(t, store, "Report.pdf"); got != "upper" {
		t.Errorf("Report.pdf = %q, want %q", got, "upper")
	}
	if got := get(t, store, "report.pdf"); got != "lower" {
		t.Errorf("report.pdf = %q, want %q", got, "lower")
	}
}

// WS-07: deleting an object removes its file, and forgets it.
func TestDeleteObjectRemovesTheFile(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()

	put(t, store, "output/gone.txt", "bye")
	path := filepath.Join(root, "output", "gone.txt")
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := store.DeleteObject(ctx, bucket, "output/gone.txt"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file survived deletion: %v", err)
	}
	if _, _, err := store.GetObject(ctx, bucket, "output/gone.txt"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("GetObject after delete = %v, want ErrObjectNotFound", err)
	}
	// The emptied directory goes too, so a deleted object leaves no scaffolding.
	if _, err := os.Lstat(filepath.Join(root, "output")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the emptied parent directory survived: %v", err)
	}
}

// TestDeleteObjectRemovesAnEscapedFile covers the escaped form's delete path,
// which is a different file and a different manifest entry.
func TestDeleteObjectRemovesAnEscapedFile(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	key := "dir/../name.txt"
	put(t, store, key, "bye")
	escaped := filepath.Join(root, ".stow", "keys", workspace.Digest(key))
	if _, err := os.Lstat(escaped); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := store.DeleteObject(ctx, bucket, key); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := os.Lstat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the escaped file survived deletion: %v", err)
	}
}

// TestMetadataIsDerivedForAForeignFile covers the adoption metadata: a file
// stow did not write has no declared content type, so one is derived rather
// than invented, and the ETag is the content hash.
func TestMetadataIsDerivedForAForeignFile(t *testing.T) {
	store, root := newStore(t)
	body := "plain text written by something else"
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	meta, err := store.HeadObject(context.Background(), bucket, "note.txt")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if want := storage.ETagForBytes([]byte(body)); !storage.ETagEqual(meta.ETag, want) {
		t.Errorf("ETag = %s, want the content hash %s", meta.ETag, want)
	}
	if !strings.HasPrefix(meta.ContentType, "text/plain") {
		t.Errorf("ContentType = %q, want a derived text type", meta.ContentType)
	}
	if meta.LastModified.IsZero() {
		t.Error("LastModified was not derived from the file")
	}
}

// TestOverwritingAForeignFileIsPickedUp is the staleness rule from the
// contract: an agent may overwrite a file stow knows about, and the next read
// must see the new bytes rather than the cached ETag.
func TestOverwritingAForeignFileIsPickedUp(t *testing.T) {
	store, root := newStore(t)
	path := filepath.Join(root, "log.txt")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := get(t, store, "log.txt"); got != "first" {
		t.Fatalf("precondition: got %q", got)
	}
	// A different size and modification time is what a real overwrite looks
	// like, and it is what the staleness check compares.
	if err := os.WriteFile(path, []byte("second, and longer"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := get(t, store, "log.txt"); got != "second, and longer" {
		t.Errorf("after an overwrite behind stow's back = %q, want the new bytes", got)
	}
}

// TestAWriteCarriesItsMetadata proves the manifest is doing its job: what a
// caller put is what comes back, which a derived-from-the-file reading could
// not deliver.
func TestAWriteCarriesItsMetadata(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	_, err := store.PutObject(ctx, bucket, "doc.json", bytes.NewReader([]byte(`{"a":1}`)), storage.PutOptions{
		ContentType: "application/json",
		Metadata:    map[string]string{"origin": "test"},
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	meta, err := store.HeadObject(ctx, bucket, "doc.json")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if meta.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want the declared type", meta.ContentType)
	}
	if meta.Metadata["origin"] != "test" {
		t.Errorf("Metadata = %v, want the declared metadata", meta.Metadata)
	}
}

// TestABucketOtherThanTheWorkspaceIsIsolated covers the routing decision: the
// workspace's own bucket is the directory, and a second bucket is namespaced so
// that having one does not stop the workspace bucket being the working
// directory.
func TestABucketOtherThanTheWorkspaceIsIsolated(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	const other = "scratch"
	if err := store.CreateBucket(ctx, other); err != nil {
		t.Fatalf("CreateBucket(%q): %v", other, err)
	}
	put(t, store, "shared.txt", "in the workspace")
	if _, err := store.PutObject(ctx, other, "shared.txt", strings.NewReader("in the other bucket"), storage.PutOptions{}); err != nil {
		t.Fatalf("PutObject into %q: %v", other, err)
	}

	body, _, err := store.GetObject(ctx, other, "shared.txt")
	if err != nil {
		t.Fatalf("GetObject from %q: %v", other, err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if string(data) != "in the other bucket" {
		t.Errorf("%q returned %q, want the other bucket's bytes", other, data)
	}
	if got := get(t, store, "shared.txt"); got != "in the workspace" {
		t.Errorf("workspace bucket = %q, want its own bytes", got)
	}
	if _, err := os.Lstat(filepath.Join(root, ".stow", "buckets", other, "shared.txt")); err != nil {
		t.Errorf("the second bucket is not namespaced under the internal directory: %v", err)
	}
}

// TestMissingBucketAndObjectAreDistinguished pins the sentinels the layering
// adapters depend on. A caching or layering adapter that treats only
// ErrObjectNotFound as a miss stops at the first missing parent, and the symptom
// reads like a client error rather than a cache miss.
func TestMissingBucketAndObjectAreDistinguished(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if err := store.CreateBucket(ctx, bucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, _, err := store.GetObject(ctx, "no-such-bucket", "k"); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("missing bucket = %v, want ErrBucketNotFound", err)
	}
	if _, _, err := store.GetObject(ctx, bucket, "no-such-key"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("missing key = %v, want ErrObjectNotFound", err)
	}
}

// TestListingSkipsTheInternalDirectory proves stow's own bookkeeping is not
// reported as objects. Without this, a workspace's .stow directory would appear
// in every list call.
func TestListingSkipsTheInternalDirectory(t *testing.T) {
	store, _ := newStore(t)
	put(t, store, "visible.txt", "an object")
	long := strings.Repeat("k", 1024)
	put(t, store, long, "an escaped object")

	listed, err := store.ListObjectsV2(context.Background(), bucket, storage.ListOptions{})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	keys := map[string]bool{}
	for _, meta := range listed.Objects {
		keys[meta.Key] = true
		if strings.HasPrefix(meta.Key, ".stow") {
			t.Errorf("listing reported stow's own directory as an object: %q", meta.Key)
		}
	}
	if !keys["visible.txt"] || !keys[long] || len(keys) != 2 {
		t.Errorf("listed %v, want exactly the two objects", keys)
	}
}
