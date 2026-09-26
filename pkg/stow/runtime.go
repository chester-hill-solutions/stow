package stow

import (
	"context"
	"errors"

	stowruntime "github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

type Runtime struct {
	inner *stowruntime.Instance
}

func Open(options Options) (*Runtime, error) {
	instance, err := stowruntime.Open(stowruntime.Options{
		Backend:    stowruntime.Backend(options.Backend),
		MaxBytes:   options.MaxBytes,
		MaxObjects: options.MaxObjects,
		Authority:  options.Authority,
	})
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
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Bucket, 0, len(buckets))
	for _, bucket := range buckets {
		out = append(out, Bucket{Name: bucket.Name, CreationDate: bucket.CreationDate})
	}
	return out, nil
}

func (r *Runtime) PutObject(ctx context.Context, bucket, key string, data []byte, options PutOptions) (Object, error) {
	object, err := r.inner.PutObject(ctx, bucket, key, data, stowruntime.PutOptions{
		ContentType: options.ContentType,
		Metadata:    storage.CloneMetadata(options.Metadata),
	})
	if err != nil {
		return Object{}, mapError(err)
	}
	return objectOf(object), nil
}

func (r *Runtime) GetObject(ctx context.Context, bucket, key string) (Object, error) {
	object, err := r.inner.GetObject(ctx, bucket, key)
	if err != nil {
		return Object{}, mapError(err)
	}
	return objectOf(object), nil
}

func (r *Runtime) HeadObject(ctx context.Context, bucket, key string) (Object, error) {
	object, err := r.inner.HeadObject(ctx, bucket, key)
	if err != nil {
		return Object{}, mapError(err)
	}
	return objectOf(object), nil
}

func (r *Runtime) ListObjects(ctx context.Context, bucket string, options ListOptions) (ObjectPage, error) {
	page, err := r.inner.ListObjects(ctx, bucket, stowruntime.ListOptions{
		Prefix: options.Prefix,
		Cursor: options.Cursor,
		Limit:  options.Limit,
	})
	if err != nil {
		return ObjectPage{}, mapError(err)
	}
	objects := make([]Object, 0, len(page.Objects))
	for _, object := range page.Objects {
		objects = append(objects, objectOf(object))
	}
	return ObjectPage{
		Objects:    objects,
		Truncated:  page.Truncated,
		NextCursor: page.NextCursor,
	}, nil
}

func (r *Runtime) DeleteObject(ctx context.Context, bucket, key string) error {
	return mapError(r.inner.DeleteObject(ctx, bucket, key))
}

func (r *Runtime) CopyObject(ctx context.Context, sourceBucket, sourceKey, destinationBucket, destinationKey string) (Object, error) {
	object, err := r.inner.CopyObject(ctx, sourceBucket, sourceKey, destinationBucket, destinationKey)
	if err != nil {
		return Object{}, mapError(err)
	}
	return objectOf(object), nil
}

func (r *Runtime) Reset(ctx context.Context) error {
	return mapError(r.inner.Reset(ctx))
}

func (r *Runtime) Close() error {
	return mapError(r.inner.Close())
}

func (r *Runtime) Usage() Usage {
	usage := r.inner.Usage()
	return Usage{Bytes: usage.Bytes, Objects: usage.Objects}
}

// Authority reports what this environment permits.
func (r *Runtime) Authority() Authority { return r.inner.Authority() }

func (r *Runtime) Capabilities() Capabilities {
	capabilities := r.inner.Capabilities()
	return Capabilities{
		Backend:    Backend(capabilities.Backend),
		MaxBytes:   capabilities.MaxBytes,
		MaxObjects: capabilities.MaxObjects,
		Persistent: capabilities.Persistent,
		Multipart:  capabilities.Multipart,
		Upstream:   capabilities.Upstream,
	}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, stowruntime.ErrClosed):
		return ErrClosed
	case errors.Is(err, stowruntime.ErrQuotaExceeded):
		return ErrQuotaExceeded
	case errors.Is(err, stowruntime.ErrUnsupportedBackend):
		return ErrUnsupportedBackend
	case errors.Is(err, stowruntime.ErrInvalidListLimit):
		return ErrInvalidListLimit
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

func objectOf(object stowruntime.Object) Object {
	return Object{
		Bucket:       object.Bucket,
		Key:          object.Key,
		Data:         append([]byte(nil), object.Data...),
		Size:         object.Size,
		ETag:         object.ETag,
		ContentType:  object.ContentType,
		Metadata:     storage.CloneMetadata(object.Metadata),
		LastModified: object.LastModified,
	}
}
