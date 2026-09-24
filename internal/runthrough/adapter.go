package runthrough

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
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
	local         storage.Store
	cache         storage.Store
	upstream      Client
	outbox        Outbox
	cfg           Config
	separateCache bool
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
	return &Adapter{cfg: cfg, local: local, cache: cache, upstream: upstream, outbox: outbox, separateCache: cache != local}
}

// Config returns the adapter configuration.
func (a *Adapter) Close() error {
	localErr := a.local.Close()
	if a.separateCache {
		if cacheErr := a.cache.Close(); localErr == nil {
			localErr = cacheErr
		}
	}
	if outboxErr := a.outbox.Close(); localErr == nil {
		localErr = outboxErr
	}
	return localErr
}

func (a *Adapter) Config() Config {
	return a.cfg
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
func (a *Adapter) decideUpstreamWrite(bucket string) writeAction {
	if !a.upstreamEnabled(bucket) {
		return writeSkip
	}
	switch a.cfg.Policy {
	case PolicyProxy, PolicyMirrorWrites:
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
		// Unknown/empty policy: treat like read-through.
		var _ Policy = a.cfg.Policy
		if a.cfg.AllowLiveWrites {
			return writePropagate
		}
		return writeSkip
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

func (a *Adapter) enqueueIntent(operation, bucket, key string) (OutboxEntry, error) {
	entry := OutboxEntry{Operation: operation, Bucket: bucket, Key: key, CreatedAt: time.Now().UTC()}
	if err := a.outbox.Enqueue(entry); err != nil {
		return OutboxEntry{}, err
	}
	pending := a.outbox.Pending()
	for i := len(pending) - 1; i >= 0; i-- {
		if pending[i].Bucket == bucket && pending[i].Key == key && pending[i].Operation == operation {
			return pending[i], nil
		}
	}
	return OutboxEntry{}, fmt.Errorf("outbox entry was not retained")
}

func (a *Adapter) propagateEntry(ctx context.Context, entry OutboxEntry) error {
	switch entry.Operation {
	case "put":
		rc, meta, err := a.local.GetObject(ctx, entry.Bucket, entry.Key)
		if err != nil {
			return err
		}
		defer rc.Close()
		return a.upstream.PutObject(ctx, entry.Bucket, entry.Key, rc, storage.PutOptions{
			ContentType: meta.ContentType,
			Metadata:    meta.Metadata,
		})
	case "delete":
		return a.upstream.DeleteObject(ctx, entry.Bucket, entry.Key)
	default:
		return fmt.Errorf("unsupported outbox operation %q", entry.Operation)
	}
}

func (a *Adapter) completeIntent(ctx context.Context, entry OutboxEntry) error {
	if err := a.propagateEntry(ctx, entry); err != nil {
		_ = a.outbox.MarkFailure(entry.ID, err, time.Now().Add(outboxRetryDelay(entry.Attempts)))
		return err
	}
	return a.outbox.MarkSuccess(entry.ID)
}

func outboxRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<uint(attempts-1)) * time.Second
}

// RetryPending retries due outbox entries. It is safe to call from a worker or
// at startup; entries that are not due remain queued.
func (a *Adapter) RetryPending(ctx context.Context) error {
	now := time.Now()
	for _, entry := range a.outbox.Pending() {
		if !entry.NextAttempt.IsZero() && entry.NextAttempt.After(now) {
			continue
		}
		if !a.upstreamEnabled(entry.Bucket) {
			continue
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) ListBuckets(ctx context.Context) ([]storage.BucketInfo, error) {
	return a.local.ListBuckets(ctx)
}

func (a *Adapter) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}

	if a.cfg.Policy == PolicyProxy && action == writePropagate {
		// Proxy reads go upstream-only; writes must match (then cache locally).
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		if err := a.upstream.PutObject(ctx, bucket, key, bytes.NewReader(data), opts); err != nil {
			return nil, err
		}
		return a.local.PutObject(ctx, bucket, key, bytes.NewReader(data), opts)
	}

	meta, err := a.local.PutObject(ctx, bucket, key, body, opts)
	if err != nil {
		return nil, err
	}
	a.invalidateCache(ctx, bucket, key)
	if action == writePropagate {
		entry, err := a.enqueueIntent("put", bucket, key)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

func (a *Adapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	if a.cfg.Policy == PolicyProxy && a.upstreamEnabled(bucket) {
		return a.upstream.GetObject(ctx, bucket, key)
	}
	return a.resolveObject(ctx, bucket, key, true)
}

func (a *Adapter) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	if a.cfg.Policy == PolicyProxy && a.upstreamEnabled(bucket) {
		return a.upstream.HeadObject(ctx, bucket, key)
	}
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
	if !metaIsNewer(upMeta, cachedMeta) {
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
		ContentType: meta.ContentType,
		Metadata:    meta.Metadata,
	})
	if err != nil {
		return nil, nil, err
	}
	if !needBody {
		return nil, cached, nil
	}
	return io.NopCloser(bytes.NewReader(data)), cached, nil
}

func (a *Adapter) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := a.local.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return err
	}
	a.invalidateCache(ctx, bucket, key)
	action := a.decideUpstreamWrite(bucket)
	switch action {
	case writeSkip:
		return nil
	case writeError:
		return ErrLiveWritesDisabled
	case writePropagate:
		entry, err := a.enqueueIntent("delete", bucket, key)
		if err != nil {
			return err
		}
		return a.completeIntent(ctx, entry)
	default:
		var _ writeAction = action
		return nil
	}
}

