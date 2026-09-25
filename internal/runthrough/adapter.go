package runthrough

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

// writeAction is the decision for whether a mutating op should touch upstream.
type writeAction int

const (
	writeSkip writeAction = iota
	writeError
	writePropagate
)

// Adapter wraps a local storage.Store with optional upstream read-through caching
// and controlled live writes. It implements storage.Store for s3api routing.
type Adapter struct {
	local             storage.Store
	cache             storage.Store
	upstream          Client
	outbox            Outbox
	coordinatedOutbox CoordinatedOutbox
	cfg               Config
	separateCache     bool
	durableOutbox     bool
	cacheHits         atomic.Uint64
	cacheMisses       atomic.Uint64
	cacheEvictions    atomic.Uint64
	cacheMu           sync.Mutex
	cacheEntries      map[string]cacheEntry
	outboxLocks       outboxKeyLocks
	claimOwner        string
	claimLease        time.Duration
}

// New creates a run-through adapter. upstream may be nil for local-only behavior.
// The local store is also used as the cache for compatibility with existing callers.
func New(cfg Config, local storage.Store, upstream Client) *Adapter {
	return NewWithOutbox(cfg, local, local, upstream, NewMemoryOutbox())
}

// NewWithCache creates an adapter with separate authoritative local and
// upstream-derived cache stores.
func NewWithCache(cfg Config, local, cache storage.Store, upstream Client) *Adapter {
	return NewWithOutbox(cfg, local, cache, upstream, NewMemoryOutbox())
}

// NewWithOutbox creates an adapter with explicit local, cache, upstream, and
// outbox dependencies.
func NewWithOutbox(cfg Config, local, cache storage.Store, upstream Client, outbox Outbox) *Adapter {
	if cache == nil {
		cache = local
	}
	if outbox == nil {
		outbox = NewMemoryOutbox()
	}
	durable := false
	if provider, ok := outbox.(DurableOutbox); ok {
		durable = provider.Durable()
	}
	coordinated, _ := outbox.(CoordinatedOutbox)
	return &Adapter{
		cfg:               cfg,
		local:             local,
		cache:             cache,
		upstream:          upstream,
		outbox:            outbox,
		coordinatedOutbox: coordinated,
		separateCache:     cache != local,
		durableOutbox:     durable,
		cacheEntries:      make(map[string]cacheEntry),
		claimOwner:        newOutboxOwner(),
		claimLease:        defaultOutboxClaimLease,
	}
}

// Config returns the adapter configuration.
func (a *Adapter) Close() error {
	outboxErr := a.outbox.Close()
	localErr := a.local.Close()
	var cacheErr error
	if a.separateCache {
		cacheErr = a.cache.Close()
	}
	return errors.Join(outboxErr, localErr, cacheErr)
}

func (a *Adapter) Config() Config {
	return a.cfg
}

// CacheStats reports process-local cache hit and miss counters for admin
// inspection. They are intentionally not persisted across restarts.
func (a *Adapter) CacheStats() (hits, misses uint64) {
	return a.cacheHits.Load(), a.cacheMisses.Load()
}

// OutboxStats reports pending and terminal entries for admin inspection.
func (a *Adapter) OutboxStats() (pending, terminal int) {
	for _, entry := range a.outbox.Pending() {
		if entry.Terminal {
			terminal++
			continue
		}
		pending++
	}
	return pending, terminal
}

func (a *Adapter) OutboxPreparedStats() int {
	return len(a.preparedEntries())
}

func (a *Adapter) OutboxLastError() string {
	for _, entry := range a.orderingEntries() {
		if entry.LastError != "" {
			return entry.LastError
		}
	}
	return ""
}

func (a *Adapter) OutboxRetryAttempts() uint64 {
	var total uint64
	for _, entry := range a.orderingEntries() {
		total += uint64(entry.Attempts)
	}
	return total
}

// OutboxEntries returns a point-in-time copy for administrative inspection.
func (a *Adapter) OutboxEntries() []OutboxEntry {
	return a.outbox.Pending()
}

// OutboxPreparedEntries returns unresolved write-ahead intents for inspection.
func (a *Adapter) OutboxPreparedEntries() []OutboxEntry {
	return a.preparedEntries()
}

// DiscardOutboxEntry removes one pending or terminal propagation intent.
func (a *Adapter) DiscardOutboxEntry(id string) error {
	for _, entry := range a.outbox.Pending() {
		if entry.ID != id {
			continue
		}
		unlock := a.outboxLocks.lock(outboxIdentity(entry.Bucket, entry.Key))
		defer unlock()
		return a.outbox.Discard(id)
	}
	for _, entry := range a.preparedEntries() {
		if entry.ID != id {
			continue
		}
		unlock := a.outboxLocks.lock(outboxIdentity(entry.Bucket, entry.Key))
		defer unlock()
		return a.discardPreparedIntent(id)
	}
	return fmt.Errorf("outbox entry %q not found", id)
}

