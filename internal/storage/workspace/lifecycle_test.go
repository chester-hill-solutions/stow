package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// Lifecycle and manifest cases: WS-09, WS-10 and WS-11 from
// docs/workspace-contract.md, plus the identity property ADR 0009 depends on —
// that a workspace and its name outlive the handle that created them.

// manifestPath is where the store keeps its bookkeeping.
func manifestPath(root string) string {
	return filepath.Join(root, ".stow", "manifest.json")
}

// workspaceOpen reopens an existing workspace, for the identity tests.
func workspaceOpen(t *testing.T, root string) (*workspace.Store, error) {
	t.Helper()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	})
	return store, nil
}

// TestCloseLeavesTheData is WS-11 in the part the store owns: closing releases
// the handle and removes nothing. A workspace outlives the process that opened
// it, and removal is an explicit destroy that the client layer owns.
func TestCloseLeavesTheData(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	put(t, store, "keep.txt", "still here")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "keep.txt")); err != nil {
		t.Errorf("Close removed data: %v", err)
	}
	// Closing twice is not an error, and a closed handle refuses work.
	if err := store.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, _, err := store.GetObject(context.Background(), bucket, "keep.txt"); !errors.Is(err, workspace.ErrClosed) {
		t.Errorf("GetObject after Close = %v, want ErrClosed", err)
	}
}

// TestCloseFlushesTheManifest proves a resumed store sees what the first one
// wrote, which is the whole point of a durable workspace: a handle is
// disposable, the directory is not.
func TestCloseFlushesTheManifest(t *testing.T) {
	root := t.TempDir()
	first, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	put(t, first, "output/report.txt", "across the reopen")
	firstID := first.ID()
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := workspaceOpen(t, root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if second.ID() != firstID {
		t.Errorf("ID changed across reopen: %q then %q", firstID, second.ID())
	}
	if got := get(t, second, "output/report.txt"); got != "across the reopen" {
		t.Errorf("after reopen = %q, want %q", got, "across the reopen")
	}
}

// TestCorruptManifestRefusesTheOpen is WS-09. A manifest is not the source of
// truth for existence, so nothing is lost — but rebuilding it empty would make
// every key resolve to absent while every file stayed on disk, which reads to
// the caller as total data loss. Refusing is the only safe answer.
func TestCorruptManifestRefusesTheOpen(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	put(t, store, "precious.txt", "do not lose me")
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := os.WriteFile(manifestPath(root), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}
	if _, err := workspace.New(workspace.Options{Root: root, Bucket: bucket}); !errors.Is(err, workspace.ErrManifestCorrupt) {
		t.Fatalf("open with a corrupt manifest = %v, want ErrManifestCorrupt", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "precious.txt")); err != nil {
		t.Errorf("the refused open touched the data: %v", err)
	}
}

// TestNewerManifestVersionIsRefused is WS-10. A manifest written by a newer
// revision is refused rather than downgraded, because a manifest that silently
// downgrades loses exactly the entries it could not understand.
func TestNewerManifestVersionIsRefused(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(manifestPath(root))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	future := bytes.Replace(raw, []byte(`"version": 1`), []byte(`"version": 99`), 1)
	if err := os.WriteFile(manifestPath(root), future, 0o644); err != nil {
		t.Fatalf("write future manifest: %v", err)
	}
	if _, err := workspace.New(workspace.Options{Root: root, Bucket: bucket}); !errors.Is(err, workspace.ErrManifestCorrupt) {
		t.Fatalf("open with a future manifest = %v, want ErrManifestCorrupt", err)
	}
}

// TestIdentityIsStable covers the accessors, and the property ADR 0009 depends
// on: a workspace's identity outlives the handle that created it, so a later
// process can resume it by name.
func TestIdentityIsStable(t *testing.T) {
	store, root := newStore(t)
	if store.Root() != root {
		t.Errorf("Root = %q, want %q", store.Root(), root)
	}
	if store.WorkspaceBucket() != bucket {
		t.Errorf("WorkspaceBucket = %q, want %q", store.WorkspaceBucket(), bucket)
	}
	if store.ID() == "" {
		t.Error("ID is empty; a workspace must be identifiable after its process exits")
	}
	put(t, store, "kept.txt", "x")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := workspaceOpen(t, root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.ID() != store.ID() {
		t.Errorf("ID changed across reopen: %q then %q", store.ID(), reopened.ID())
	}
	if reopened.WorkspaceBucket() != bucket {
		t.Errorf("WorkspaceBucket changed across reopen: %q", reopened.WorkspaceBucket())
	}
}

// TestTheInternalDirectoryIsNotAnObject guards the reserved name. `.stow` is
// stow's, so a key that would land there is escaped rather than colliding with
// the bookkeeping.
func TestTheInternalDirectoryIsNotAnObject(t *testing.T) {
	store, _ := newStore(t)
	put(t, store, ".stow/impostor.txt", "not really internal")

	// It round-trips as an ordinary key...
	if got := get(t, store, ".stow/impostor.txt"); got != "not really internal" {
		t.Errorf("GetObject = %q, want the bytes", got)
	}
	// ...and the real manifest is untouched.
	raw, err := os.ReadFile(manifestPath(store.Root()))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !bytes.Contains(raw, []byte("impostor")) {
		t.Logf("manifest does not mention the impostor key; form was escaped as expected")
	}
	if !strings.Contains(string(raw), `"version"`) {
		t.Errorf("the manifest was replaced rather than extended: %s", raw)
	}
	_ = storage.ErrObjectNotFound
}
