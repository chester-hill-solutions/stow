package runthrough

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (a *Adapter) localVersion(ctx context.Context, bucket, key string) string {
	version, _ := a.localVersionStrict(ctx, bucket, key)
	return version
}

func (a *Adapter) localVersionStrict(ctx context.Context, bucket, key string) (string, error) {
	meta, err := a.local.HeadObject(ctx, bucket, key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return objectVersion(meta), nil
}

func objectVersion(meta *storage.ObjectMeta) string {
	if meta == nil {
		return ""
	}
	if meta.VersionID != "" {
		return meta.VersionID
	}
	return meta.ETag
}

func (a *Adapter) enqueueIntent(ctx context.Context, operation OutboxOperation, bucket, key string, versions ...string) (OutboxEntry, error) {
	unlock := a.outboxLocks.lock(outboxIdentity(bucket, key))
	defer unlock()
	return a.enqueueIntentLocked(ctx, operation, bucket, key, versions...)
}

func (a *Adapter) enqueueIntentLocked(ctx context.Context, operation OutboxOperation, bucket, key string, versions ...string) (OutboxEntry, error) {
	version := ""
	if len(versions) > 0 {
		version = versions[0]
	}
	if operation == OutboxPut && version == "" {
		meta, err := a.local.HeadObject(ctx, bucket, key)
		if err != nil {
			return OutboxEntry{}, err
		}
		version = objectVersion(meta)
	}
	entry := OutboxEntry{Operation: operation, Bucket: bucket, Key: key, Version: version, CreatedAt: time.Now().UTC()}
	return a.outbox.Enqueue(entry)
}

func (a *Adapter) enqueuePreparedIntentLocked(operation OutboxOperation, bucket, key, previousVersion string) (OutboxEntry, error) {
	entry := OutboxEntry{
		Operation:       operation,
		Bucket:          bucket,
		Key:             key,
		PreviousVersion: previousVersion,
		Prepared:        true,
		CreatedAt:       time.Now().UTC(),
	}
	return a.outbox.Enqueue(entry)
}

func (a *Adapter) propagateEntry(ctx context.Context, entry OutboxEntry) error {
	switch entry.Operation {
	case OutboxPut:
		rc, meta, err := a.local.GetObject(ctx, entry.Bucket, entry.Key)
		if err != nil {
			return err
		}
		if entry.Version != "" && entry.Version != objectVersion(meta) {
			_ = rc.Close()
			return ErrOutboxVersionConflict
		}
		defer rc.Close()
		return a.upstream.PutObject(ctx, entry.Bucket, entry.Key, rc, storage.PutOptions{
			ContentType: meta.ContentType,
			Metadata:    meta.Metadata,
		})
	case OutboxDelete:
		if entry.Version != "" {
			current, err := a.local.HeadObject(ctx, entry.Bucket, entry.Key)
			if err == nil && objectVersion(current) != entry.Version {
				return ErrOutboxVersionConflict
			}
			if err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
				return err
			}
		}
		return a.upstream.DeleteObject(ctx, entry.Bucket, entry.Key)
	default:
		return NewDeterministicUpstreamError(fmt.Errorf("unsupported outbox operation %q", entry.Operation))
	}
}

func (a *Adapter) completeIntent(ctx context.Context, entry OutboxEntry) error {
	unlock := a.outboxLocks.lock(outboxIdentity(entry.Bucket, entry.Key))
	defer unlock()
	return a.completeIntentWithScheduleLocked(ctx, entry, true)
}

func (a *Adapter) completeIntentLocked(ctx context.Context, entry OutboxEntry) error {
	return a.completeIntentWithScheduleLocked(ctx, entry, false)
}

func (a *Adapter) completeIntentWithScheduleLocked(ctx context.Context, entry OutboxEntry, respectSchedule bool) error {
	pending := a.outbox.Pending()
	current, ok := findPendingEntry(pending, entry.ID)
	if !ok || current.Terminal || (respectSchedule && !current.NextAttempt.IsZero() && current.NextAttempt.After(time.Now())) || !isFirstPendingForKey(pending, current) {
		return nil
	}
	if err := a.propagateEntry(ctx, current); err != nil {
		retryAt := time.Time{}
		if IsTransientRetry(err) {
			retryAt = time.Now().Add(outboxRetryDelay(current.Attempts))
		}
		markErr := a.outbox.MarkFailure(current.ID, err, retryAt)
		if markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	return a.outbox.MarkSuccess(current.ID)
}

func outboxRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<uint(attempts-1)) * time.Second
}

