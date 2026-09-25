package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// This file holds WS-01 and WS-02 from docs/workspace-contract.md section 8.
// They are the acceptance gate for the entire workspace direction, and they are
// written before the backend they test.
//
// Both were run against the memory and filesystem stores first and both failed,
// which is the only evidence that they can fail at all. A store is not a
// workspace: it keeps object bytes somewhere that is not a path the caller can
// name. See docs/agent-dx-plan.md section 0.6 for the three other times in this
// repository that a green suite sat on top of a path that had never run.

// rootedStores returns one store per backend, each paired with the directory a
// caller would be working in. The workspace's own bucket is the bucket the
// case uses, so the same-bytes claim is tested against the real mapping —
// objects at the workspace root — rather than a namespaced side directory.
//
// A store with no directory cannot be a workspace, which is why the in-memory
// store is absent from this list rather than special-cased.
type rootedStore struct {
	name  string
	store storage.Store
	root  string
	// isWorkspace is false for a backend that keeps object bytes somewhere
	// other than the caller's directory. Those backends are reported as
	// skipped rather than failed: a record store is not a working directory,
	// and asserting that it fails to be one would pin a deficiency rather than
	// a requirement. They stay in the list so the contrast is visible in
	// verbose output, and so a new backend cannot quietly skip the gate.
	isWorkspace bool
}

func rootedStores(t *testing.T, bucket string) []rootedStore {
	t.Helper()

	fsRoot := t.TempDir()
	fsStore, err := fs.NewFilesystemStore(fsRoot)
	if err != nil {
		t.Fatalf("new filesystem store: %v", err)
	}
	t.Cleanup(func() {
		if err := fsStore.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	wsRoot := t.TempDir()
	wsStore := newWorkspace(t, wsRoot, bucket)

	return []rootedStore{
		{name: "filesystem", store: fsStore, root: fsRoot, isWorkspace: false},
		{name: "workspace", store: wsStore, root: wsRoot, isWorkspace: true},
	}
}

// newWorkspace opens a workspace store rooted at root whose own bucket is
// bucket.
func newWorkspace(t *testing.T, root, bucket string) storage.Store {
	t.Helper()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new workspace store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func withRootedStores(t *testing.T, bucket string, test func(*testing.T, rootedStore)) {
	t.Helper()
	for _, candidate := range rootedStores(t, bucket) {
		t.Run(candidate.name, func(t *testing.T) {
			if !candidate.isWorkspace {
				t.Skipf("%s is a store, not a workspace: it keeps object bytes in its own "+
					"layout rather than at the caller's path, so the same-bytes claim does "+
					"not apply to it", candidate.name)
			}
			test(t, candidate)
		})
	}
}

// WS-01: a file written through the host's own filesystem API, with stow never
// involved, is returned by GetObject with identical bytes.
//
// This is the half of the product claim that makes a workspace a workspace. An
// agent that has been told to read output/report.pdf must be able to hand stow
// that key and get the file, without an import step and without stow having
// observed the write.
func TestWS01ServesAFileTheHostWrote(t *testing.T) {
	const bucket = "ws01"
	withRootedStores(t, bucket, func(t *testing.T, candidate rootedStore) {
		ctx := context.Background()
		if err := candidate.store.CreateBucket(ctx, bucket); err != nil {
			t.Fatalf("CreateBucket: %v", err)
		}

		want := []byte("%PDF-1.7 the host wrote this without stow\n")
		path := filepath.Join(candidate.root, "output", "report.pdf")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write host file: %v", err)
		}

		body, meta, err := candidate.store.GetObject(ctx, bucket, "output/report.pdf")
		if err != nil {
			t.Fatalf("GetObject on a file the host wrote: %v", err)
		}
		defer body.Close()

		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("bytes differ\n got %q\nwant %q", got, want)
		}
		if meta.Size != int64(len(want)) {
			t.Errorf("Size = %d, want %d", meta.Size, len(want))
		}
	})
}

// WS-02: an object written through PutObject exists on disk as a real file with
// identical bytes, at a path a person can open.
//
// The other half. A workspace whose objects are only reachable through S3 is a
// store with a friendlier name, and the bytes an agent leaves behind have to
// survive in a form a human can be handed.
func TestWS02PutObjectIsARealFileOnDisk(t *testing.T) {
	const bucket = "ws02"
	withRootedStores(t, bucket, func(t *testing.T, candidate rootedStore) {
		ctx := context.Background()
		if err := candidate.store.CreateBucket(ctx, bucket); err != nil {
			t.Fatalf("CreateBucket: %v", err)
		}

		want := []byte("# summary\n\nwritten through the S3 API\n")
		if _, err := candidate.store.PutObject(ctx, bucket, "output/summary.md", bytes.NewReader(want), storage.PutOptions{
			ContentType: "text/markdown",
		}); err != nil {
			t.Fatalf("PutObject: %v", err)
		}

		path := filepath.Join(candidate.root, "output", "summary.md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the object is not a file a person can open, at %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("bytes on disk differ\n got %q\nwant %q", got, want)
		}
	})
}
