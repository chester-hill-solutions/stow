package stow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// Workspace is a bounded artifact workspace: a real directory a caller works
// in, and a bucket over the same bytes. It is the default surface described in
// ADR 0007, and its contract is docs/workspace-contract.md.
//
// The embedded *Runtime supplies the object operations, so quotas and
// accounting have exactly one choke point and a workspace is not a second
// implementation of PutObject.
//
// Closing a workspace releases the handle. It does not delete anything: a
// workspace outlives the process that opened it (ADR 0009), and removal is an
// explicit Destroy.
type Workspace struct {
	*Runtime

	dir       string
	bucket    string
	id        string
	store     *workspace.Store
	session   *workspace.Session
	registry  *workspace.Registry
	now       func() time.Time
	closed    bool
	destroyed bool
}

// WorkspaceOptions configures a workspace.
//
// Dir is the only required field, and it is required on purpose: a workspace is
// a place, not a container of opaque bytes.
type WorkspaceOptions struct {
	// Dir is the workspace directory. It is created if absent, and adopted with
	// whatever it already holds if not, so pointing a workspace at a directory a
	// caller is already working in is the normal case rather than a special one.
	Dir string
	// Bucket overrides the generated workspace bucket name. The bucket's objects
	// are the directory itself, so a key like output/report.pdf is at
	// Dir/output/report.pdf.
	Bucket string
	// MaxBytes and MaxObjects bound the workspace. Zero takes the runtime
	// defaults, so a workspace is bounded unless a caller deliberately lifts
	// the bound.
	MaxBytes   int64
	MaxObjects int64
	// TTL is the collection window the workspace records for itself. A later
	// collector reclaims a workspace whose session is gone; until that exists
	// the value is recorded in the workspace manifest and honoured by nothing,
	// which is why it is documented as advisory here rather than promised.
	TTL time.Duration
	// RegistryDir places the machine's workspace registry. Empty takes the
	// default under the user's configuration directory. Tests set it so they
	// never touch a real one.
	RegistryDir string
	// Now is injectable for tests.
	Now func() time.Time
}

// OpenWorkspace opens a workspace rooted at options.Dir.
//
// It starts no process, opens no listener, and reads no ambient environment, so
// there is nothing to clean up on a crash beyond the caller's own exit and
// nothing for a host to leak credentials for. Where a client cannot embed the
// runtime, the scoped S3 session in the TypeScript and Python packages remains
// the supported alternative; see ADR 0007 section 3 for why Python is the
// exception.
func OpenWorkspace(options WorkspaceOptions) (*Workspace, error) {
	if options.Dir == "" {
		return nil, fmt.Errorf("stow: workspace Dir is required")
	}
	bucket := options.Bucket
	if bucket == "" {
		bucket = generatedBucketName()
	}

	store, err := workspace.New(workspace.Options{
		Root:       options.Dir,
		Bucket:     bucket,
		TTLSeconds: int64(options.TTL.Seconds()),
		Now:        options.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("stow: open workspace: %w", err)
	}

	instance, err := runtime.OpenWithStore(runtime.Options{
		Backend:    runtime.BackendWorkspace,
		MaxBytes:   options.MaxBytes,
		MaxObjects: options.MaxObjects,
	}, store, nil)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("stow: open workspace: %w", err)
	}

	// A live session holds an advisory lock for as long as this handle exists.
	// It is what lets a collector establish that nobody is using the workspace
	// rather than guess, and the kernel releases it even if this process is
	// killed outright.
	session, err := workspace.AcquireSession(store.Root())
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("stow: claim workspace: %w", err)
	}

	ws := &Workspace{
		Runtime: &Runtime{inner: instance},
		dir:     store.Root(),
		bucket:  store.WorkspaceBucket(),
		id:      store.ID(),
		store:   store,
		session: session,
	}
	ws.now = options.Now
	if err := ws.Runtime.CreateBucket(context.Background(), ws.bucket); err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("stow: create workspace bucket: %w", err)
	}
	if err := ws.register(options.RegistryDir, int64(options.TTL.Seconds())); err != nil {
		_ = ws.Close()
		return nil, err
	}
	return ws, nil
}

// Dir is the absolute workspace directory, stable for the workspace's life.
func (w *Workspace) Dir() string { return w.dir }

// Bucket is the bucket over the workspace's bytes. Its objects are the directory
// itself, which is what makes a local file and an s3:// key the same bytes.
func (w *Workspace) Bucket() string { return w.bucket }

// ID is the workspace's durable identity, which outlives this handle.
func (w *Workspace) ID() string { return w.id }

// Path returns where a key's bytes live, and whether they are there. It answers
// without an S3 round trip, so a host can print a real path for a caller.
func (w *Workspace) Path(key string) (string, bool) {
	return w.store.Path(w.bucket, key)
}

// Close releases the handle. It is the non-destructive half of the split in
// ADR 0009 section 3: the process and the client go away, and the bytes do not.
//
// It also releases the session lock, which is what makes the workspace
// collectable again. Closing twice is not an error.
func (w *Workspace) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	var firstErr error
	if err := w.session.Release(); err != nil {
		firstErr = err
	}
	if err := w.Runtime.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// assertOpen refuses work on a closed handle, so a lifecycle mistake is a named
// error rather than a confusing failure from underneath.
func (w *Workspace) assertOpen() error {
	if w.closed {
		return ErrClosed
	}
	return nil
}

// Destroy removes the workspace directory, and only if stow created it.
//
// This is the explicit half of the lifecycle split in ADR 0009 section 3:
// Close releases the handle, Destroy removes the bytes. A workspace stow
// *adopted* — one pointed at a directory the caller already had, which is the
// documented way to use one — is refused, because its contents are the caller's
// and not stow's to delete. A refusal leaves the workspace intact and usable.
//
// A workspace that is already gone is not an error: destroy is idempotent.
func (w *Workspace) Destroy(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.store.Destroy(); err != nil {
		return err
	}
	w.destroyed = true
	// The bytes are gone, so the name they were filed under must go too, or the
	// registry accumulates entries that resolve to nothing.
	if w.registry != nil {
		return w.registry.Forget(w.id)
	}
	return nil
}

// generatedBucketName returns a bucket name unlikely to collide with another
// workspace's. A caller that needs a stable name across reopens passes Bucket
// explicitly or reads it back from an existing workspace.
func generatedBucketName() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// A bucket name that cannot be randomised is still a valid bucket name;
		// the collision risk is far below anything that matters here.
		return "stow-workspace"
	}
	return "stow-workspace-" + hex.EncodeToString(suffix[:])
}
