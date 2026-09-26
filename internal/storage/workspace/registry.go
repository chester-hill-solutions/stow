package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// registryVersion is the only on-disk layout this build implements. A file
// written by a newer revision is refused rather than downgraded, for the same
// reason the manifest is: a registry that silently drops entries forgets
// workspaces, and a registry that forgets a workspace leaks it forever.
const registryVersion = 1

// Entry is the durable record of one workspace, named by its session ID.
//
// This is the thing that makes a workspace outlive the process that created it.
// ADR 0009 section 1 requires a resume to be "opening by this ID", not
// reconstructing state from an environment mapping, and that is only possible if
// the directory and the bucket survive independently of any handle.
type Entry struct {
	ID         string    `json:"id"`
	Dir        string    `json:"dir"`
	Bucket     string    `json:"bucket"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"last_used"`
	TTLSeconds int64     `json:"ttl_seconds"`
	// Owned mirrors the workspace manifest. The collector uses it to refuse
	// adopted workspaces outright, which is the single most important safety
	// property in this file: an adopted workspace is somebody's project, and no
	// unattended sweep may remove it.
	Owned bool `json:"owned"`
}

// Registry is the set of workspaces on this machine, on disk.
type Registry struct {
	dir string
}

// DefaultRegistryDir is where the registry lives: the user's configuration
// directory, under the package's own name. It holds small metadata only, never
// object data, and it is not inside any workspace — a registry stored in a
// workspace would be collectable by its own sweep.
func DefaultRegistryDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("workspace: locate config directory: %w", err)
	}
	return filepath.Join(base, "stow-s3", "workspaces"), nil
}

// OpenRegistry opens the registry at dir, creating it if absent.
func OpenRegistry(dir string) (*Registry, error) {
	if dir == "" {
		return nil, fmt.Errorf("workspace: registry directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: create registry: %w", err)
	}
	return &Registry{dir: dir}, nil
}

func (r *Registry) entryPath(id string) string {
	return filepath.Join(r.dir, id+".json")
}

// Register records a workspace and returns its entry.
func (r *Registry) Register(entry Entry) error {
	if entry.ID == "" {
		return fmt.Errorf("workspace: registry entry needs an id")
	}
	if entry.Created.IsZero() {
		entry.Created = time.Now().UTC()
	}
	if entry.LastUsed.IsZero() {
		entry.LastUsed = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encode registry entry: %w", err)
	}
	if err := writeFileAtomic(r.entryPath(entry.ID), append(raw, '\n')); err != nil {
		return fmt.Errorf("workspace: record registry entry: %w", err)
	}
	return nil
}

// Lookup returns the entry for a session ID.
func (r *Registry) Lookup(id string) (Entry, bool, error) {
	raw, err := os.ReadFile(r.entryPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("workspace: read registry entry: %w", err)
	}
	entry, err := decodeEntry(raw)
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

// Forget removes a session's record. It is what makes collection and an explicit
// Destroy converge on the same end state: the bytes go, and so does the name
// they were filed under.
func (r *Registry) Forget(id string) error {
	if err := os.Remove(r.entryPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: forget registry entry: %w", err)
	}
	return nil
}

// All returns every recorded workspace, ordered by ID so a sweep is
// deterministic.
func (r *Registry) All() ([]Entry, error) {
	names, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: list registry: %w", err)
	}
	var entries []Entry
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.dir, name.Name()))
		if err != nil {
			continue
		}
		entry, err := decodeEntry(raw)
		if err != nil {
			// A single unreadable entry must not stop the sweep, or one bad file
			// would make every workspace permanent.
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries, nil
}

func decodeEntry(raw []byte) (Entry, error) {
	entry := Entry{}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return Entry{}, fmt.Errorf("workspace: decode registry entry: %w", err)
	}
	if entry.ID == "" {
		return Entry{}, fmt.Errorf("workspace: registry entry has no id")
	}
	return entry, nil
}

// Reclaim is what a sweep did, and why.
type Reclaim struct {
	Entry Entry
	// Reason is one of "expired", "adopted", or "in-use", and is what a caller
	// reports to a user. "in-use" and "adopted" are the two that mean the
	// collector declined to act.
	Reason string
}

// Collect removes workspaces that are past their TTL and provably unused.
//
// The order of the checks is the design. Liveness is established before
// anything is considered for deletion, so a workspace that is merely *old* is
// never removed while somebody is in it. Ownership is checked next, so no sweep
// can ever remove a directory a caller pointed stow at. Only then is age
// consulted, and only for workspaces this machine created.
//
// It returns what it declined as well as what it removed, because "nothing was
// collected" and "three workspaces were skipped because they are in use" are
// different answers and a caller diagnosing a leak needs to tell them apart.
func (r *Registry) Collect(now time.Time) ([]Reclaim, error) {
	if !LockSupported() {
		return nil, fmt.Errorf("workspace: cannot collect on this host: %w", ErrLockUnsupported)
	}
	entries, err := r.All()
	if err != nil {
		return nil, err
	}

	var out []Reclaim
	for _, entry := range entries {
		reason, act := r.classify(entry, now)
		out = append(out, Reclaim{Entry: entry, Reason: reason})
		if !act {
			continue
		}
		if err := reclaimWorkspace(entry); err != nil {
			// A workspace that cannot be opened right now — a permission
			// problem, a path that has gone — is recorded and skipped, not
			// fatal. One bad entry must not make the rest permanent.
			out[len(out)-1].Reason = "unreadable: " + err.Error()
			continue
		}
		if err := r.Forget(entry.ID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// classify decides whether one workspace may be removed, and says why.
func (r *Registry) classify(entry Entry, now time.Time) (reason string, act bool) {
	live, err := ProbeLiveness(entry.Dir)
	if err != nil {
		return "unreadable: " + err.Error(), false
	}
	if live {
		return "in-use", false
	}
	// Ownership before age. An adopted workspace is a developer's project, and a
	// sweep that could remove one would be a data-loss bug reachable by waiting.
	if !entry.Owned {
		return "adopted", false
	}
	if entry.TTLSeconds <= 0 {
		return "no-ttl", false
	}
	if now.Sub(entry.LastUsed) < time.Duration(entry.TTLSeconds)*time.Second {
		return "not-expired", false
	}
	return "expired", true
}

// reclaimWorkspace opens a workspace and destroys it, which routes the removal
// through the same ownership and protected-path checks a caller gets. The
// collector has no privilege of its own.
func reclaimWorkspace(entry Entry) error {
	store, err := New(Options{Root: entry.Dir, Bucket: entry.Bucket})
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Destroy()
}
