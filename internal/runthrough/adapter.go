package runthrough

import (
	"bytes"
	"context"
	"io"
	"sort"

	"github.com/chester-hill-solutions/stow/internal/storage"
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

func (a *Adapter) requireLiveWrites() error {
	if a.cfg.AllowLiveWrites {
		return nil
	}
	return ErrLiveWritesDisabled
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
	meta, err := a.local.PutObject(ctx, bucket, key, body, opts)
	if err != nil {
		return nil, err
	}
	if err := a.propagatePut(ctx, bucket, key, meta, opts); err != nil {
		return meta, err
	}
	return meta, nil
}

func (a *Adapter) propagatePut(ctx context.Context, bucket, key string, meta *storage.ObjectMeta, opts storage.PutOptions) error {
	if !a.upstreamEnabled(bucket) {
		return nil
	}
	if a.cfg.Policy == PolicyProxy {
		return a.requireLiveWrites()
	}
	if !a.cfg.AllowLiveWrites && a.cfg.Policy != PolicyMirrorWrites {
		return nil
	}
	if !a.cfg.AllowLiveWrites {
		return ErrLiveWritesDisabled
	}
	rc, _, err := a.local.GetObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	return a.upstream.PutObject(ctx, bucket, key, rc, opts)
}

func (a *Adapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	if a.cfg.Policy == PolicyProxy && a.upstreamEnabled(bucket) {
		return a.upstream.GetObject(ctx, bucket, key)
	}
	return a.getReadThrough(ctx, bucket, key)
}

func (a *Adapter) getReadThrough(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	rc, localMeta, localErr := a.local.GetObject(ctx, bucket, key)
	if localErr == nil {
		if !a.upstreamEnabled(bucket) || !a.cfg.Revalidate {
			return rc, localMeta, nil
		}
		rc.Close()

		upMeta, headErr := a.upstream.HeadObject(ctx, bucket, key)
		if headErr == storage.ErrObjectNotFound {
			if a.cfg.EvictOnUpstreamMissing {
				_ = a.local.DeleteObject(ctx, bucket, key)
			}
			return nil, nil, storage.ErrObjectNotFound
		}
		if headErr != nil {
			return a.local.GetObject(ctx, bucket, key)
		}
		if !metaIsNewer(upMeta, localMeta) {
			return a.local.GetObject(ctx, bucket, key)
		}
		return a.refreshFromUpstream(ctx, bucket, key)
	}
	if localErr != storage.ErrObjectNotFound {
		return nil, nil, localErr
	}
	if !a.upstreamEnabled(bucket) {
		return nil, nil, storage.ErrObjectNotFound
	}
	return a.refreshFromUpstream(ctx, bucket, key)
}

func (a *Adapter) refreshFromUpstream(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
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
	return io.NopCloser(bytes.NewReader(data)), cached, nil
}

func (a *Adapter) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	switch a.cfg.Policy {
	case PolicyProxy:
		if a.upstreamEnabled(bucket) {
			return a.upstream.HeadObject(ctx, bucket, key)
		}
		return a.local.HeadObject(ctx, bucket, key)
	default:
		return a.headReadThrough(ctx, bucket, key)
	}
}

func (a *Adapter) headReadThrough(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	localMeta, localErr := a.local.HeadObject(ctx, bucket, key)
	if localErr == nil {
		if !a.upstreamEnabled(bucket) || !a.cfg.Revalidate {
			return localMeta, nil
		}
		upMeta, headErr := a.upstream.HeadObject(ctx, bucket, key)
		if headErr == storage.ErrObjectNotFound {
			if a.cfg.EvictOnUpstreamMissing {
				_ = a.local.DeleteObject(ctx, bucket, key)
			}
			return nil, storage.ErrObjectNotFound
		}
		if headErr != nil {
			return localMeta, nil
		}
		if !metaIsNewer(upMeta, localMeta) {
			return localMeta, nil
		}
		_, refreshed, err := a.refreshFromUpstream(ctx, bucket, key)
		return refreshed, err
	}
	if localErr != storage.ErrObjectNotFound {
		return nil, localErr
	}
	if !a.upstreamEnabled(bucket) {
		return nil, storage.ErrObjectNotFound
	}
	_, meta, err := a.refreshFromUpstream(ctx, bucket, key)
	return meta, err
}

