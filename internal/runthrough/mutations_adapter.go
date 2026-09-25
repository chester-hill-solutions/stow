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
	var prepared OutboxEntry
	version := ""
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
		defer unlock()
		if err := a.rejectPreparedKey(bucket, key); err != nil {
			return err
		}
		var err error
		version, err = a.localVersionStrict(ctx, bucket, key)
		if err != nil {
			return err
		}
		prepared, err = a.enqueuePreparedIntentLocked(OutboxDelete, bucket, key, version)
		if err != nil {
			return err
		}
	}
	localErr := a.local.DeleteObject(ctx, bucket, key)
	if localErr != nil {
		if errors.Is(localErr, storage.ErrObjectNotFound) {
			if prepared.ID != "" {
				return a.discardPreparedIntent(prepared.ID)
			}
			return nil
		}
		if prepared.ID != "" {
			return errors.Join(localErr, a.discardPreparedIntent(prepared.ID))
		}
		return localErr
	}
	a.invalidateCache(ctx, bucket, key)
	if action == writePropagate {
		if _, err := a.commitPreparedIntent(prepared.ID, version); err != nil {
			return err
		}
		return a.completeIntentLocked(ctx, prepared)
	}
	return nil
}

func (a *Adapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	if err := a.requireDurableOutbox(action); err != nil {
		return nil, err
	}
	var prepared []OutboxEntry
	if action == writePropagate {
		identities := make([]string, 0, len(keys))
		seen := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			identities = append(identities, outboxIdentity(bucket, key))
		}
		unlock := a.outboxLocks.lockMany(identities)
		defer unlock()
		for _, key := range keys {
			if err := a.rejectPreparedKey(bucket, key); err != nil {
				return nil, err
			}
		}
		var err error
		prepared, err = a.prepareDeleteIntents(ctx, bucket, keys)
		if err != nil {
			return nil, err
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
	if localErr != nil {
		return deleted, localErr
	}

	entries, err := a.commitDeleteIntents(prepared, deleted)
	if err != nil {
		return deleted, err
	}
	return deleted, a.propagateIntents(ctx, entries)
}

func (a *Adapter) prepareDeleteIntents(ctx context.Context, bucket string, keys []string) ([]OutboxEntry, error) {
	entries := make([]OutboxEntry, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		previousVersion, err := a.localVersionStrict(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		entry, err := a.enqueuePreparedIntentLocked(OutboxDelete, bucket, key, previousVersion)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *Adapter) commitDeleteIntents(prepared []OutboxEntry, deleted []string) ([]OutboxEntry, error) {
	deletedSet := make(map[string]struct{}, len(deleted))
	for _, key := range deleted {
		deletedSet[key] = struct{}{}
	}
	committed := make([]OutboxEntry, 0, len(deleted))
	for _, entry := range prepared {
		if _, ok := deletedSet[entry.Key]; !ok {
			if err := a.outbox.Discard(entry.ID); err != nil {
				return nil, err
			}
			continue
		}
		committedEntry, err := a.commitPreparedIntent(entry.ID, entry.PreviousVersion)
		if err != nil {
			return nil, err
		}
		committed = append(committed, committedEntry)
	}
	return committed, nil
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
	var prepared OutboxEntry
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(dstBucket, dstKey))
		defer unlock()
		if err := a.rejectPreparedKey(dstBucket, dstKey); err != nil {
			return nil, err
		}
		previousVersion, err := a.localVersionStrict(ctx, dstBucket, dstKey)
		if err != nil {
			return nil, err
		}
		prepared, err = a.enqueuePreparedIntentLocked(OutboxPut, dstBucket, dstKey, previousVersion)
		if err != nil {
			return nil, err
		}
	}

	meta, err := a.local.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		if prepared.ID != "" {
			return nil, errors.Join(err, a.discardPreparedIntent(prepared.ID))
		}
		return nil, err
	}
	a.invalidateCache(ctx, dstBucket, dstKey)
	if action == writePropagate {
		if _, err := a.commitPreparedIntent(prepared.ID, objectVersion(meta)); err != nil {
			return meta, err
		}
		if err := a.completeIntentLocked(ctx, prepared); err != nil {
			return meta, err
		}
	}
	return meta, nil
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
	var prepared OutboxEntry
	if action == writePropagate {
		unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
		defer unlock()
		if err := a.rejectPreparedKey(bucket, key); err != nil {
			return nil, err
		}
		previousVersion, err := a.localVersionStrict(ctx, bucket, key)
		if err != nil {
			return nil, err
		}
		prepared, err = a.enqueuePreparedIntentLocked(OutboxPut, bucket, key, previousVersion)
		if err != nil {
			return nil, err
		}
	}

	meta, err := a.local.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		if prepared.ID != "" {
			return nil, errors.Join(err, a.discardPreparedIntent(prepared.ID))
		}
		return nil, err
	}
	if action == writePropagate {
		if _, err := a.commitPreparedIntent(prepared.ID, objectVersion(meta)); err != nil {
			return meta, err
		}
		if err := a.completeIntentLocked(ctx, prepared); err != nil {
			return meta, err
		}
	}
	return meta, nil
}

func (a *Adapter) multipartTarget(ctx context.Context, uploadID string) (string, error) {
	upload, err := a.local.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return "", err
	}
	return upload.Bucket, nil
}
