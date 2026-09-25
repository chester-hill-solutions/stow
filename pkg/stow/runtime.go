package stow

import (
	"context"
	"errors"

	stowruntime "github.com/chester-hill-solutions/stow/internal/runtime"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

type Runtime struct {
	inner *stowruntime.Instance
}

func Open(options Options) (*Runtime, error) {
	instance, err := stowruntime.Open(options)
	if err != nil {
		return nil, mapError(err)
	}
	return &Runtime{inner: instance}, nil
}

func (r *Runtime) CreateBucket(ctx context.Context, name string) error {
	return mapError(r.inner.CreateBucket(ctx, name))
}

func (r *Runtime) DeleteBucket(ctx context.Context, name string) error {
	return mapError(r.inner.DeleteBucket(ctx, name))
}

func (r *Runtime) ListBuckets(ctx context.Context) ([]Bucket, error) {
	buckets, err := r.inner.ListBuckets(ctx)
	return buckets, mapError(err)
}

func (r *Runtime) PutObject(ctx context.Context, bucket, key string, data []byte, options PutOptions) (Object, error) {
	object, err := r.inner.PutObject(ctx, bucket, key, data, options)
	return object, mapError(err)
}

func (r *Runtime) GetObject(ctx context.Context, bucket, key string) (Object, error) {
	object, err := r.inner.GetObject(ctx, bucket, key)
	return object, mapError(err)
}

func (r *Runtime) HeadObject(ctx context.Context, bucket, key string) (Object, error) {
	object, err := r.inner.HeadObject(ctx, bucket, key)
	return object, mapError(err)
}

func (r *Runtime) ListObjects(ctx context.Context, bucket string, options ListOptions) (ObjectPage, error) {
	page, err := r.inner.ListObjects(ctx, bucket, options)
	return page, mapError(err)
}

func (r *Runtime) DeleteObject(ctx context.Context, bucket, key string) error {
	return mapError(r.inner.DeleteObject(ctx, bucket, key))
}

func (r *Runtime) CopyObject(ctx context.Context, sourceBucket, sourceKey, destinationBucket, destinationKey string) (Object, error) {
	object, err := r.inner.CopyObject(ctx, sourceBucket, sourceKey, destinationBucket, destinationKey)
	return object, mapError(err)
}

func (r *Runtime) Reset(ctx context.Context) error {
	return mapError(r.inner.Reset(ctx))
}

func (r *Runtime) Close() error {
	return mapError(r.inner.Close())
}

func (r *Runtime) Usage() Usage {
	return r.inner.Usage()
}

func (r *Runtime) Capabilities() Capabilities {
	return r.inner.Capabilities()
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, storage.ErrBucketNotFound):
		return ErrBucketNotFound
	case errors.Is(err, storage.ErrBucketExists):
		return ErrBucketExists
	case errors.Is(err, storage.ErrBucketNotEmpty):
		return ErrBucketNotEmpty
	case errors.Is(err, storage.ErrObjectNotFound):
		return ErrObjectNotFound
	case errors.Is(err, storage.ErrInvalidBucketName):
		return ErrInvalidBucket
	case errors.Is(err, storage.ErrInvalidKey):
		return ErrInvalidKey
	default:
		return err
	}
}