func (a *Adapter) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := a.local.DeleteObject(ctx, bucket, key); err != nil && err != storage.ErrObjectNotFound {
		return err
	}
	if !a.upstreamEnabled(bucket) {
		return nil
	}
	if !a.cfg.AllowLiveWrites {
		if a.cfg.Policy == PolicyMirrorWrites {
			return ErrLiveWritesDisabled
		}
		return nil
	}
	return a.upstream.DeleteObject(ctx, bucket, key)
}

func (a *Adapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	deleted, err := a.local.DeleteObjects(ctx, bucket, keys)
	if err != nil {
		return deleted, err
	}
	if !a.upstreamEnabled(bucket) || !a.cfg.AllowLiveWrites {
		if a.cfg.Policy == PolicyMirrorWrites && a.upstreamEnabled(bucket) {
			return deleted, ErrLiveWritesDisabled
		}
		return deleted, nil
	}
	for _, key := range deleted {
		if err := a.upstream.DeleteObject(ctx, bucket, key); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (a *Adapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	meta, err := a.local.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		return nil, err
	}
	if !a.upstreamEnabled(dstBucket) || !a.cfg.AllowLiveWrites {
		if a.cfg.Policy == PolicyMirrorWrites && a.upstreamEnabled(dstBucket) {
			return meta, ErrLiveWritesDisabled
		}
		return meta, nil
	}
	rc, srcMeta, err := a.local.GetObject(ctx, dstBucket, dstKey)
	if err != nil {
		return meta, err
	}
	defer rc.Close()
	if err := a.upstream.PutObject(ctx, dstBucket, dstKey, rc, storage.PutOptions{
		ContentType: srcMeta.ContentType,
		Metadata:    srcMeta.Metadata,
	}); err != nil {
		return meta, err
	}
	return meta, nil
}

func (a *Adapter) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	localResult, localErr := a.local.ListObjectsV2(ctx, bucket, opts)
	if !a.upstreamEnabled(bucket) {
		return localResult, localErr
	}

	upResult, upErr := a.upstream.ListObjectsV2(ctx, bucket, opts)
	if localErr != nil && upErr != nil {
		return nil, localErr
	}
	if localErr != nil {
		return upResult, nil
	}
	if upErr != nil {
		return localResult, nil
	}
	return mergeListResults(localResult, upResult), nil
}

func mergeListResults(local, upstream *storage.ListResult) *storage.ListResult {
	byKey := make(map[string]storage.ObjectMeta)
	for _, obj := range upstream.Objects {
		byKey[obj.Key] = obj
	}
	for _, obj := range local.Objects {
		byKey[obj.Key] = obj
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := &storage.ListResult{
		ContinuationToken:     local.ContinuationToken,
		NextContinuationToken: local.NextContinuationToken,
		IsTruncated:           local.IsTruncated || upstream.IsTruncated,
	}
	prefixSet := map[string]struct{}{}
	for _, p := range local.CommonPrefixes {
		prefixSet[p] = struct{}{}
	}
	for _, p := range upstream.CommonPrefixes {
		prefixSet[p] = struct{}{}
	}
	for p := range prefixSet {
		out.CommonPrefixes = append(out.CommonPrefixes, p)
	}
	sort.Strings(out.CommonPrefixes)

	for _, k := range keys {
		obj := byKey[k]
		out.Objects = append(out.Objects, obj)
	}
	out.KeyCount = len(out.Objects) + len(out.CommonPrefixes)
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
	if !a.upstreamEnabled(meta.Bucket) || !a.cfg.AllowLiveWrites {
		if a.cfg.Policy == PolicyMirrorWrites && a.upstreamEnabled(meta.Bucket) {
			return meta, ErrLiveWritesDisabled
		}
		return meta, nil
	}
	rc, objMeta, err := a.local.GetObject(ctx, meta.Bucket, meta.Key)
	if err != nil {
		return meta, err
	}
	defer rc.Close()
	if err := a.upstream.PutObject(ctx, meta.Bucket, meta.Key, rc, storage.PutOptions{
		ContentType: objMeta.ContentType,
		Metadata:    objMeta.Metadata,
	}); err != nil {
		return meta, err
	}
	return meta, nil
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	return a.local.AbortMultipartUpload(ctx, uploadID)
}

func (a *Adapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	return a.local.ListParts(ctx, uploadID)
}

// PropagateUpstreamPut attempts an upstream PutObject and enforces the live-write guard.
func (a *Adapter) PropagateUpstreamPut(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) error {
	if !a.upstreamEnabled(bucket) {
		return nil
	}
	if err := a.requireLiveWrites(); err != nil {
		return err
	}
	return a.upstream.PutObject(ctx, bucket, key, body, opts)
}
