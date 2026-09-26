package runtime

import (
	"context"
	"sync"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

type Instance struct {
	mu               sync.Mutex
	store            storage.Store
	multipartStore   storage.MultipartStore
	resetStore       func() (storage.Store, error)
	options          Options
	authority        authority.Authority
	usage            Usage
	multipart        map[string]multipartUsage
	multipartTargets map[string]int
	reservedTargets  map[string]struct{}
	reservedObjects  int64
	reservedBytes    int64
	persistent       bool
	closed           bool
	closeOnce        sync.Once
	closeErr         error
}

type multipartUsage struct {
	upload storage.MultipartUpload
	parts  map[int]int64
}

func Open(options Options) (*Instance, error) {
	normalized, err := normalizeOptions(options, false)
	if err != nil {
		return nil, err
	}
	return newInstance(normalized, storage.NewMemoryStore(), func() (storage.Store, error) {
		return storage.NewMemoryStore(), nil
	}, false), nil
}

func newInstance(options Options, store storage.Store, resetStore func() (storage.Store, error), persistent bool) *Instance {
	granted := authority.All()
	if options.Authority != nil {
		granted = *options.Authority
	}
	multipart, _ := store.(storage.MultipartStore)
	return &Instance{
		store:            store,
		multipartStore:   multipart,
		resetStore:       resetStore,
		options:          options,
		authority:        granted,
		multipart:        make(map[string]multipartUsage),
		multipartTargets: make(map[string]int),
		reservedTargets:  make(map[string]struct{}),
		persistent:       persistent,
	}
}

// check is the single authorization point. Every public operation calls it
// before it touches anything, which is what makes the answer independent of
// which interface asked.
//
// Close is deliberately not routed through here. Refusing to release a handle
// would leak the process, the temporary directory and the in-flight multipart
// state, so disposal is always permitted; EnvironmentDestroy is the operation
// for tearing an environment down on purpose, and that is gated.
func (i *Instance) check(op authority.Operation) error {
	return i.authority.Check(op)
}

// Authority reports what this environment permits, so an interface can narrow
// its own behaviour to match rather than deciding separately.
func (i *Instance) Authority() authority.Authority {
	return i.authority
}

func (i *Instance) checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (i *Instance) checkOpen() error {
	if i.closed {
		return ErrClosed
	}
	return nil
}

func (i *Instance) capabilitiesLocked() Capabilities {
	return Capabilities{
		Backend:    i.options.Backend,
		MaxBytes:   i.options.MaxBytes,
		MaxObjects: i.options.MaxObjects,
		Persistent: i.persistent,
		Multipart:  i.multipartStore != nil,
	}
}

func (i *Instance) Capabilities() Capabilities {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.capabilitiesLocked()
}

func (i *Instance) Usage() Usage {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.usage
}

func (i *Instance) Reset(ctx context.Context) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	if err := i.check(authority.EnvironmentReset); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	if i.resetStore == nil {
		return ErrExternalResetUnsupported
	}
	next, err := i.resetStore()
	if err != nil {
		return err
	}
	if err := i.store.Close(); err != nil {
		_ = next.Close()
		return err
	}
	i.store = next
	// Re-derived: the store just closed is the one multipartStore pointed at.
	i.multipartStore, _ = next.(storage.MultipartStore)
	i.usage = Usage{}
	i.multipart = make(map[string]multipartUsage)
	i.multipartTargets = make(map[string]int)
	i.reservedTargets = make(map[string]struct{})
	i.reservedObjects = 0
	i.reservedBytes = 0
	return nil
}

func (i *Instance) Close() error {
	i.closeOnce.Do(func() {
		i.mu.Lock()
		i.closed = true
		store := i.store
		i.mu.Unlock()
		i.closeErr = store.Close()
	})
	return i.closeErr
}
