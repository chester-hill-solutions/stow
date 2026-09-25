package workspace_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// These cases are almost entirely about refusals. A collector's job is to delete
// things, so the value of testing it is in what it declines to do.

// registryIn builds a registry scoped to one test.
func registryIn(t *testing.T) *workspace.Registry {
	t.Helper()
	registry, err := workspace.OpenRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	return registry
}

// registerOwned creates a workspace stow owns, registers it, and returns its ID.
func registerOwned(t *testing.T, registry *workspace.Registry, ttl time.Duration) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket, TTLSeconds: int64(ttl.Seconds())})
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	id := "ws-test-" + filepath.Base(filepath.Dir(root))
	if err := registry.Register(workspace.Entry{
		ID: id, Dir: root, Bucket: bucket,
		TTLSeconds: int64(ttl.Seconds()), Owned: true,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return id, root
}

// expired is far enough past any TTL these tests use.
func expired() time.Time { return time.Now().Add(30 * 24 * time.Hour) }

func reasons(results []workspace.Reclaim) map[string]string {
	out := map[string]string{}
	for _, r := range results {
		out[r.Entry.ID] = r.Reason
	}
	return out
}

func TestCollectRemovesAnExpiredUnusedWorkspace(t *testing.T) {
	registry := registryIn(t)
	id, root := registerOwned(t, registry, time.Hour)

	results, err := registry.Collect(expired())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := reasons(results)[id]; got != "expired" {
		t.Errorf("reason = %q, want expired", got)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("an expired, unused workspace survived collection: %v", err)
	}
	if _, found, _ := registry.Lookup(id); found {
		t.Error("the registry still lists a workspace that was collected")
	}
}

// TestCollectNeverRemovesALiveWorkspace is the invariant the whole feature exists
// to hold. A workspace is a working directory, so collecting one out from under
// a running agent destroys the artifact it is producing.
func TestCollectNeverRemovesALiveWorkspace(t *testing.T) {
	registry := registryIn(t)
	root := filepath.Join(t.TempDir(), "workspace")
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket, TTLSeconds: 1})
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	defer store.Close()

	// A live session holds the workspace for as long as it is open.
	session, err := workspace.AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	defer session.Release()

	put(t, store, "output/report.txt", "an artifact in progress")
	if err := registry.Register(workspace.Entry{
		ID: "ws-live", Dir: root, Bucket: bucket, TTLSeconds: 1, Owned: true,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	results, err := registry.Collect(expired())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := reasons(results)["ws-live"]; got != "in-use" {
		t.Errorf("reason = %q, want in-use", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "output", "report.txt")); err != nil {
		t.Errorf("collection removed a live workspace: %v", err)
	}
}

// TestCollectNeverRemovesAnAdoptedWorkspace is the second invariant, and the one
// that would be catastrophic rather than merely wrong: an adopted workspace is
// somebody's project, and an unattended sweep must never be able to remove one.
func TestCollectNeverRemovesAnAdoptedWorkspace(t *testing.T) {
	registry := registryIn(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "thesis.md"), []byte("a year of work"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket, TTLSeconds: 1})
	if err != nil {
		t.Fatalf("open adopted workspace: %v", err)
	}
	defer store.Close()
	if err := registry.Register(workspace.Entry{
		ID: "ws-adopted", Dir: root, Bucket: bucket, TTLSeconds: 1, Owned: false,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	results, err := registry.Collect(expired())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := reasons(results)["ws-adopted"]; got != "adopted" {
		t.Errorf("reason = %q, want adopted", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "thesis.md")); err != nil {
		t.Fatalf("collection removed a caller's project: %v", err)
	}
	// And it stays registered, because it is still a workspace — just not one
	// this machine may delete.
	if _, found, _ := registry.Lookup("ws-adopted"); !found {
		t.Error("an adopted workspace was forgotten rather than skipped")
	}
}

// TestCollectKeepsUnexpiredWorkspaces covers the ordinary case: nothing is old
// enough, so nothing happens.
func TestCollectKeepsUnexpiredWorkspaces(t *testing.T) {
	registry := registryIn(t)
	id, root := registerOwned(t, registry, 24*time.Hour)

	results, err := registry.Collect(time.Now())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := reasons(results)[id]; got != "not-expired" {
		t.Errorf("reason = %q, want not-expired", got)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Errorf("an unexpired workspace was removed: %v", err)
	}
}

// TestCollectKeepsWorkspacesWithNoTTL pins that a workspace registered without
// a window is never collected. An absent TTL means "no opinion", and treating
// that as "expired" would delete on a default nobody chose.
func TestCollectKeepsWorkspacesWithNoTTL(t *testing.T) {
	registry := registryIn(t)
	root := filepath.Join(t.TempDir(), "workspace")
	store, err := workspace.New(workspace.Options{Root: root, Bucket: bucket})
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := registry.Register(workspace.Entry{
		ID: "ws-no-ttl", Dir: root, Bucket: bucket, TTLSeconds: 0, Owned: true,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	results, err := registry.Collect(expired())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := reasons(results)["ws-no-ttl"]; got != "no-ttl" {
		t.Errorf("reason = %q, want no-ttl", got)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Errorf("a workspace with no TTL was removed: %v", err)
	}
}

// TestCollectReportsEveryDecision matters for diagnosis: "nothing was collected"
// and "three were skipped because they are in use" are different answers, and a
// caller debugging a leak has to be able to tell them apart.
func TestCollectReportsEveryDecision(t *testing.T) {
	registry := registryIn(t)
	live := filepath.Join(t.TempDir(), "live")
	adopted := t.TempDir()
	dead, _ := registerOwned(t, registry, time.Hour)

	store, err := workspace.New(workspace.Options{Root: live, Bucket: bucket})
	if err != nil {
		t.Fatalf("new live workspace: %v", err)
	}
	defer store.Close()
	session, err := workspace.AcquireSession(live)
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	defer session.Release()
	for id, dir := range map[string]string{"ws-live": live, "ws-adopted": adopted} {
		owned := id == "ws-live"
		if err := registry.Register(workspace.Entry{
			ID: id, Dir: dir, Bucket: bucket, TTLSeconds: 1, Owned: owned,
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	results, err := registry.Collect(expired())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := reasons(results)
	for id, want := range map[string]string{
		dead: "expired", "ws-live": "in-use", "ws-adopted": "adopted",
	} {
		if got[id] != want {
			t.Errorf("%s: reason = %q, want %q", id, got[id], want)
		}
	}
	if len(results) != 3 {
		t.Errorf("reported %d decisions, want 3: %v", len(results), got)
	}
}

// TestLivenessIsReleasedWhenTheHolderExits is the property that makes an
// advisory lock the right primitive: there is no stale state to clean up, so a
// crashed session cannot make its workspace permanent.
func TestLivenessIsReleasedWhenTheHolderExits(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if _, err := workspace.New(workspace.Options{Root: root, Bucket: bucket}); err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	session, err := workspace.AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	if live, err := workspace.ProbeLiveness(root); err != nil || !live {
		t.Fatalf("a held session did not read as live (live=%v err=%v)", live, err)
	}
	if err := session.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if live, err := workspace.ProbeLiveness(root); err != nil || live {
		t.Fatalf("a released session still reads as live (live=%v err=%v)", live, err)
	}
}

// TestLivenessIsPerProcessAndConcurrent takes the lock from several goroutines
// at once. Exactly one may hold it, which is what makes "somebody is using
// this" a fact rather than a race.
func TestLivenessIsPerProcessAndConcurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if _, err := workspace.New(workspace.Options{Root: root, Bucket: bucket}); err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	first, err := workspace.AcquireSession(root)
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	defer first.Release()

	// A second acquisition while the first is held must be refused, not queued.
	if _, err := workspace.AcquireSession(root); err == nil {
		t.Error("a second session acquired a workspace that was already held")
	}

	// Probing from several goroutines must agree every time.
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live, err := workspace.ProbeLiveness(root)
			results[i] = err == nil && live
		}()
	}
	wg.Wait()
	for i, live := range results {
		if !live {
			t.Errorf("probe %d disagreed that a held workspace is live", i)
		}
	}
}
