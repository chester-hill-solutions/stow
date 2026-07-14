package runthrough

import (
	"bytes"
	"context"
	"io"
	"sort"

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
	local    storage.Store
	upstream Client
	cfg      Config
}

// New creates a run-through adapter. upstream may be nil for local-only behavior.
func New(cfg Config, local storage.Store, upstream Client) *Adapter {
	return &Adapter{cfg: cfg, local: local, upstream: upstream}
}

// Config returns the adapter configuration.
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
	if action == writePropagate {
		if err := a.propagateLocalObject(ctx, bucket, key); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

func (a *Adapter) propagateLocalObject(ctx context.Context, bucket, key string) error {
	rc, meta, err := a.local.GetObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	return a.upstream.PutObject(ctx, bucket, key, rc, storage.PutOptions{
		ContentType: meta.ContentType,
		Metadata:    meta.Metadata,
	})
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
		if !a.upstreamEnabled(bucket) || !a.cfg.Revalidate {
			return a.openLocal(ctx, bucket, key, localMeta, needBody)
		}
		upMeta, headErr := a.upstream.HeadObject(ctx, bucket, key)
		if headErr == storage.ErrObjectNotFound {
			if a.cfg.EvictOnUpstreamMissing {
				_ = a.local.DeleteObject(ctx, bucket, key)
			}
			return nil, nil, storage.ErrObjectNotFound
		}
		if headErr != nil {
			return a.openLocal(ctx, bucket, key, localMeta, needBody)
		}
		if !metaIsNewer(upMeta, localMeta) {
			return a.openLocal(ctx, bucket, key, localMeta, needBody)
		}
		return a.refreshFromUpstream(ctx, bucket, key, needBody)
	}
	if localErr != storage.ErrObjectNotFound {
		return nil, nil, localErr
	}
	if !a.upstreamEnabled(bucket) {
		return nil, nil, storage.ErrObjectNotFound
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
	cached, err := a.local.PutObject(ctx, bucket, key, bytes.NewReader(data), storage.PutOptions{
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
	if err := a.local.DeleteObject(ctx, bucket, key); err != nil && err != storage.ErrObjectNotFound {
		return err
	}
	action := a.decideUpstreamWrite(bucket)
	switch action {
	case writeSkip:
		return nil
	case writeError:
		return ErrLiveWritesDisabled
	case writePropagate:
		return a.upstream.DeleteObject(ctx, bucket, key)
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
	action := a.decideUpstreamWrite(bucket)
	switch action {
	case writeSkip:
		return deleted, nil
	case writeError:
		return deleted, ErrLiveWritesDisabled
	case writePropagate:
		for _, key := range deleted {
			if err := a.upstream.DeleteObject(ctx, bucket, key); err != nil {
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
	action := a.decideUpstreamWrite(dstBucket)
	switch action {
	case writeSkip:
		return meta, nil
	case writeError:
		return meta, ErrLiveWritesDisabled
	case writePropagate:
		if err := a.propagateLocalObject(ctx, dstBucket, dstKey); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		var _ writeAction = action
		return meta, nil
	}
}

func (a *Adapter) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	if a.cfg.Policy == PolicyProxy && a.upstreamEnabled(bucket) {
		return a.upstream.ListObjectsV2(ctx, bucket, opts)
	}
	if !a.upstreamEnabled(bucket) {
		return a.local.ListObjectsV2(ctx, bucket, opts)
	}

	localItems, err := listAllObjects(ctx, a.local, bucket, opts.Prefix)
	if err != nil {
		return nil, err
	}
	upItems, upErr := listAllObjects(ctx, a.upstream, bucket, opts.Prefix)
	if upErr != nil {
		// Upstream unavailable: fall back to local listing.
		return storage.PaginateObjects(localItems, opts), nil
	}
	return storage.PaginateObjects(mergeObjectLists(localItems, upItems), opts), nil
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
		if err := a.propagateLocalObject(ctx, meta.Bucket, meta.Key); err != nil {
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