// RetryPending retries due outbox entries. It is safe to call from a worker or
// at startup; entries that are not due remain queued.
func (a *Adapter) RetryPending(ctx context.Context) error {
	now := time.Now()
	blocked := make(map[string]bool)
	var firstErr error
	for _, entry := range a.outbox.Pending() {
		key := outboxIdentity(entry.Bucket, entry.Key)
		if blocked[key] {
			continue
		}
		block, err := a.retryEntry(ctx, entry, now)
		if block {
			blocked[key] = true
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *Adapter) retryEntry(ctx context.Context, entry OutboxEntry, now time.Time) (bool, error) {
	if entry.Terminal || !outboxEntryDue(entry, now) {
		return true, nil
	}
	key := outboxIdentity(entry.Bucket, entry.Key)
	unlock := a.outboxLocks.lock(key)
	defer unlock()

	pending := a.outbox.Pending()
	current, ok := findPendingEntry(pending, entry.ID)
	if !ok {
		return false, nil
	}
	if current.Prepared {
		ready, err := a.reconcilePreparedEntry(ctx, current)
		if err != nil || !ready {
			return err != nil, err
		}
		pending = a.outbox.Pending()
		current, ok = findPendingEntry(pending, current.ID)
		if !ok {
			return false, nil
		}
	}
	if !a.upstreamEnabled(current.Bucket) || current.Terminal || !outboxEntryDue(current, now) || !isFirstPendingForKey(pending, current) {
		return true, nil
	}
	if err := a.completeIntentLocked(ctx, current); err != nil {
		return true, err
	}
	remainingState := a.outbox.Pending()
	remaining, stillPending := findPendingEntry(remainingState, current.ID)
	return stillPending && (!outboxEntryDue(remaining, time.Now()) || !isFirstPendingForKey(remainingState, remaining)), nil
}

func (a *Adapter) reconcilePreparedEntry(ctx context.Context, entry OutboxEntry) (bool, error) {
	switch entry.Operation {
	case OutboxPut:
		meta, err := a.local.HeadObject(ctx, entry.Bucket, entry.Key)
		if err != nil {
			if errors.Is(err, storage.ErrObjectNotFound) {
				return false, a.outbox.Discard(entry.ID)
			}
			return false, err
		}
		currentVersion := objectVersion(meta)
		if entry.PreviousVersion != "" && currentVersion == entry.PreviousVersion {
			return false, a.outbox.Discard(entry.ID)
		}
		return true, a.outbox.Commit(entry.ID, currentVersion)
	case OutboxDelete:
		meta, err := a.local.HeadObject(ctx, entry.Bucket, entry.Key)
		if err == nil {
			if entry.Version != "" && objectVersion(meta) == entry.Version {
				return false, a.outbox.Discard(entry.ID)
			}
			return false, a.markPreparedConflict(entry)
		}
		if !errors.Is(err, storage.ErrObjectNotFound) {
			return false, err
		}
		return true, a.outbox.Commit(entry.ID, entry.Version)
	default:
		return false, a.markPreparedConflict(entry)
	}
}

func (a *Adapter) markPreparedConflict(entry OutboxEntry) error {
	conflict := NewDeterministicUpstreamError(ErrOutboxVersionConflict)
	if err := a.outbox.MarkFailure(entry.ID, conflict, time.Time{}); err != nil {
		return errors.Join(conflict, err)
	}
	return conflict
}

func outboxEntryDue(entry OutboxEntry, now time.Time) bool {
	return entry.NextAttempt.IsZero() || !entry.NextAttempt.After(now)
}

func findPendingEntry(entries []OutboxEntry, id string) (OutboxEntry, bool) {
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return OutboxEntry{}, false
}

func isFirstPendingForKey(entries []OutboxEntry, target OutboxEntry) bool {
	for _, entry := range entries {
		if entry.Bucket != target.Bucket || entry.Key != target.Key || entry.ID == target.ID {
			continue
		}
		if outboxEntryBefore(entry, target) {
			return false
		}
	}
	return true
}

func outboxEntryBefore(left, right OutboxEntry) bool {
	leftSequence, leftOK := outboxSequenceOK(left.ID)
	rightSequence, rightOK := outboxSequenceOK(right.ID)
	if leftOK && rightOK && leftSequence != rightSequence {
		return leftSequence < rightSequence
	}
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID < right.ID
	}
	return left.CreatedAt.Before(right.CreatedAt)
}
