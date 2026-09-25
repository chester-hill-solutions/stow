package stow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stow "github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

// W1: pkg/stow opens a persistent workspace with no injected store and no child
// process. The proof is a file the caller wrote with the ordinary filesystem
// being served through the runtime's object API, in-process, in the same test
// binary — which is the whole of ADR 0007's claim for the Go path.

// openWorkspace opens a workspace in a fresh directory and closes it after.
func openWorkspace(t *testing.T, dir string) *stow.Workspace {
	t.Helper()
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{Dir: dir})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("close workspace: %v", err)
		}
	})
	return ws
}

// TestWorkspaceServesAFileTheHostWrote is the same-bytes claim reached through
// the public package rather than the store. Nothing here starts a process.
func TestWorkspaceServesAFileTheHostWrote(t *testing.T) {
	dir := t.TempDir()
	ws := openWorkspace(t, dir)
	ctx := context.Background()

	path := filepath.Join(dir, "output", "report.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := "written by the host, never by stow"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	object, err := ws.GetObject(ctx, ws.Bucket(), "output/report.txt")
	if err != nil {
		t.Fatalf("GetObject on a host-written file: %v", err)
	}
	if !bytes.Equal(object.Data, []byte(want)) {
		t.Errorf("GetObject = %q, want %q", object.Data, want)
	}
	if object.Size != int64(len(want)) {
		t.Errorf("Size = %d, want %d", object.Size, len(want))
	}
}

// TestWorkspaceWritesARealFile is the other direction, through the public API.
func TestWorkspaceWritesARealFile(t *testing.T) {
	dir := t.TempDir()
	ws := openWorkspace(t, dir)
	ctx := context.Background()

	want := "written through the object API"
	if _, err := ws.PutObject(ctx, ws.Bucket(), "output/summary.md", []byte(want), stow.PutOptions{
		ContentType: "text/markdown",
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "output", "summary.md"))
	if err != nil {
		t.Fatalf("the object is not a file on disk: %v", err)
	}
	if string(got) != want {
		t.Errorf("on disk = %q, want %q", got, want)
	}
}

// TestWorkspacePathAnswersWithoutReading pins Path: a host can print a real
// path for a caller without an S3 round trip, and an absent key says so rather
// than returning a path that does not exist.
func TestWorkspacePathAnswersWithoutReading(t *testing.T) {
	dir := t.TempDir()
	ws := openWorkspace(t, dir)
	ctx := context.Background()

	if _, err := ws.PutObject(ctx, ws.Bucket(), "found.txt", []byte("x"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	path, ok := ws.Path("found.txt")
	if !ok {
		t.Fatal("Path reported an existing key as absent")
	}
	if want := filepath.Join(dir, "found.txt"); path != want {
		t.Errorf("Path = %q, want %q", path, want)
	}
	if path, ok := ws.Path("absent.txt"); ok || path != "" {
		t.Errorf("Path on an absent key = (%q, %v), want (\"\", false)", path, ok)
	}
}

// TestWorkspaceIsPersistentAndBounded covers the two properties a host sets and
// then forgets: the bytes survive the handle, and the quotas are enforced by the
// runtime rather than merely reported.
func TestWorkspaceIsPersistentAndBounded(t *testing.T) {
	dir := t.TempDir()
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
		Dir:        dir,
		MaxBytes:   64,
		MaxObjects: 2,
	})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	ctx := context.Background()
	if _, err := ws.PutObject(ctx, ws.Bucket(), "small.txt", []byte("within the bound"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject within the bound: %v", err)
	}
	// The quota a host sets is the quota enforced.
	if _, err := ws.PutObject(ctx, ws.Bucket(), "too-big.txt", bytes.Repeat([]byte("x"), 256), stow.PutOptions{}); !errors.Is(err, stow.ErrQuotaExceeded) {
		t.Errorf("PutObject over MaxBytes = %v, want ErrQuotaExceeded", err)
	}
	if _, err := ws.PutObject(ctx, ws.Bucket(), "second.txt", []byte("b"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if _, err := ws.PutObject(ctx, ws.Bucket(), "third.txt", []byte("c"), stow.PutOptions{}); !errors.Is(err, stow.ErrQuotaExceeded) {
		t.Errorf("PutObject over MaxObjects = %v, want ErrQuotaExceeded", err)
	}

	capabilities := ws.Capabilities()
	if !capabilities.Persistent {
		t.Error("a workspace must report itself persistent")
	}
	if capabilities.Backend != stow.BackendWorkspace {
		t.Errorf("Backend = %q, want %q", capabilities.Backend, stow.BackendWorkspace)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The bytes outlive the handle, which is ADR 0009's whole claim and the
	// reason a workspace is not a scoped session.
	reopened := openWorkspace(t, dir)
	object, err := reopened.GetObject(ctx, reopened.Bucket(), "small.txt")
	if err != nil {
		t.Fatalf("GetObject after reopen: %v", err)
	}
	if string(object.Data) != "within the bound" {
		t.Errorf("after reopen = %q, want the bytes", object.Data)
	}
	if reopened.ID() != ws.ID() {
		t.Errorf("ID changed across reopen: %q then %q", ws.ID(), reopened.ID())
	}
}

// TestCloseLeavesTheData covers WS-11 at the public boundary: closing a handle
// is not deleting a workspace, so a caller who wants the bytes gone has to say
// so. Removal itself is Phase B work, which is why there is no Destroy here yet.
func TestCloseLeavesTheData(t *testing.T) {
	dir := t.TempDir()
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{Dir: dir})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	ctx := context.Background()
	if _, err := ws.PutObject(ctx, ws.Bucket(), "keep.txt", []byte("still here"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Errorf("Close removed data: %v", err)
	}
	// Close is idempotent, and a closed workspace refuses work.
	if err := ws.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := ws.PutObject(ctx, ws.Bucket(), "late.txt", []byte("x"), stow.PutOptions{}); !errors.Is(err, stow.ErrClosed) {
		t.Errorf("PutObject after Close = %v, want ErrClosed", err)
	}
}

// TestOpenWorkspaceRequiresADirectory pins the one required option. A workspace
// is a place; an option that made it optional would let a caller ask for a
// workspace with nowhere to put it.
func TestOpenWorkspaceRequiresADirectory(t *testing.T) {
	if _, err := stow.OpenWorkspace(stow.WorkspaceOptions{}); err == nil {
		t.Error("OpenWorkspace accepted an empty Dir")
	}
}

// TestOpenRefusesTheWorkspaceBackend closes the trap the API is shaped to
// avoid: there is a BackendWorkspace constant, and Open cannot serve it, so it
// says so rather than handing back a memory runtime that would lose the bytes.
func TestOpenRefusesTheWorkspaceBackend(t *testing.T) {
	_, err := stow.Open(stow.Options{Backend: stow.BackendWorkspace})
	if !errors.Is(err, stow.ErrUnsupportedBackend) {
		t.Errorf("Open with the workspace backend = %v, want ErrUnsupportedBackend", err)
	}
}

// TestOpenStillServesMemory guards the existing profile, because the backend
// validation was rewritten to admit the workspace backend and could plausibly
// have narrowed it.
func TestOpenStillServesMemory(t *testing.T) {
	runtime, err := stow.Open(stow.Options{Backend: stow.BackendMemory})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer runtime.Close()
	ctx := context.Background()
	if err := runtime.CreateBucket(ctx, "assets"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "assets", "hello.txt", []byte("hello"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	object, err := runtime.GetObject(ctx, "assets", "hello.txt")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(object.Data) != "hello" {
		t.Errorf("GetObject = %q, want %q", object.Data, "hello")
	}
	if runtime.Capabilities().Persistent {
		t.Error("a memory runtime must not report itself persistent")
	}
}

// TestAdoptingAnExistingDirectory covers the case that makes a workspace worth
// having: pointing one at the directory a caller is already working in, with no
// import step and no migration.
func TestAdoptingAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("already here"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ws := openWorkspace(t, dir)
	ctx := context.Background()
	page, err := ws.ListObjects(ctx, ws.Bucket(), stow.ListOptions{})
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	keys := map[string]bool{}
	for _, object := range page.Objects {
		keys[object.Key] = true
	}
	if !keys["notes.md"] || !keys["src/main.go"] {
		t.Errorf("listed %v, want the pre-existing files", keys)
	}
	object, err := ws.GetObject(ctx, ws.Bucket(), "src/main.go")
	if err != nil {
		t.Fatalf("GetObject on an adopted file: %v", err)
	}
	if !strings.Contains(string(object.Data), "package main") {
		t.Errorf("adopted file = %q", object.Data)
	}
	// And stow's own bookkeeping is not offered as an object.
	if keys[".stow/manifest.json"] {
		t.Error("listing offered the workspace manifest as an object")
	}
}
