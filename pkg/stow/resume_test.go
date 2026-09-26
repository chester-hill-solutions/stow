package stow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	stow "github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

// W4: a workspace survives its process, resumes by ID, and is collected on a
// TTL without ever touching a live one. The refusals are the substance here; a
// collector's value is almost entirely in what it declines to delete.

func registryDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "registry")
}

// TestResumeReturnsTheSameWorkspace is the property that justifies the whole
// registry: an agent that was preempted comes back and finds its bytes.
func TestResumeReturnsTheSameWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	ctx := context.Background()

	first, err := stow.OpenWorkspace(stow.WorkspaceOptions{Dir: dir, RegistryDir: reg})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if _, err := first.PutObject(ctx, first.Bucket(), "output/report.txt", []byte("across the resume"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	id := first.ID()
	// Close releases the handle. The bytes stay, which is the entire point of
	// the close/destroy split.
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := stow.ResumeIn(reg, id)
	if err != nil {
		t.Fatalf("ResumeIn: %v", err)
	}
	defer second.Close()
	if second.ID() != id {
		t.Errorf("resumed id = %q, want %q", second.ID(), id)
	}
	object, err := second.GetObject(ctx, second.Bucket(), "output/report.txt")
	if err != nil {
		t.Fatalf("GetObject after resume: %v", err)
	}
	if string(object.Data) != "across the resume" {
		t.Errorf("after resume = %q, want the bytes", object.Data)
	}
}

// TestResumeRejectsAnUnknownID keeps a typo from opening the wrong thing.
func TestResumeRejectsAnUnknownID(t *testing.T) {
	if _, err := stow.ResumeIn(registryDir(t), "ws_does_not_exist"); err == nil {
		t.Error("Resume accepted an unknown id")
	}
}

// TestCollectRemovesAnExpiredUnusedWorkspace is the sweep doing its job.
func TestCollectRemovesAnExpiredUnusedWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	ctx := context.Background()

	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
		Dir: filepath.Join(dir, "workspace"), RegistryDir: reg, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if _, err := ws.PutObject(ctx, ws.Bucket(), "k.txt", []byte("v"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	root := ws.Dir()
	id := ws.ID()
	// Closing is what makes it collectable: the session lock is released.
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	future := time.Now().Add(30 * 24 * time.Hour)
	results, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: func() time.Time { return future }})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("reported %d results, want 1: %+v", len(results), results)
	}
	if results[0].ID != id || !results[0].Removed || results[0].Reason != "expired" {
		t.Errorf("result = %+v, want the workspace removed as expired", results[0])
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("an expired workspace survived collection: %v", err)
	}
}

// TestCollectNeverRemovesALiveWorkspace is the invariant the feature exists to
// hold. A workspace is a working directory; deleting one under a running agent
// destroys the artifact it is producing.
func TestCollectNeverRemovesALiveWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	ctx := context.Background()

	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
		Dir: filepath.Join(dir, "workspace"), RegistryDir: reg, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	defer ws.Close()
	if _, err := ws.PutObject(ctx, ws.Bucket(), "output/report.txt", []byte("in progress"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	future := time.Now().Add(30 * 24 * time.Hour)
	results, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: func() time.Time { return future }})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("reported %d results, want 1: %+v", len(results), results)
	}
	if results[0].Removed || results[0].Reason != "in-use" {
		t.Errorf("result = %+v, want it skipped as in-use", results[0])
	}
	if _, err := os.Lstat(filepath.Join(ws.Dir(), "output", "report.txt")); err != nil {
		t.Errorf("collection removed a live workspace: %v", err)
	}
}

// TestCollectNeverRemovesAnAdoptedWorkspace is the one that would be
// catastrophic rather than merely wrong: an adopted workspace is somebody's
// project, and no unattended sweep may delete one.
func TestCollectNeverRemovesAnAdoptedWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	if err := os.WriteFile(filepath.Join(dir, "thesis.md"), []byte("a year of work"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{Dir: dir, RegistryDir: reg, TTL: time.Hour})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	future := time.Now().Add(30 * 24 * time.Hour)
	results, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: func() time.Time { return future }})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 || results[0].Removed || results[0].Reason != "adopted" {
		t.Errorf("results = %+v, want it skipped as adopted", results)
	}
	if _, err := os.Lstat(filepath.Join(dir, "thesis.md")); err != nil {
		t.Fatalf("collection removed a caller's project: %v", err)
	}
}

