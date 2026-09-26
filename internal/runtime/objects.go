package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// The object half of the namespace. Every one of these consults the
// environment's Authority before touching anything, which is what makes the
// answer the same whether a caller arrived over S3 or in process.

func (i *Instance) PutObject(ctx context.Context, bucket, key string, data []byte, options PutOptions) (Object, error) {
	if err := i.checkContext(ctx); err != nil {
		return Object{}, err
	}
	if err := i.check(authority.ObjectWrite); err != nil {
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
	if err := i.check(authority.ObjectRead); err != nil {
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
	if err := i.check(authority.ObjectRead); err != nil {
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
	if err := i.check(authority.ObjectList); err != nil {
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
	if err := i.check(authority.ObjectDelete); err != nil {
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
	if err := i.check(authority.ObjectDelete); err != nil {
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
	if err := i.check(authority.ObjectWrite); err != nil {
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
