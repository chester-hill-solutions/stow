package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

type Instance struct {
	mu        sync.Mutex
	store     storage.Store
	options   Options
	usage     Usage
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func Open(options Options) (*Instance, error) {
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Instance{
		store:   storage.NewMemoryStore(),
		options: normalized,
	}, nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Backend == "" {
		options.Backend = BackendMemory
	}
	if options.Backend != BackendMemory {
		return Options{}, ErrUnsupportedBackend
	}
	if options.MaxBytes < 0 || options.MaxObjects < 0 {
		return Options{}, fmt.Errorf("runtime quotas must not be negative")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MaxObjects == 0 {
		options.MaxObjects = DefaultMaxObjects
	}
	return options, nil
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

func (i *Instance) CreateBucket(ctx context.Context, name string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	return i.store.CreateBucket(ctx, name)
}

func (i *Instance) DeleteBucket(ctx context.Context, name string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	return i.store.DeleteBucket(ctx, name)
}

func (i *Instance) ListBuckets(ctx context.Context) ([]Bucket, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return nil, err
	}
	items, err := i.store.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(items))
	for _, item := range items {
		out = append(out, Bucket{Name: item.Name, CreationDate: item.CreationDate})
	}
	return out, nil
}

func (i *Instance) PutObject(ctx context.Context, bucket, key string, data []byte, options PutOptions) (Object, error) {
	if err := i.checkContext(ctx); err != nil {
		return Object{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Object{}, err
	}

	oldSize, exists, err := i.objectSize(ctx, bucket, key)
	if err != nil {
		return Object{}, err
	}
	requestedSize := int64(len(data))
	if !exists && i.usage.Objects+1 > i.options.MaxObjects {
		return Object{}, ErrQuotaExceeded
	}
	if i.usage.Bytes-oldSize+requestedSize > i.options.MaxBytes {
		return Object{}, ErrQuotaExceeded
	}
	copyData := append([]byte(nil), data...)
	meta, err := i.store.PutObject(ctx, bucket, key, bytes.NewReader(copyData), storage.PutOptions{
		ContentType: options.ContentType,
		Metadata:    storage.CloneMetadata(options.Metadata),
	})
	if err != nil {
		return Object{}, err
	}
	if exists {
		i.usage.Bytes -= oldSize
	} else {
		i.usage.Objects++
	}
	i.usage.Bytes += int64(len(copyData))
	return objectFromMeta(meta, nil), nil
}

func (i *Instance) GetObject(ctx context.Context, bucket, key string) (Object, error) {
	if err := i.checkContext(ctx); err != nil {
		return Object{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Object{}, err
	}
	reader, meta, err := i.store.GetObject(ctx, bucket, key)
	if err != nil {
		return Object{}, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return Object{}, err
	}
	return objectFromMeta(meta, data), nil
}

func (i *Instance) HeadObject(ctx context.Context, bucket, key string) (Object, error) {
	if err := i.checkContext(ctx); err != nil {
		return Object{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Object{}, err
	}
	meta, err := i.store.HeadObject(ctx, bucket, key)
	if err != nil {
		return Object{}, err
	}
	return objectFromMeta(meta, nil), nil
}

func (i *Instance) ListObjects(ctx context.Context, bucket string, options ListOptions) (ObjectPage, error) {
	if err := i.checkContext(ctx); err != nil {
		return ObjectPage{}, err
	}
	if options.Limit < 0 {
		return ObjectPage{}, ErrInvalidListLimit
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return ObjectPage{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = 1000
	}
	result, err := i.store.ListObjectsV2(ctx, bucket, storage.ListOptions{
		Prefix:            options.Prefix,
		ContinuationToken: options.Cursor,
		MaxKeys:           limit,
	})
	if err != nil {
		return ObjectPage{}, err
	}
	objects := make([]Object, 0, len(result.Objects))
	for _, meta := range result.Objects {
		objects = append(objects, objectFromMeta(&meta, nil))
	}
	return ObjectPage{
		Objects:    objects,
		Truncated:  result.IsTruncated,
		NextCursor: result.NextContinuationToken,
	}, nil
}

func (i *Instance) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	meta, err := i.store.HeadObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	if err := i.store.DeleteObject(ctx, bucket, key); err != nil {
		return err
	}
	i.usage.Bytes -= meta.Size
	i.usage.Objects--
	return nil
}

func (i *Instance) CopyObject(ctx context.Context, sourceBucket, sourceKey, destinationBucket, destinationKey string) (Object, error) {
	if err := i.checkContext(ctx); err != nil {
		return Object{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Object{}, err
	}
	sourceMeta, err := i.store.HeadObject(ctx, sourceBucket, sourceKey)
	if err != nil {
		return Object{}, err
	}
	oldSize, exists, err := i.objectSize(ctx, destinationBucket, destinationKey)
	if err != nil {
		return Object{}, err
	}
	if !exists && i.usage.Objects+1 > i.options.MaxObjects {
		return Object{}, ErrQuotaExceeded
	}
	if i.usage.Bytes-oldSize+sourceMeta.Size > i.options.MaxBytes {
		return Object{}, ErrQuotaExceeded
	}
	meta, err := i.store.CopyObject(ctx, sourceBucket, sourceKey, destinationBucket, destinationKey)
	if err != nil {
		return Object{}, err
	}
	if exists {
		i.usage.Bytes -= oldSize
	} else {
		i.usage.Objects++
	}
	i.usage.Bytes += sourceMeta.Size
	return objectFromMeta(meta, nil), nil
}

func (i *Instance) objectSize(ctx context.Context, bucket, key string) (int64, bool, error) {
	meta, err := i.store.HeadObject(ctx, bucket, key)
	if err == nil {
		return meta.Size, true, nil
	}
	if errors.Is(err, storage.ErrObjectNotFound) {
		return 0, false, nil
	}
	return 0, false, err
}

func objectFromMeta(meta *storage.ObjectMeta, data []byte) Object {
	if meta == nil {
		return Object{Data: append([]byte(nil), data...)}
	}
	return Object{
		Bucket:       meta.Bucket,
		Key:          meta.Key,
		Data:         append([]byte(nil), data...),
		Size:         meta.Size,
		ETag:         meta.ETag,
		ContentType:  meta.ContentType,
		Metadata:     storage.CloneMetadata(meta.Metadata),
		LastModified: meta.LastModified,
	}
}

func (i *Instance) Reset(ctx context.Context) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	if err := i.store.Close(); err != nil {
		return err
	}
	i.store = storage.NewMemoryStore()
	i.usage = Usage{}
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