func (a *Adapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	deleted, err := a.local.DeleteObjects(ctx, bucket, keys)
	if err != nil {
		return deleted, err
	}
	if a.separateCache {
		for _, key := range deleted {
			a.invalidateCache(ctx, bucket, key)
		}
	}
	action := a.decideUpstreamWrite(bucket)
	switch action {
	case writeSkip:
		return deleted, nil
	case writeError:
		return deleted, ErrLiveWritesDisabled
	case writePropagate:
		for _, key := range deleted {
			entry, err := a.enqueueIntent("delete", bucket, key)
			if err != nil {
				return deleted, err
			}
			if err := a.completeIntent(ctx, entry); err != nil {
				return deleted, err
			}
		}
		return deleted, nil
	default:
		var _ writeAction = action
		return deleted, nil
	}
}

func (a *Adapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	meta, err := a.local.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		return nil, err
	}
	a.invalidateCache(ctx, dstBucket, dstKey)
	action := a.decideUpstreamWrite(dstBucket)
	switch action {
	case writeSkip:
		return meta, nil
	case writeError:
		return meta, ErrLiveWritesDisabled
	case writePropagate:
		entry, err := a.enqueueIntent("put", dstBucket, dstKey)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		var _ writeAction = action
		return meta, nil
	}
}

func (a *Adapter) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	localItems, err := listAllObjects(ctx, a.local, bucket, opts.Prefix)
	if err != nil {
		return nil, err
	}
	cacheItems := localItems
	if a.separateCache {
		cacheItems, err = listAllObjects(ctx, a.cache, bucket, opts.Prefix)
		if errors.Is(err, storage.ErrBucketNotFound) {
			cacheItems = nil
			err = nil
		}
		if err != nil {
			return nil, err
		}
	}
	merged := mergeObjectLists(localItems, cacheItems)
	if !a.upstreamEnabled(bucket) {
		return storage.PaginateObjects(merged, opts), nil
	}
	upItems, upErr := listAllObjects(ctx, a.upstream, bucket, opts.Prefix)
	if upErr != nil {
		// Upstream unavailable: fall back to local plus cached listing.
		return storage.PaginateObjects(merged, opts), nil
	}
	return storage.PaginateObjects(mergeObjectLists(merged, upItems), opts), nil
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

func (a *Adapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	return a.local.CreateMultipartUpload(ctx, bucket, key)
}

func (a *Adapter) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	return a.local.UploadPart(ctx, uploadID, partNumber, body)
}

func (a *Adapter) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	meta, err := a.local.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		return nil, err
	}
	action := a.decideUpstreamWrite(meta.Bucket)
	switch action {
	case writeSkip:
		return meta, nil
	case writeError:
		return meta, ErrLiveWritesDisabled
	case writePropagate:
		entry, err := a.enqueueIntent("put", meta.Bucket, meta.Key)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		var _ writeAction = action
		return meta, nil
	}
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	return a.local.AbortMultipartUpload(ctx, uploadID)
}

func (a *Adapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	return a.local.ListParts(ctx, uploadID)
}

func (a *Adapter) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	return a.local.ListMultipartUploads(ctx, bucket, opts)
}