func (a *Adapter) upstreamEnabled(bucket string) bool {
	if a.upstream == nil {
		return false
	}
	if a.cfg.Upstream.Bucket == "" {
		return true
	}
	return a.cfg.Upstream.Bucket == bucket
}

// decideUpstreamWrite collapses Policy × AllowLiveWrites into one action.
func (a *Adapter) requireDurableOutbox(action writeAction) error {
	if action == writePropagate && (!a.durableOutbox || a.coordinatedOutbox == nil) {
		return ErrDurableOutboxRequired
	}
	return nil
}

func (a *Adapter) decideUpstreamWrite(bucket string) writeAction {
	if !a.upstreamEnabled(bucket) {
		return writeSkip
	}
	switch a.cfg.Policy {
	case PolicyMirrorWrites:
		if a.cfg.AllowLiveWrites {
			return writePropagate
		}
		return writeError
	case PolicyReadThroughCache:
		if a.cfg.AllowLiveWrites {
			return writePropagate
		}
		return writeSkip
	default:
		return writeError
	}
}

func (a *Adapter) invalidateCache(ctx context.Context, bucket, key string) {
	if !a.separateCache {
		return
	}
	if err := a.cache.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return
	}
}

func (a *Adapter) CreateBucket(ctx context.Context, name string) error {
	return a.local.CreateBucket(ctx, name)
}

func (a *Adapter) DeleteBucket(ctx context.Context, name string) error {
	return a.local.DeleteBucket(ctx, name)
}

func (a *Adapter) HeadBucket(ctx context.Context, name string) (*storage.BucketInfo, error) {
	return a.local.HeadBucket(ctx, name)
}

func (a *Adapter) ListBuckets(ctx context.Context) ([]storage.BucketInfo, error) {
	return a.local.ListBuckets(ctx)
}

func (a *Adapter) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return nil, err
	}
	var prepared OutboxEntry
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
		defer unlock()
		if err := a.rejectPreparedKey(bucket, key); err != nil {
			return nil, err
		}
		previousVersion, err := a.localVersionStrict(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		prepared, err = a.enqueuePreparedIntentLocked(OutboxPut, bucket, key, previousVersion)
		if err != nil {
			return nil, err
		}
	}

	meta, err := a.local.PutObject(ctx, bucket, key, body, opts)
	if err != nil {
		if prepared.ID != "" {
			return nil, a.reconcilePreparedAfterError(ctx, prepared, err)
		}
		return nil, err
	}
	a.invalidateCache(ctx, bucket, key)
	if action == writePropagate {
		if _, err := a.commitPreparedIntent(prepared.ID, objectVersion(meta)); err != nil {
			return meta, err
		}
		if err := a.completeIntentLocked(ctx, prepared); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

func (a *Adapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	return a.resolveObject(ctx, bucket, key, true)
}

func (a *Adapter) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	_, meta, err := a.resolveObject(ctx, bucket, key, false)
	return meta, err
}

// resolveObject implements read-through cache with optional revalidation.
// When needBody is false, the returned ReadCloser is always nil.
func (a *Adapter) resolveObject(ctx context.Context, bucket, key string, needBody bool) (io.ReadCloser, *storage.ObjectMeta, error) {
	localMeta, localErr := a.local.HeadObject(ctx, bucket, key)
	if localErr == nil {
		// With separate stores, local writes are authoritative and must not be
		// replaced by an upstream revalidation. The legacy single-store mode
		// retains its historical revalidation behavior for compatibility.
		if a.separateCache || !a.upstreamEnabled(bucket) || !a.cfg.Revalidate {
			return a.openLocal(ctx, bucket, key, localMeta, needBody)
		}
		return a.revalidateCachedObject(ctx, bucket, key, localMeta, needBody)
	}
	if !errors.Is(localErr, storage.ErrObjectNotFound) {
		return nil, nil, localErr
	}
	if !a.upstreamEnabled(bucket) {
		return nil, nil, storage.ErrObjectNotFound
	}

	cacheMeta, cacheErr := a.cache.HeadObject(ctx, bucket, key)
	if cacheErr == nil && a.cacheEntryExpired(bucket, key) {
		if err := a.cache.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
			return nil, nil, err
		}
		a.cacheEvictions.Add(1)
		cacheMeta, cacheErr = nil, storage.ErrObjectNotFound
	}
	if cacheErr == nil {
		if !a.cfg.Revalidate {
			return a.openCached(ctx, bucket, key, cacheMeta, needBody)
		}
		return a.revalidateCachedObject(ctx, bucket, key, cacheMeta, needBody)
	}
	if cacheErr != nil && !errors.Is(cacheErr, storage.ErrObjectNotFound) {
		return nil, nil, cacheErr
	}
	return a.refreshFromUpstream(ctx, bucket, key, needBody)
}

