package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

type Store struct {
	root      string
	layout    Layout
	manifest  *Manifest
	workspace string

	// folded maps a case-folded workspace-relative path, qualified by bucket,
	// to the key that owns it. It is the collision defence from
	// docs/workspace-contract.md section 3.4: on a case-insensitive filesystem
	// two keys differing only in case are one file, and serving both from it
	// loses one silently.
	folded map[string]string

	// Now is the store's clock, replaceable in tests.
	Now func() time.Time

	mu     sync.RWMutex
	closed bool
}

var _ storage.Store = (*Store)(nil)

// Options configures a workspace store.
type Options struct {
	// Root is the workspace directory. It is created if absent, and adopted
	// with whatever it already holds if not.
	Root string
	// Bucket is the workspace's own bucket, whose objects are the root
	// directory itself. Any other bucket is namespaced under the reserved
	// internal directory so that a caller who genuinely wants a second bucket
	// gets bucket isolation without the workspace bucket ceasing to be the
	// working directory.
	Bucket string
	// TTLSeconds records the intended collection window. The collector itself
	// is ADR 0009 work; the value is stored so a future collector and any
	// operator reading the manifest can see what was asked for.
	TTLSeconds int64
	// Now is injectable for tests.
	Now func() time.Time
}

// New opens a workspace store rooted at options.Root.

func New(options Options) (*Store, error) {
	if options.Root == "" {
		return nil, fmt.Errorf("workspace store: Root is required")
	}
	if options.Bucket != "" {
		if err := storage.ValidateBucketName(options.Bucket); err != nil {
			return nil, fmt.Errorf("workspace store: bucket %q: %w", options.Bucket, err)
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(options.Root, 0o755); err != nil {
		return nil, fmt.Errorf("workspace store: create root: %w", err)
	}
	if err := os.MkdirAll(InternalPath(options.Root, "keys"), 0o755); err != nil {
		return nil, fmt.Errorf("workspace store: create internal directory: %w", err)
	}
	manifest, err := loadManifest(options.Root)
	if err != nil {
		return nil, err
	}
	store := &Store{
		root:      options.Root,
		layout:    NewLayout(),
		manifest:  manifest,
		workspace: options.Bucket,
		folded:    map[string]string{},
		Now:       now,
	}
	if store.manifest.WorkspaceID == "" {
		id, err := newWorkspaceID()
		if err != nil {
			return nil, err
		}
		store.manifest.WorkspaceID = id
	}
	if store.manifest.Bucket == "" {
		store.manifest.Bucket = options.Bucket
	}
	if store.manifest.TTLSeconds == 0 {
		store.manifest.TTLSeconds = options.TTLSeconds
	}
	store.manifest.Created = store.manifest.Created.UTC()
	if store.manifest.Created.IsZero() {
		store.manifest.Created = now().UTC()
	}
	if err := store.index(); err != nil {
		return nil, err
	}
	return store, store.manifest.save()
}

// Now is the store's clock, replaceable in tests.

func newWorkspaceID() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("workspace store: mint id: %w", err)
	}
	return "ws_" + hex.EncodeToString(id[:]), nil
}

// Root is the absolute workspace directory.
func (s *Store) Root() string { return s.root }

// WorkspaceBucket is the bucket whose objects are the workspace directory.
func (s *Store) WorkspaceBucket() string { return s.manifest.Bucket }

// ID is the workspace's durable identity.
func (s *Store) ID() string { return s.manifest.WorkspaceID }

// Path returns where a key's bytes live on disk, and whether they are there.
//
// It exists so a host can hand a caller a real path without an S3 round trip:
// "the report is at /work/output/report.pdf" is the sentence a workspace is
// for. The bool is false when the key is absent, which is the same answer
// GetObject gives, reached without reading a byte.
func (s *Store) Path(bucket, key string) (string, bool) {
	absPath, err := s.locate(bucket, key)
	if err != nil {
		return "", false
	}
	return absPath, true
}

// bucketDir returns the directory a bucket's objects live in. The workspace
// bucket is the root itself; anything else is namespaced under the internal
// directory, which is what lets a caller have a second bucket without the
// workspace bucket ceasing to be the working directory.

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.manifest.LastUsed = s.now().UTC()
	return s.manifest.save()
}

func (s *Store) checkOpen() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// metaFromEntry turns a manifest entry plus a file's stat into object metadata.
// A file with no entry is still an object: size and modification time come from
// the filesystem, and the ETag is the content hash.
