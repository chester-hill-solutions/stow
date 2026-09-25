package runthrough

import (
	"context"
	"errors"
	"io"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (a *Adapter) DeleteObject(ctx context.Context, bucket, key string) error {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return err
	}
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
		defer unlock()
	}

	version := ""
	if action == writePropagate {
		version = a.localVersion(ctx, bucket, key)
	}
	if err := a.local.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return err
	}
	a.invalidateCache(ctx, bucket, key)
	switch action {
	case writeSkip:
		return nil
	case writePropagate:
		entry, err := a.enqueueIntentLocked(ctx, OutboxDelete, bucket, key, version)
		if err != nil {
			return err
		}
		return a.completeIntentLocked(ctx, entry)
	default:
		return nil
	}
}

func (a *Adapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return nil, err
	}
	if action == writePropagate {
		identities := make([]string, 0, len(keys))
		for _, key := range keys {
			identities = append(identities, outboxIdentity(bucket, key))
		}
		unlock := a.outboxLocks.lockMany(identities)
		defer unlock()
	}

	versions := map[string]string{}
	if action == writePropagate {
		for _, key := range keys {
			versions[key] = a.localVersion(ctx, bucket, key)
		}
	}
	deleted, localErr := a.local.DeleteObjects(ctx, bucket, keys)
	if a.separateCache {
		for _, key := range deleted {
			a.invalidateCache(ctx, bucket, key)
		}
	}
	if action != writePropagate {
		return deleted, localErr
	}

	// Persist every deletion intent before attempting any upstream delete. A
	// failed enqueue therefore cannot leave a partially propagated batch.
	entries, err := a.enqueueDeleteIntents(ctx, bucket, deleted, versions)
	if err != nil {
		if localErr != nil {
			return deleted, errors.Join(localErr, err)
		}
		return deleted, err
	}
	if localErr != nil {
		return deleted, localErr
	}
	return deleted, a.propagateIntents(ctx, entries)
}

func (a *Adapter) enqueueDeleteIntents(ctx context.Context, bucket string, deleted []string, versions map[string]string) ([]OutboxEntry, error) {
	entries := make([]OutboxEntry, 0, len(deleted))
	for _, key := range deleted {
		entry, err := a.enqueueIntentLocked(ctx, OutboxDelete, bucket, key, versions[key])
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *Adapter) propagateIntents(ctx context.Context, entries []OutboxEntry) error {
	for _, entry := range entries {
		if err := a.completeIntentLocked(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	action := a.decideUpstreamWrite(dstBucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return nil, err
	}
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(dstBucket, dstKey))
		defer unlock()
	}

	meta, err := a.local.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		return nil, err
	}
	a.invalidateCache(ctx, dstBucket, dstKey)
	switch action {
	case writeSkip:
		return meta, nil
	case writePropagate:
		entry, err := a.enqueueIntentLocked(ctx, OutboxPut, dstBucket, dstKey)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntentLocked(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		return meta, nil
	}
}

func (a *Adapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	return a.local.CreateMultipartUpload(ctx, bucket, key)
}

func (a *Adapter) GetMultipartUpload(ctx context.Context, uploadID string) (*storage.MultipartUpload, error) {
	return a.local.GetMultipartUpload(ctx, uploadID)
}

func (a *Adapter) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	return a.local.UploadPart(ctx, uploadID, partNumber, body)
}

func (a *Adapter) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	upload, err := a.local.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	bucket, key := upload.Bucket, upload.Key
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return nil, err
	}
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
		defer unlock()
	}

	meta, err := a.local.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		return nil, err
	}
	switch action {
	case writeSkip:
		return meta, nil
	case writePropagate:
		entry, err := a.enqueueIntentLocked(ctx, OutboxPut, meta.Bucket, meta.Key)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntentLocked(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		return meta, nil
	}
}

func (a *Adapter) multipartTarget(ctx context.Context, uploadID string) (string, error) {
	upload, err := a.local.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return "", err
	}
	return upload.Bucket, nil
}
