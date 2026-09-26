package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// StoreAdapter exposes an Instance through the storage.Store compatibility
// contract. All operations pass through the instance so runtime lifecycle,
// quotas, and usage accounting remain authoritative.
type StoreAdapter struct {
	instance *Instance
}

var _ storage.Store = (*StoreAdapter)(nil)

// NewStoreAdapter binds a storage.Store compatibility adapter to instance.
func NewStoreAdapter(instance *Instance) (*StoreAdapter, error) {
	if instance == nil {
		return nil, errors.New("runtime: instance is required")
	}
	return &StoreAdapter{instance: instance}, nil
}

// OpenWithStore binds store to a new runtime instance. The returned Instance
// takes ownership of store: Close closes it exactly once. If resetStore is
// non-nil, Reset closes the current store and replaces it with the store
// returned by that function; otherwise Reset returns
// ErrExternalResetUnsupported without mutating the bound store.
//
// Open remains the public embedded-runtime constructor and always creates a
// memory store. OpenWithStore is the internal compatibility seam used by the
// native server to retain a filesystem or run-through backing store.
func OpenWithStore(options Options, store storage.Store, resetStore func() (storage.Store, error)) (*Instance, error) {
	if store == nil {
		return nil, errors.New("runtime: store is required")
	}
	normalized, err := normalizeOptions(options, true)
	if err != nil {
		return nil, err
	}
	instance := newInstance(
		normalized,
		store,
		resetStore,
		IsPersistentBackend(normalized.Backend),
		!normalized.DisableMultipart,
	)
	if err := instance.initialize(context.Background()); err != nil {
		return nil, err
	}
	return instance, nil
}

func (a *StoreAdapter) CreateBucket(ctx context.Context, name string) error {
	return a.instance.CreateBucket(ctx, name)
}

func (a *StoreAdapter) DeleteBucket(ctx context.Context, name string) error {
	return a.instance.DeleteBucket(ctx, name)
}

func (a *StoreAdapter) HeadBucket(ctx context.Context, name string) (*storage.BucketInfo, error) {
	bucket, err := a.instance.HeadBucket(ctx, name)
	if err != nil {
		return nil, err
	}
	return &storage.BucketInfo{Name: bucket.Name, CreationDate: bucket.CreationDate}, nil
}

func (a *StoreAdapter) ListBuckets(ctx context.Context) ([]storage.BucketInfo, error) {
	buckets, err := a.instance.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]storage.BucketInfo, 0, len(buckets))
	for _, bucket := range buckets {
		out = append(out, storage.BucketInfo{Name: bucket.Name, CreationDate: bucket.CreationDate})
	}
	return out, nil
}

func (a *StoreAdapter) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	if body == nil {
		body = bytes.NewReader(nil)
	}
	// A caller that already holds the body in memory, such as the S3 handler
	// which read it once for the SigV4, Content-Length, Content-MD5 and
	// checksum checks, hands those bytes over instead of being read again into
	// a second full-size buffer. The runtime does not retain them; the store
	// makes its own copy.
	data, err := storage.BytesOf(body)
	if err != nil {
		return nil, err
	}
	object, err := a.instance.PutObject(ctx, bucket, key, data, PutOptions{
		ContentType:       opts.ContentType,
		Metadata:          opts.Metadata,
		ChecksumAlgorithm: opts.ChecksumAlgorithm,
		ChecksumValue:     opts.ChecksumValue,
		IfMatch:           opts.IfMatch,
		IfNoneMatch:       opts.IfNoneMatch,
	})
	if err != nil {
		return nil, err
	}
	return objectMeta(&object), nil
}

func (a *StoreAdapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	object, err := a.instance.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, nil, err
	}
	return io.NopCloser(bytes.NewReader(object.Data)), objectMeta(&object), nil
}

func (a *StoreAdapter) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	object, err := a.instance.HeadObject(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	return objectMeta(&object), nil
}

func (a *StoreAdapter) DeleteObject(ctx context.Context, bucket, key string) error {
	return a.instance.DeleteObject(ctx, bucket, key)
}

func (a *StoreAdapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	return a.instance.DeleteObjects(ctx, bucket, keys)
}

func (a *StoreAdapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	object, err := a.instance.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		return nil, err
	}
	return objectMeta(&object), nil
}

func (a *StoreAdapter) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	if opts.MaxKeys < 0 {
		return nil, ErrInvalidListLimit
	}
	page, err := a.instance.ListObjects(ctx, bucket, ListOptions{
		Prefix:     opts.Prefix,
		Cursor:     opts.ContinuationToken,
		Limit:      opts.MaxKeys,
		Delimiter:  opts.Delimiter,
		StartAfter: opts.StartAfter,
	})
	if err != nil {
		return nil, err
	}
	objects := make([]storage.ObjectMeta, 0, len(page.Objects))
	for i := range page.Objects {
		objects = append(objects, *objectMeta(&page.Objects[i]))
	}
	return &storage.ListResult{
		Objects:               objects,
		CommonPrefixes:        append([]string(nil), page.CommonPrefixes...),
		IsTruncated:           page.Truncated,
		ContinuationToken:     page.Cursor,
		NextContinuationToken: page.NextCursor,
		KeyCount:              page.KeyCount,
	}, nil
}

func objectMeta(object *Object) *storage.ObjectMeta {
	if object == nil {
		return nil
	}
	return &storage.ObjectMeta{
		Bucket:            object.Bucket,
		Key:               object.Key,
		Size:              object.Size,
		ETag:              object.ETag,
		VersionID:         object.VersionID,
		ContentType:       object.ContentType,
		LastModified:      object.LastModified,
		Metadata:          storage.CloneMetadata(object.Metadata),
		ChecksumAlgorithm: object.ChecksumAlgorithm,
		ChecksumValue:     object.ChecksumValue,
	}
}

func (a *StoreAdapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	return a.instance.CreateMultipartUpload(ctx, bucket, key)
}

func (a *StoreAdapter) GetMultipartUpload(ctx context.Context, uploadID string) (*storage.MultipartUpload, error) {
	return a.instance.GetMultipartUpload(ctx, uploadID)
}

func (a *StoreAdapter) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	return a.instance.UploadPart(ctx, uploadID, partNumber, body)
}

func (a *StoreAdapter) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	return a.instance.CompleteMultipartUpload(ctx, uploadID, parts)
}

func (a *StoreAdapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	return a.instance.AbortMultipartUpload(ctx, uploadID)
}

func (a *StoreAdapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	return a.instance.ListParts(ctx, uploadID)
}

func (a *StoreAdapter) ListPartsPage(ctx context.Context, uploadID string, opts storage.ListPartsOptions) (*storage.ListPartsResult, error) {
	return a.instance.ListPartsPage(ctx, uploadID, opts)
}

func (a *StoreAdapter) ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error {
	return a.instance.ValidateMultipartUpload(ctx, uploadID, bucket, key)
}

func (a *StoreAdapter) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	return a.instance.ListMultipartUploads(ctx, bucket, opts)
}

func (a *StoreAdapter) Close() error {
	if a == nil || a.instance == nil {
		return nil
	}
	return a.instance.Close()
}