// TestCollectKeepsAnUnexpiredWorkspace covers the ordinary case: nothing is old
// enough, so nothing happens.
func TestCollectKeepsAnUnexpiredWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
		Dir: filepath.Join(dir, "workspace"), RegistryDir: reg, TTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	results, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: time.Now})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 1 || results[0].Removed {
		t.Errorf("results = %+v, want an unexpired workspace kept", results)
	}
}

// TestDestroyForgetsTheRegistryEntry keeps the two halves converging: explicit
// destruction must leave nothing behind for a later sweep to trip over.
func TestDestroyForgetsTheRegistryEntry(t *testing.T) {
	dir := t.TempDir()
	reg := registryDir(t)
	ctx := context.Background()
	ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
		Dir: filepath.Join(dir, "workspace"), RegistryDir: reg, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if _, err := ws.PutObject(ctx, ws.Bucket(), "k.txt", []byte("v"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := ws.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	results, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: time.Now})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("a destroyed workspace is still registered: %+v", results)
	}
	if _, err := stow.ResumeIn(reg, ws.ID()); err == nil {
		t.Error("a destroyed workspace can still be resumed")
	}
}

// TestTouchMovesTheWindow is what a TTL is for: scratch nobody returns to is
// collected, and an agent that keeps coming back is not scratch.
//
// Time is simulated and every moment below is absolute against a fixed base. An
// earlier version offset from a mutable clock and every assertion drifted with
// it, which is one way a correct TTL gets made to look wrong.
//
// Touch postpones and does not exempt: a workspace that is touched and then left
// alone still ages out. That second half matters, because an implementation
// which "protects" anything ever touched would leak every workspace an agent
// opened once.
func TestTouchMovesTheWindow(t *testing.T) {
	const ttl = time.Hour
	reg := registryDir(t)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) func() time.Time {
		moment := base.Add(d)
		return func() time.Time { return moment }
	}
	run := func(d time.Duration) []stow.CollectResult {
		t.Helper()
		out, err := stow.Collect(stow.CollectOptions{RegistryDir: reg, Now: at(d)})
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		return out
	}
	find := func(results []stow.CollectResult, id string) stow.CollectResult {
		t.Helper()
		for _, r := range results {
			if r.ID == id {
				return r
			}
		}
		t.Fatalf("no result for %s in %+v", id, results)
		return stow.CollectResult{}
	}
	openAt := func(name string, when time.Duration) *stow.Workspace {
		t.Helper()
		ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
			Dir: filepath.Join(t.TempDir(), name), RegistryDir: reg, TTL: ttl, Now: at(when),
		})
		if err != nil {
			t.Fatalf("OpenWorkspace %s: %v", name, err)
		}
		return ws
	}

	idle := openAt("idle", 0)
	active := openAt("active", 0)
	idleID, activeID := idle.ID(), active.ID()
	if err := idle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := active.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Used two hours in, which pushes its window out to three.
	resumed, err := stow.ResumeWith(stow.WorkspaceOptions{RegistryDir: reg, Now: at(2 * time.Hour)}, activeID)
	if err != nil {
		t.Fatalf("ResumeWith: %v", err)
	}
	if err := resumed.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Two and a half hours in: the idle one is well past its window, the
	// touched one is well inside it.
	results := run(150 * time.Minute)
	if r := find(results, idleID); !r.Removed {
		t.Errorf("an idle workspace past its TTL survived: %+v", r)
	}
	if r := find(results, activeID); r.Removed {
		t.Errorf("a workspace used two hours ago was collected: %+v", r)
	}

	// Four hours in, the touched workspace has itself aged out. Touch postpones;
	// it does not exempt.
	results = run(4 * time.Hour)
	if r := find(results, activeID); !r.Removed {
		t.Errorf("a touched workspace was never collected: %+v", r)
	}
}
