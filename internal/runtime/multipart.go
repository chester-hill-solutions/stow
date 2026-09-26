package runtime

import (
	"bytes"
	"context"
	"io"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (i *Instance) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectWrite); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	target := objectTarget(bucket, key)
	_, exists, err := i.objectSize(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	// A target that already holds a reservation has spent its object slot, so
	// only a first upload for a missing object needs room for one.
	needsObjectSlot := !exists && !i.hasTargetReservation(target)
	if needsObjectSlot && !i.objectQuotaFits(target, 1) {
		return nil, ErrQuotaExceeded
	}
	upload, err := i.store.CreateMultipartUpload(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	clone := *upload
	i.multipart[upload.UploadID] = multipartUsage{upload: clone, parts: make(map[int]int64)}
	i.addMultipartTarget(target)
	if needsObjectSlot {
		// The check above passed under this lock, so the reservation is
		// guaranteed; declining it here would leave the upload uncounted.
		i.reserveTarget(target)
	}
	return &clone, nil
}

func (i *Instance) GetMultipartUpload(ctx context.Context, uploadID string) (*storage.MultipartUpload, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectRead); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	upload, err := i.store.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	clone := *upload
	return &clone, nil
}

func (i *Instance) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectWrite); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	usage, ok := i.multipart[uploadID]
	if !ok {
		return nil, storage.ErrUploadNotFound
	}
	oldSize := usage.parts[partNumber]
	if !i.bytesWithinQuota(oldSize, int64(len(data))) {
		return nil, ErrQuotaExceeded
	}
	part, err := i.store.UploadPart(ctx, uploadID, partNumber, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	usage.parts[partNumber] = part.Size
	i.multipart[uploadID] = usage
	i.reservedBytes += part.Size - oldSize
	clone := *part
	return &clone, nil
}

func (i *Instance) ListPartsPage(ctx context.Context, uploadID string, opts storage.ListPartsOptions) (*storage.ListPartsResult, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectRead); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	if lister, ok := i.store.(interface {
		ListPartsPage(context.Context, string, storage.ListPartsOptions) (*storage.ListPartsResult, error)
	}); ok {
		result, err := lister.ListPartsPage(ctx, uploadID, opts)
		if err != nil {
			return nil, err
		}
		clone := *result
		clone.Parts = append([]storage.PartInfo(nil), result.Parts...)
		return &clone, nil
	}
	parts, err := i.store.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return storage.PaginateParts(parts, opts), nil
}

func (i *Instance) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectWrite); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	uploadUsage, ok := i.multipart[uploadID]
	if !ok {
		return nil, storage.ErrUploadNotFound
	}
	oldSize, exists, err := i.objectSize(ctx, uploadUsage.upload.Bucket, uploadUsage.upload.Key)
	if err != nil {
		return nil, err
	}
	var completedBytes int64
	for _, part := range parts {
		completedBytes += uploadUsage.parts[part.PartNumber]
	}
	if !exists && !i.objectQuotaFits(objectTarget(uploadUsage.upload.Bucket, uploadUsage.upload.Key), 1) {
		return nil, ErrQuotaExceeded
	}
	completedReservation := uploadUsage.bytes()
	if completedReservation > i.reservedBytes {
		return nil, ErrQuotaExceeded
	}
	otherReservedBytes := i.reservedBytes - completedReservation
	if i.usage.Bytes-oldSize+completedBytes+otherReservedBytes > i.options.MaxBytes {
		return nil, ErrQuotaExceeded
	}
	meta, err := i.store.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		return nil, err
	}
	if exists {
		i.usage.Bytes -= oldSize
	} else {
		i.usage.Objects++
	}
	i.usage.Bytes += meta.Size
	i.reservedBytes -= completedReservation
	target := objectTarget(uploadUsage.upload.Bucket, uploadUsage.upload.Key)
	i.removeMultipartTarget(target)
	i.consumeTargetReservation(target)
	i.reconcileTargetReservation(target, true)
	delete(i.multipart, uploadID)
	clone := *meta
	return &clone, nil
}

func (i *Instance) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	if err := i.check(authority.ObjectWrite); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return err
	}
	usage, ok := i.multipart[uploadID]
	if !ok {
		return storage.ErrUploadNotFound
	}
	if err := i.store.AbortMultipartUpload(ctx, uploadID); err != nil {
		return err
	}
	i.reservedBytes -= usage.bytes()
	target := objectTarget(usage.upload.Bucket, usage.upload.Key)
	i.removeMultipartTarget(target)
	i.consumeTargetReservation(target)
	i.reconcileTargetReservation(target, false)
	delete(i.multipart, uploadID)
	return nil
}

func (i *Instance) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectRead); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	parts, err := i.store.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return append([]storage.PartInfo(nil), parts...), nil
}

func (i *Instance) ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	if err := i.check(authority.ObjectRead); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return err
	}
	return i.store.ValidateMultipartUpload(ctx, uploadID, bucket, key)
}

func (i *Instance) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.ObjectList); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkMultipartOpen(); err != nil {
		return nil, err
	}
	result, err := i.store.ListMultipartUploads(ctx, bucket, opts)
	if err != nil {
		return nil, err
	}
	clone := *result
	clone.Uploads = append([]storage.MultipartUpload(nil), result.Uploads...)
	return &clone, nil
}

func (u multipartUsage) bytes() int64 {
	var total int64
	for _, size := range u.parts {
		total += size
	}
	return total
}

func (i *Instance) checkMultipartOpen() error {
	if !i.multipartEnabled {
		return ErrMultipartUnsupported
	}
	return i.checkOpen()
}

func (i *Instance) bytesWithinQuota(oldSize, newSize int64) bool {
	return i.usage.Bytes+i.reservedBytes-oldSize+newSize <= i.options.MaxBytes
}
