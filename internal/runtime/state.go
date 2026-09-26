package runtime

import (
	"context"
	"fmt"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (i *Instance) objectQuotaFits(target string, objectDelta int64) bool {
	reserved := i.reservedObjects
	if _, ok := i.reservedTargets[target]; ok {
		reserved--
	}
	return i.usage.Objects+objectDelta+reserved <= i.options.MaxObjects
}

func (i *Instance) bytesQuotaFits(oldSize, newSize int64) bool {
	return i.usage.Bytes+i.reservedBytes-oldSize+newSize <= i.options.MaxBytes
}

func (i *Instance) hasTargetReservation(target string) bool {
	_, ok := i.reservedTargets[target]
	return ok
}

func (i *Instance) consumeTargetReservation(target string) {
	if _, ok := i.reservedTargets[target]; !ok {
		return
	}
	delete(i.reservedTargets, target)
	i.reservedObjects--
}

func (i *Instance) addMultipartTarget(target string) {
	i.multipartTargets[target]++
}

func (i *Instance) removeMultipartTarget(target string) {
	count := i.multipartTargets[target]
	if count <= 1 {
		delete(i.multipartTargets, target)
		return
	}
	i.multipartTargets[target] = count - 1
}

// reserveTarget claims one object-count slot for a multipart target that has
// no committed object yet, and reports whether it did. Callers decide what
// running out of room means: initialization fails the open, while a live
// reconciliation simply leaves the reservation unmade.
func (i *Instance) reserveTarget(target string) bool {
	if i.hasTargetReservation(target) || !i.objectQuotaFits(target, 0) {
		return false
	}
	i.reservedTargets[target] = struct{}{}
	i.reservedObjects++
	return true
}

func (i *Instance) reconcileTargetReservation(target string, objectExists bool) {
	if i.multipartTargets[target] == 0 || objectExists {
		i.consumeTargetReservation(target)
		return
	}
	i.reserveTarget(target)
}

func (i *Instance) initialize(ctx context.Context) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	buckets, err := i.store.ListBuckets(ctx)
	if err != nil {
		return err
	}
	for _, bucket := range buckets {
		if err := i.initializeBucket(ctx, bucket.Name); err != nil {
			return err
		}
	}
	if i.usage.Objects > i.options.MaxObjects || i.usage.Bytes > i.options.MaxBytes || i.reservedObjects > i.options.MaxObjects || i.reservedBytes > i.options.MaxBytes {
		return ErrQuotaExceeded
	}
	return nil
}

func (i *Instance) initializeBucket(ctx context.Context, bucket string) error {
	if err := i.initializeObjects(ctx, bucket); err != nil {
		return err
	}
	// Multipart reconciliation is skipped when the environment has no multipart
	// capability. It used to run unconditionally, which meant a store that serves
	// no multipart could not be OPENED as soon as it held a bucket: the
	// capability gated the operations and not the initialization, so it was
	// decorative. An empty store hid it, because the failing case needs a bucket
	// and a fresh store has none.
	if !i.multipartEnabled {
		return nil
	}
	return i.initializeMultipart(ctx, bucket)
}

func (i *Instance) initializeObjects(ctx context.Context, bucket string) error {
	continuation := ""
	for {
		result, err := i.store.ListObjectsV2(ctx, bucket, storage.ListOptions{ContinuationToken: continuation, MaxKeys: 1000})
		if err != nil {
			return err
		}
		for _, object := range result.Objects {
			if object.Size < 0 || i.usage.Bytes > i.options.MaxBytes-object.Size {
				return ErrQuotaExceeded
			}
			i.usage.Objects++
			i.usage.Bytes += object.Size
		}
		if !result.IsTruncated {
			return nil
		}
		if result.NextContinuationToken == "" || result.NextContinuationToken == continuation {
			return fmt.Errorf("runtime store returned an invalid continuation token for bucket %q", bucket)
		}
		continuation = result.NextContinuationToken
	}
}

func (i *Instance) initializeMultipart(ctx context.Context, bucket string) error {
	markers := storage.MultipartListOptions{MaxUploads: 1000}
	for {
		result, err := i.store.ListMultipartUploads(ctx, bucket, markers)
		if err != nil {
			return err
		}
		for _, upload := range result.Uploads {
			if err := i.initializeMultipartUpload(ctx, upload); err != nil {
				return err
			}
		}
		if !result.IsTruncated {
			return nil
		}
		if result.NextKeyMarker == markers.KeyMarker && result.NextUploadIDMarker == markers.UploadIDMarker {
			return fmt.Errorf("runtime store returned an invalid multipart marker for bucket %q", bucket)
		}
		markers.KeyMarker = result.NextKeyMarker
		markers.UploadIDMarker = result.NextUploadIDMarker
	}
}

func (i *Instance) initializeMultipartUpload(ctx context.Context, upload storage.MultipartUpload) error {
	parts, err := i.store.ListParts(ctx, upload.UploadID)
	if err != nil {
		return err
	}
	partSizes := make(map[int]int64, len(parts))
	var uploadBytes int64
	for _, part := range parts {
		if part.Size < 0 || uploadBytes > i.options.MaxBytes-part.Size {
			return ErrQuotaExceeded
		}
		partSizes[part.PartNumber] = part.Size
		uploadBytes += part.Size
	}
	if i.reservedBytes > i.options.MaxBytes-uploadBytes {
		return ErrQuotaExceeded
	}
	target := objectTarget(upload.Bucket, upload.Key)
	i.multipart[upload.UploadID] = multipartUsage{upload: upload, parts: partSizes}
	i.reservedBytes += uploadBytes
	i.addMultipartTarget(target)
	if i.multipartTargets[target] != 1 {
		return nil
	}
	_, exists, err := i.objectSize(ctx, upload.Bucket, upload.Key)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if !i.reserveTarget(target) {
		return ErrQuotaExceeded
	}
	return nil
}
