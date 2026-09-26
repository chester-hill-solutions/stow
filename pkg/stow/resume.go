package stow

import (
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// Resume reopens a workspace by the identity it was given when it was created.
//
// This is the whole of ADR 0009 section 1: a workspace outlives the process that
// made it, and reopening is "open by this ID" rather than reconstructing state
// from an environment mapping. An agent that was preempted comes back, asks the
// registry where its work lives, and carries on with the same bytes.
func Resume(id string) (*Workspace, error) {
	return resumeWith(WorkspaceOptions{}, id)
}

// ResumeIn is Resume against a chosen registry. It exists so a caller with its
// own registry location — a host that keeps workspaces somewhere deliberate —
// does not have to adopt the user's configuration directory.
func ResumeIn(registryDir, id string) (*Workspace, error) {
	return ResumeWith(WorkspaceOptions{RegistryDir: registryDir}, id)
}

// ResumeWith is Resume with options, chiefly so the registry location and the
// clock can be chosen. The clock matters more than it looks: a TTL is the one
// behaviour here that cannot be tested by sleeping, so it is tested by
// simulating time instead.
func ResumeWith(options WorkspaceOptions, id string) (*Workspace, error) {
	return resumeWith(options, id)
}

// resumeWith is Resume with options.
func resumeWith(options WorkspaceOptions, id string) (*Workspace, error) {
	registry, err := openRegistry(options.RegistryDir)
	if err != nil {
		return nil, err
	}
	entry, found, err := registry.Lookup(id)
	if err != nil {
		return nil, fmt.Errorf("stow: look up workspace %s: %w", id, err)
	}
	if !found {
		return nil, fmt.Errorf("stow: no workspace with id %s", id)
	}
	ws, err := OpenWorkspace(WorkspaceOptions{
		Dir:         entry.Dir,
		Bucket:      entry.Bucket,
		TTL:         time.Duration(entry.TTLSeconds) * time.Second,
		RegistryDir: options.RegistryDir,
		Now:         options.Now,
	})
	if err != nil {
		return nil, err
	}
	if ws.ID() != id {
		// The directory is authoritative, so this means the registry and the
		// workspace disagree about who they are. Carrying on would silently
		// hand back a different workspace than the one that was asked for.
		_ = ws.Close()
		return nil, fmt.Errorf("stow: workspace %s does not match the workspace registered at %s", id, entry.Dir)
	}
	return ws, nil
}

// CollectResult is one workspace a sweep considered, and what it did about it.
type CollectResult struct {
	ID string
	// Dir is the workspace directory, whether or not it survived.
	Dir string
	// Removed is true only when the bytes are gone.
	Removed bool
	// Reason explains a decision. "expired" means it was collected; "in-use",
	// "adopted", "not-expired" and "no-ttl" all mean the sweep declined, and
	// "unreadable: …" means it could not be opened and was left alone.
	Reason string
}

// Collect reclaims workspaces that are past their TTL and provably unused.
//
// The two refusals are the point. A workspace a live session holds is never
// removed, because it is a working directory and deleting it destroys the
// artifact in progress. A workspace stow *adopted* is never removed, because it
// is a caller's own project and no unattended sweep may delete one — that check
// comes before the age check for exactly that reason.
//
// It reports every decision, not only the removals, because "nothing was
// collected" and "three were skipped because they are in use" are different
// answers and a caller diagnosing a leaked workspace has to tell them apart.
//
// Collect is a no-op with an explanatory error on a host that cannot establish
// liveness, rather than guessing.
func Collect(options CollectOptions) ([]CollectResult, error) {
	registry, err := openRegistry(options.RegistryDir)
	if err != nil {
		return nil, err
	}
	if !workspace.LockSupported() {
		return nil, fmt.Errorf("stow: cannot collect on this host: %w", workspace.ErrLockUnsupported)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	results, err := registry.Collect(now())
	if err != nil {
		return nil, err
	}
	out := make([]CollectResult, 0, len(results))
	for _, result := range results {
		out = append(out, CollectResult{
			ID:      result.Entry.ID,
			Dir:     result.Entry.Dir,
			Removed: result.Reason == "expired",
			Reason:  result.Reason,
		})
	}
	return out, nil
}

// CollectOptions configures a sweep.
type CollectOptions struct {
	// RegistryDir places the machine's workspace registry. Empty takes the
	// default under the user's configuration directory.
	RegistryDir string
	// Now is the clock. Empty takes the wall clock; tests set it.
	Now func() time.Time
}

// openRegistry opens the workspace registry, creating it if absent.
func openRegistry(dir string) (*workspace.Registry, error) {
	if dir == "" {
		defaultDir, err := workspace.DefaultRegistryDir()
		if err != nil {
			return nil, err
		}
		dir = defaultDir
	}
	registry, err := workspace.OpenRegistry(dir)
	if err != nil {
		return nil, fmt.Errorf("stow: open workspace registry: %w", err)
	}
	return registry, nil
}

// register records a workspace so a later process can resume it. A failure to
// register is reported rather than swallowed: an unregistered workspace cannot
// be resumed, and the caller is the only one who can decide that is acceptable.
func (w *Workspace) register(registryDir string, ttlSeconds int64) error {
	registry, err := openRegistry(registryDir)
	if err != nil {
		return err
	}
	w.registry = registry
	now := w.nowFunc()()
	return registry.Register(workspace.Entry{
		ID:         w.id,
		Dir:        w.dir,
		Bucket:     w.bucket,
		Created:    now,
		LastUsed:   now,
		TTLSeconds: ttlSeconds,
		Owned:      w.store.IsOwned(),
	})
}

// Touch records that the workspace was used, which is what a TTL is measured
// from. A workspace an agent keeps returning to is not scratch.
func (w *Workspace) Touch() error {
	if w.registry == nil {
		return nil
	}
	if err := w.assertOpen(); err != nil {
		return err
	}
	entry, found, err := w.registry.Lookup(w.id)
	if err != nil || !found {
		return err
	}
	entry.LastUsed = w.nowFunc()()
	return w.registry.Register(entry)
}

func (w *Workspace) nowFunc() func() time.Time {
	if w.now != nil {
		return w.now
	}
	return time.Now
}
