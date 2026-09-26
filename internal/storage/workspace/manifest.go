package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// manifestVersion is the only on-disk manifest layout this build implements. A
// file written by a newer revision is refused rather than migrated on read: the
// outbox already established that rule for durable files, and a manifest that
// silently downgrades loses exactly the entries it could not understand.
const manifestVersion = 1

// ErrManifestCorrupt is returned when a manifest exists and cannot be trusted.
//
// It is a refusal, not a repair. The filesystem, not the manifest, is the source
// of truth for what exists, so an empty manifest would not lose bytes — but it
// would make every key resolve to absent while every file stayed on disk, which
// reads to the caller as total data loss. Refusing is the only safe answer.
var ErrManifestCorrupt = errors.New("workspace manifest is corrupt")

// Manifest is the single stow-workspace.json at a workspace root. It carries
// identity and metadata. It is not the source of truth for existence: a manifest
// entry pointing at a missing file serves as absent, and a file with no entry
// exists. Losing the manifest degrades metadata, never data.
type Manifest struct {
	Version     int                                 `json:"version"`
	WorkspaceID string                              `json:"workspace_id"`
	Bucket      string                              `json:"bucket"`
	Created     time.Time                           `json:"created"`
	LastUsed    time.Time                           `json:"last_used"`
	TTLSeconds  int64                               `json:"ttl_seconds"`
	Buckets     map[string]map[string]ManifestEntry `json:"buckets"`
	// Owned records whether stow created the workspace directory or adopted one
	// that already existed. It is the only thing that makes Destroy safe: a
	// workspace is *meant* to be pointed at a caller's existing directory, so the
	// presence of a manifest is not evidence that the contents are stow's to
	// delete. An adopted workspace can be read, written and closed; removing it
	// is the caller's business.
	Owned bool `json:"owned"`

	path string
}

// ManifestEntry records what stow knows about one object. A missing entry is
// legal and is the normal case for a file the host wrote.
type ManifestEntry struct {
	Form              Form              `json:"form"`
	Digest            string            `json:"digest,omitempty"`
	Size              int64             `json:"size"`
	ETag              string            `json:"etag,omitempty"`
	VersionID         string            `json:"version_id,omitempty"`
	ContentType       string            `json:"content_type,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	ChecksumAlgorithm string            `json:"checksum_algorithm,omitempty"`
	ChecksumValue     string            `json:"checksum_value,omitempty"`
	Modified          time.Time         `json:"modified"`
}

// manifestPath is where a workspace keeps its manifest.
func manifestPath(root string) string {
	return InternalPath(root, "manifest.json")
}

// loadManifest reads a workspace's manifest. A workspace with no manifest yet
// is not corrupt; it is new, and an empty one is returned so the caller can
// populate it. A manifest that exists and cannot be parsed, or that carries a
// version this build does not implement, is refused.
func loadManifest(root string) (*Manifest, error) {
	path := manifestPath(root)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newManifest(root), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace manifest: %w", err)
	}

	manifest := &Manifest{}
	if err := json.Unmarshal(raw, manifest); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestCorrupt, err)
	}
	if manifest.Version != manifestVersion {
		return nil, fmt.Errorf("%w: unsupported version %d, this build implements %d",
			ErrManifestCorrupt, manifest.Version, manifestVersion)
	}
	if manifest.Buckets == nil {
		manifest.Buckets = map[string]map[string]ManifestEntry{}
	}
	manifest.path = path
	return manifest, nil
}

// newManifest returns an empty manifest for a workspace that does not have one.
func newManifest(root string) *Manifest {
	return &Manifest{
		Version: manifestVersion,
		Buckets: map[string]map[string]ManifestEntry{},
		path:    manifestPath(root),
	}
}

// save writes the manifest atomically: a temporary file in the same directory,
// then a rename. A manifest is therefore never observed half-written, and a
// crash mid-save leaves the previous version intact rather than a truncated one.
func (m *Manifest) save() error {
	if m.Buckets == nil {
		m.Buckets = map[string]map[string]ManifestEntry{}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace manifest: %w", err)
	}
	return writeFileAtomic(m.path, append(raw, '\n'))
}

// writeFileAtomic writes through a temporary file in the destination directory
// and renames it into place.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

// entry returns what the manifest knows about one key, if anything.
func (m *Manifest) entry(bucket, key string) (ManifestEntry, bool) {
	keys, ok := m.Buckets[bucket]
	if !ok {
		return ManifestEntry{}, false
	}
	entry, ok := keys[key]
	return entry, ok
}

// setEntry records what stow knows about one key.
func (m *Manifest) setEntry(bucket, key string, entry ManifestEntry) {
	keys, ok := m.Buckets[bucket]
	if !ok {
		keys = map[string]ManifestEntry{}
		m.Buckets[bucket] = keys
	}
	keys[key] = entry
}

// removeEntry forgets a key. The bytes are removed separately; this only stops
// stow claiming to know about something that is gone.
func (m *Manifest) removeEntry(bucket, key string) {
	keys, ok := m.Buckets[bucket]
	if !ok {
		return
	}
	delete(keys, key)
}

// stale reports whether a recorded entry no longer describes the file on disk,
// which is what an agent overwriting a file behind stow's back looks like.
//
// Size and modification time are compared rather than the ETag because checking
// the ETag would mean reading every file on every head.
func (e ManifestEntry) stale(size int64, modified time.Time) bool {
	if e.Size != size {
		return true
	}
	return !e.Modified.Equal(modified.UTC().Truncate(time.Second))
}
