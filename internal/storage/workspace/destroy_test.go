package workspace_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// Destroy is the operation ADR 0009 requires to be explicit, and the only one
// that deletes anything a caller cares about. These cases are therefore mostly
// about refusals: the dangerous outcomes are the ones that must not happen.

// TestDestroyRemovesAWorkspaceStowCreated is the happy path, and it is the only
// one that should remove anything.
func TestDestroyRemovesAWorkspaceStowCreated(t *testing.T) {
	store, root := newOwnedStore(t)
	put(t, store, "output/report.txt", "bytes")
	if err := store.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the workspace directory survived Destroy: %v", err)
	}
}

// TestDestroyRefusesAnAdoptedWorkspace is the case that decides the design.
//
// A workspace is meant to be pointed at a directory the caller already has, so
// the presence of a manifest is not evidence that the contents are stow's. If
// adoption and destruction were both allowed, the documented way to use this
// package would be a way to delete someone's project.
func TestDestroyRefusesAnAdoptedWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "thesis.md"), []byte("a year of work"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("open adopted workspace: %v", err)
	}
	defer store.Close()

	if err := store.Destroy(); !errors.Is(err, workspace.ErrNotDestructible) {
		t.Fatalf("Destroy on an adopted workspace = %v, want ErrNotDestructible", err)
	}
	// The refusal is worthless if it deleted anything on the way to refusing.
	if _, err := os.Lstat(filepath.Join(root, "thesis.md")); err != nil {
		t.Fatalf("the refusal destroyed the caller's file: %v", err)
	}
	// And the workspace is still usable, because a refusal is not a failure.
	if err := store.Close(); err != nil {
		t.Errorf("a refused Destroy left the store unusable: %v", err)
	}
}

// TestAdoptedWorkspaceSurvivesReopening pins the ownership decision across
// opens: re-opening an adopted directory must not promote it to something
// Destroy will remove.
func TestAdoptedWorkspaceSurvivesReopening(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for attempt := range 3 {
		store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
		if err != nil {
			t.Fatalf("open %d: %v", attempt, err)
		}
		if err := store.Destroy(); !errors.Is(err, workspace.ErrNotDestructible) {
			t.Fatalf("open %d: Destroy = %v, want ErrNotDestructible", attempt, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("open %d: close: %v", attempt, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "keep.txt")); err != nil {
		t.Errorf("the caller's file is gone: %v", err)
	}
}

// TestCreatedWorkspaceStaysDestructibleAcrossReopening is the other half:
// ownership must not decay, or a workspace stow created would quietly become
// undeletable after a restart.
func TestCreatedWorkspaceStaysDestructibleAcrossReopening(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	first, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := second.Destroy(); err != nil {
		t.Errorf("a workspace stow created became undeletable after a reopen: %v", err)
	}
}

// TestDestroyIsIdempotent covers the contract in docs/workspace-contract.md
// section 1.4: destroy succeeds on an already-destroyed workspace, because a
// workspace that is gone is in the state the caller asked for.
//
// It is worth being explicit that this is a *success*, not a refusal. The
// tempting implementation reports "there is nothing there" as an error, which
// makes a retry after a partial failure impossible — and a caller that cannot
// retry a delete cannot clean up reliably.
func TestDestroyIsIdempotent(t *testing.T) {
	store, root := newOwnedStore(t)
	if err := store.Destroy(); err != nil {
		t.Fatalf("first Destroy: %v", err)
	}
	if err := store.Destroy(); err != nil {
		t.Errorf("second Destroy on an absent workspace = %v, want success", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory reappeared: %v", err)
	}
}

// TestDestroyRefusalExplainsItself keeps the error actionable. A bare sentinel
// tells a caller only that something was refused, not what to do instead.
func TestDestroyRefusalExplainsItself(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	err = store.Destroy()
	if err == nil {
		t.Fatal("expected a refusal for an adopted workspace")
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("the refusal does not name the path: %v", err)
	}
	if !strings.Contains(err.Error(), "yourself") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// newOwnedStore opens a workspace whose root stow actually created.
//
// It exists because t.TempDir() returns a directory that already exists, which
// makes it an *adopted* workspace — correct behaviour, and the wrong fixture
// for anything about Destroy's happy path. The distinction is the whole point
// of the ownership marker, so it has to be exercised deliberately rather than
// inherited from a helper written for something else.
func newOwnedStore(t *testing.T) (*workspace.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new workspace store: %v", err)
	}
	return store, root
}