func (a *Adapter) revalidateCachedObject(ctx context.Context, bucket, key string, cachedMeta *storage.ObjectMeta, needBody bool) (io.ReadCloser, *storage.ObjectMeta, error) {
	upMeta, headErr := a.upstream.HeadObject(ctx, bucket, key)
	if headErr == storage.ErrObjectNotFound {
		if a.cfg.EvictOnUpstreamMissing {
			_ = a.cache.DeleteObject(ctx, bucket, key)
			if !a.separateCache {
				_ = a.local.DeleteObject(ctx, bucket, key)
			}
		}
		return nil, nil, storage.ErrObjectNotFound
	}
	if headErr != nil {
		return a.openCached(ctx, bucket, key, cachedMeta, needBody)
	}
	if !upstreamChanged(upMeta, cachedMeta) {
		return a.openCached(ctx, bucket, key, cachedMeta, needBody)
	}
	return a.refreshFromUpstream(ctx, bucket, key, needBody)
}

func (a *Adapter) openLocal(ctx context.Context, bucket, key string, meta *storage.ObjectMeta, needBody bool) (io.ReadCloser, *storage.ObjectMeta, error) {
	if !needBody {
		return nil, meta, nil
	}
	rc, bodyMeta, err := a.local.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, nil, err
	}
	return rc, bodyMeta, nil
}

func (a *Adapter) openCached(ctx context.Context, bucket, key string, meta *storage.ObjectMeta, needBody bool) (io.ReadCloser, *storage.ObjectMeta, error) {
	a.cacheHits.Add(1)
	a.touchCache(bucket, key)
	if !needBody {
		return nil, meta, nil
	}
	store := a.local
	if a.separateCache {
		store = a.cache
	}
	return store.GetObject(ctx, bucket, key)
}

func (a *Adapter) refreshFromUpstream(ctx context.Context, bucket, key string, needBody bool) (io.ReadCloser, *storage.ObjectMeta, error) {
	a.cacheMisses.Add(1)
	rc, meta, err := a.upstream.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, err
	}
	cacheStore := a.local
	if a.separateCache {
		cacheStore = a.cache
		if err := cacheStore.CreateBucket(ctx, bucket); err != nil && !errors.Is(err, storage.ErrBucketExists) {
			return nil, nil, err
		}
	}
	cached, err := cacheStore.PutObject(ctx, bucket, key, bytes.NewReader(data), storage.PutOptions{
		ContentType:       meta.ContentType,
		Metadata:          meta.Metadata,
		ChecksumAlgorithm: meta.ChecksumAlgorithm,
		ChecksumValue:     meta.ChecksumValue,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := a.trackCacheObject(ctx, bucket, key); err != nil {
		return nil, nil, err
	}
	if !needBody {
		return nil, cached, nil
	}
	return io.NopCloser(bytes.NewReader(data)), cached, nil
}

func (a *Adapter) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	localItems, err := listAllObjects(ctx, a.local, bucket, opts.Prefix)
	if err != nil {
		return nil, err
	}
	if !a.upstreamEnabled(bucket) {
		cacheItems, cacheErr := a.listCacheItems(ctx, bucket, opts.Prefix)
		if cacheErr != nil {
			return nil, cacheErr
		}
		return storage.PaginateObjects(mergeObjectLists(localItems, cacheItems), opts), nil
	}
	upItems, upErr := listAllObjects(ctx, a.upstream, bucket, opts.Prefix)
	if upErr == nil {
		return storage.PaginateObjects(mergeObjectLists(localItems, upItems), opts), nil
	}
	cacheItems, cacheErr := a.listCacheItems(ctx, bucket, opts.Prefix)
	if cacheErr != nil {
		return nil, cacheErr
	}
	return storage.PaginateObjects(mergeObjectLists(localItems, cacheItems), opts), nil
}

func (a *Adapter) listCacheItems(ctx context.Context, bucket, prefix string) ([]storage.ObjectMeta, error) {
	if !a.separateCache {
		return nil, nil
	}
	items, err := listAllObjects(ctx, a.cache, bucket, prefix)
	if errors.Is(err, storage.ErrBucketNotFound) {
		return nil, nil
	}
	return items, err
}

// listAllObjects walks every page for a prefix so merged listings can re-paginate stably.
func listAllObjects(ctx context.Context, store interface {
	ListObjectsV2(context.Context, string, storage.ListOptions) (*storage.ListResult, error)
}, bucket, prefix string) ([]storage.ObjectMeta, error) {
	var out []storage.ObjectMeta
	token := ""
	for {
		page, err := store.ListObjectsV2(ctx, bucket, storage.ListOptions{
			Prefix:            prefix,
			MaxKeys:           1000,
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Objects...)
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// mergeObjectLists unions by key; local metadata wins on duplicates.
func mergeObjectLists(local, upstream []storage.ObjectMeta) []storage.ObjectMeta {
	byKey := make(map[string]storage.ObjectMeta, len(local)+len(upstream))
	for _, obj := range upstream {
		byKey[obj.Key] = obj
	}
	for _, obj := range local {
		byKey[obj.Key] = obj
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]storage.ObjectMeta, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}
