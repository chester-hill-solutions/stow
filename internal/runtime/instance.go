package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

type Instance struct {
	mu               sync.Mutex
	store            storage.Store
	resetStore       func() (storage.Store, error)
	options          Options
	usage            Usage
	multipart        map[string]multipartUsage
	multipartTargets map[string]int
	reservedTargets  map[string]struct{}
	reservedObjects  int64
	reservedBytes    int64
	persistent       bool
	multipartEnabled bool
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
	}, false, false), nil
}

func newInstance(options Options, store storage.Store, resetStore func() (storage.Store, error), persistent, multipartEnabled bool) *Instance {
	return &Instance{
		store:            store,
		resetStore:       resetStore,
		options:          options,
		multipart:        make(map[string]multipartUsage),
		multipartTargets: make(map[string]int),
		reservedTargets:  make(map[string]struct{}),
		persistent:       persistent,
		multipartEnabled: multipartEnabled,
	}
}

func normalizeOptions(options Options, boundStore bool) (Options, error) {
	if options.Backend == "" {
		options.Backend = BackendMemory
	}
	if options.Backend != BackendMemory && (!boundStore || options.Backend != BackendFilesystem) {
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

func (i *Instance) checkContextAndOpen(ctx context.Context) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	return i.checkOpen()
}

func (i *Instance) capabilitiesLocked() Capabilities {
	return Capabilities{
		Backend:    i.options.Backend,
		MaxBytes:   i.options.MaxBytes,
		MaxObjects: i.options.MaxObjects,
		Persistent: i.persistent,
		Multipart:  i.multipartEnabled,
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

func (i *Instance) HeadBucket(ctx context.Context, name string) (Bucket, error) {
	if err := i.checkContext(ctx); err != nil {
		return Bucket{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Bucket{}, err
	}
	info, err := i.store.HeadBucket(ctx, name)
	if err != nil {
		return Bucket{}, err
	}
	return Bucket{Name: info.Name, CreationDate: info.CreationDate}, nil
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
	if !exists && !i.objectQuotaFits(objectTarget(bucket, key), 1) {
		return Object{}, ErrQuotaExceeded
	}
	if !i.bytesQuotaFits(oldSize, requestedSize) {
		return Object{}, ErrQuotaExceeded
	}
	target := objectTarget(bucket, key)
	_, targetReserved := i.reservedTargets[target]
	// Not copied: every store copies the body through ETagForReader, so a copy
	// here was a redundant allocation. See storage.ByteReader for the ownership rule.
	meta, err := i.store.PutObject(ctx, bucket, key, bytes.NewReader(data), storage.PutOptions{
		ContentType:       options.ContentType,
		Metadata:          storage.CloneMetadata(options.Metadata),
		ChecksumAlgorithm: options.ChecksumAlgorithm,
		ChecksumValue:     options.ChecksumValue,
		IfMatch:           options.IfMatch,
		IfNoneMatch:       options.IfNoneMatch,
	})
	if err != nil {
		return Object{}, err
	}
	if exists {
		i.usage.Bytes -= oldSize
	} else {
		i.usage.Objects++
	}
	i.usage.Bytes += int64(len(data))
	if targetReserved {
		i.consumeTargetReservation(target)
	}
	i.reconcileTargetReservation(target, true)
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
		Delimiter:         options.Delimiter,
		ContinuationToken: options.Cursor,
		MaxKeys:           limit,
		StartAfter:        options.StartAfter,
	})
	if err != nil {
		return ObjectPage{}, err
	}
	objects := make([]Object, 0, len(result.Objects))
	for _, meta := range result.Objects {
		objects = append(objects, objectFromMeta(&meta, nil))
	}
	return ObjectPage{
		Objects:        objects,
		CommonPrefixes: append([]string(nil), result.CommonPrefixes...),
		Truncated:      result.IsTruncated,
		Cursor:         result.ContinuationToken,
		NextCursor:     result.NextContinuationToken,
		KeyCount:       result.KeyCount,
	}, nil
}

func (i *Instance) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return nil, err
	}
	if err := storage.ValidateBucketName(bucket); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if err := storage.ValidateKey(key); err != nil {
			return nil, err
		}
	}

	sizes := make(map[string]int64, len(keys))
	for _, key := range keys {
		if err := i.checkContext(ctx); err != nil {
			return nil, err
		}
		meta, err := i.store.HeadObject(ctx, bucket, key)
		if err == nil {
			sizes[key] = meta.Size
		} else if !errors.Is(err, storage.ErrObjectNotFound) {
			return nil, err
		}
	}
	deleted, err := i.store.DeleteObjects(ctx, bucket, keys)
	for _, key := range deleted {
		if size, ok := sizes[key]; ok {
			i.usage.Bytes -= size
		}
		i.usage.Objects--
		target := objectTarget(bucket, key)
		i.consumeTargetReservation(target)
		i.reconcileTargetReservation(target, false)
	}
	return deleted, err
}

func (i *Instance) deleteObjectLocked(ctx context.Context, bucket, key string) error {
	meta, err := i.store.HeadObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	if err := i.store.DeleteObject(ctx, bucket, key); err != nil {
		return err
	}
	i.usage.Bytes -= meta.Size
	i.usage.Objects--
	target := objectTarget(bucket, key)
	i.consumeTargetReservation(target)
	i.reconcileTargetReservation(target, false)
	return nil
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
	return i.deleteObjectLocked(ctx, bucket, key)
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
	if !exists && !i.objectQuotaFits(objectTarget(destinationBucket, destinationKey), 1) {
		return Object{}, ErrQuotaExceeded
	}
	if !i.bytesQuotaFits(oldSize, sourceMeta.Size) {
		return Object{}, ErrQuotaExceeded
	}
	target := objectTarget(destinationBucket, destinationKey)
	_, targetReserved := i.reservedTargets[target]
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
	if targetReserved {
		i.consumeTargetReservation(target)
	}
	i.reconcileTargetReservation(target, true)
	return objectFromMeta(meta, nil), nil
}

func objectTarget(bucket, key string) string {
	return bucket + "\x00" + key
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
		Bucket:            meta.Bucket,
		Key:               meta.Key,
		Data:              append([]byte(nil), data...),
		Size:              meta.Size,
		ETag:              meta.ETag,
		VersionID:         meta.VersionID,
		ContentType:       meta.ContentType,
		Metadata:          storage.CloneMetadata(meta.Metadata),
		LastModified:      meta.LastModified,
		ChecksumAlgorithm: meta.ChecksumAlgorithm,
		ChecksumValue:     meta.ChecksumValue,
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
	var next storage.Store
	if i.resetStore != nil {
		var err error
		next, err = i.resetStore()
		if err != nil {
			return err
		}
	} else {
		return ErrExternalResetUnsupported
	}
	if err := i.store.Close(); err != nil {
		if i.resetStore != nil {
			_ = next.Close()
		}
		return err
	}
	i.store = next
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
