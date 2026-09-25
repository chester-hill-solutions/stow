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

func (a *Adapter) prepareIntent(operation OutboxOperation, bucket, key, previousVersion string, source ...string) (OutboxEntry, error) {
	if a.coordinatedOutbox == nil {
		return OutboxEntry{}, ErrDurableOutboxRequired
	}
	entry := OutboxEntry{
		Operation:       operation,
		Bucket:          bucket,
		Key:             key,
		PreviousVersion: previousVersion,
		Prepared:        true,
		CreatedAt:       time.Now().UTC(),
	}
	if len(source) > 0 {
		entry.SourceBucket = source[0]
	}
	if len(source) > 1 {
		entry.SourceKey = source[1]
	}
	if owned, ok := a.coordinatedOutbox.(OwnedPreparedOutbox); ok {
		return owned.PrepareOwned(entry, a.claimOwner, defaultOutboxClaimLease)
	}
	return a.coordinatedOutbox.Prepare(entry)
}

func (a *Adapter) commitPreparedIntent(entry OutboxEntry, version string) (OutboxEntry, error) {
	if a.coordinatedOutbox == nil {
		return OutboxEntry{}, ErrDurableOutboxRequired
	}
	if owned, ok := a.coordinatedOutbox.(OwnedPreparedOutbox); ok && entry.PreparedOwner != "" {
		owner := entry.PreparedOwner
		return owned.CommitPrepared(entry.ID, owner, entry.PreparedToken, version)
	}
	return a.coordinatedOutbox.Commit(entry.ID, version)
}

func (a *Adapter) discardPreparedIntent(entry OutboxEntry) error {
	if a.coordinatedOutbox == nil {
		return ErrDurableOutboxRequired
	}
	if owned, ok := a.coordinatedOutbox.(OwnedPreparedOutbox); ok && entry.PreparedOwner != "" {
		return owned.DiscardPreparedOwned(entry.ID, entry.PreparedOwner, entry.PreparedToken)
	}
	return a.coordinatedOutbox.DiscardPrepared(entry.ID)
}

func (a *Adapter) enqueuePreparedIntentLocked(operation OutboxOperation, bucket, key, previousVersion string, source ...string) (OutboxEntry, error) {
	return a.prepareIntent(operation, bucket, key, previousVersion, source...)
}

func (a *Adapter) propagateEntry(ctx context.Context, entry OutboxEntry) error {
	switch entry.Operation {
	case OutboxPut, OutboxCopy, OutboxMultipart:
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
	pending := a.orderingEntries()
	current, ok := findPendingEntry(a.outbox.Pending(), entry.ID)
	if !ok || current.Terminal || (respectSchedule && !current.NextAttempt.IsZero() && current.NextAttempt.After(time.Now())) || !isFirstPendingForKey(pending, current) {
		return nil
	}
	claim, err := a.claimPropagation(current)
	if err != nil {
		return err
	}
	if !claim.acquired {
		return nil
	}
	if err := claim.propagate(ctx, a); err != nil {
		retryAt := time.Time{}
		if IsTransientRetry(err) {
			retryAt = time.Now().Add(outboxRetryDelay(current.Attempts))
		}
		markErr := claim.failure(err, retryAt)
		if claim.provider == nil {
			markErr = a.outbox.MarkFailure(current.ID, err, retryAt)
		}
		if markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	if claim.provider == nil {
		return a.outbox.MarkSuccess(current.ID)
	}
	return claim.success()
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
	recoveryErr := a.RecoverPrepared(ctx)
	now := time.Now()
	blocked := make(map[string]bool)
	for _, entry := range a.preparedEntries() {
		blocked[outboxIdentity(entry.Bucket, entry.Key)] = true
	}
	var firstErr error
	if recoveryErr != nil {
		firstErr = recoveryErr
	}
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

func (a *Adapter) RecoverPrepared(ctx context.Context) error {
	if a.coordinatedOutbox == nil {
		return nil
	}
	entries, err := a.preparedEntriesWithError()
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		if entry.PreparedOwner != "" && (entry.PreparedUntil.After(time.Now().UTC()) || outboxOwnerAlive(entry.PreparedOwner)) {
			continue
		}
		unlock := a.outboxLocks.lock(outboxIdentity(entry.Bucket, entry.Key))
		_, err := a.reconcilePreparedEntryLocked(ctx, entry)
		unlock()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *Adapter) rejectPreparedKey(bucket, key string) error {
	entries, err := a.preparedEntriesWithError()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Bucket == bucket && entry.Key == key {
			return ErrOutboxPreparedUnresolved
		}
	}
	return nil
}

func (a *Adapter) preparedEntries() []OutboxEntry {
	entries, _ := a.preparedEntriesWithError()
	return entries
}

func (a *Adapter) preparedEntriesWithError() ([]OutboxEntry, error) {
	if a.coordinatedOutbox == nil {
		return nil, nil
	}
	if snapshot, ok := a.outbox.(SnapshotOutbox); ok {
		return snapshot.PreparedSnapshot()
	}
	return a.coordinatedOutbox.Prepared(), nil
}

func (a *Adapter) orderingEntries() []OutboxEntry {
	entries := append([]OutboxEntry(nil), a.outbox.Pending()...)
	return append(entries, a.preparedEntries()...)
}

func (a *Adapter) retryEntry(ctx context.Context, entry OutboxEntry, now time.Time) (bool, error) {
	if entry.Terminal || !outboxEntryDue(entry, now) {
		return true, nil
	}
	key := outboxIdentity(entry.Bucket, entry.Key)
	unlock := a.outboxLocks.lock(key)
	defer unlock()

	pending := a.orderingEntries()
	current, ok := findPendingEntry(a.outbox.Pending(), entry.ID)
	if !ok {
		return false, nil
	}
	if !a.upstreamEnabled(current.Bucket) || current.Terminal || !outboxEntryDue(current, now) || !isFirstPendingForKey(pending, current) {
		return true, nil
	}
	if err := a.completeIntentLocked(ctx, current); err != nil {
		return true, err
	}
	remainingState := a.orderingEntries()
	remaining, stillPending := findPendingEntry(a.outbox.Pending(), current.ID)
	return stillPending && (!outboxEntryDue(remaining, time.Now()) || !isFirstPendingForKey(remainingState, remaining)), nil
}

func (a *Adapter) reconcilePreparedAfterError(ctx context.Context, entry OutboxEntry, cause error) error {
	_, recoveryErr := a.reconcilePreparedEntryLocked(ctx, entry)
	return errors.Join(cause, recoveryErr)
}

func (a *Adapter) reconcilePreparedEntryLocked(ctx context.Context, entry OutboxEntry) (bool, error) {
	switch entry.Operation {
	case OutboxPut, OutboxCopy, OutboxMultipart:
		meta, err := a.local.HeadObject(ctx, entry.Bucket, entry.Key)
		if err != nil {
			if errors.Is(err, storage.ErrObjectNotFound) {
				return false, a.discardPreparedIntent(entry)
			}
			return false, err
		}
		currentVersion := objectVersion(meta)
		if entry.PreviousVersion != "" && currentVersion == entry.PreviousVersion {
			return false, a.discardPreparedIntent(entry)
		}
		_, err = a.commitPreparedIntent(entry, currentVersion)
		return true, err
	case OutboxDelete:
		meta, err := a.local.HeadObject(ctx, entry.Bucket, entry.Key)
		if err == nil {
			if entry.Version != "" && objectVersion(meta) == entry.Version {
				return false, a.discardPreparedIntent(entry)
			}
			return false, ErrOutboxPreparedUnresolved
		}
		if !errors.Is(err, storage.ErrObjectNotFound) {
			return false, err
		}
		_, err = a.commitPreparedIntent(entry, entry.Version)
		return true, err
	default:
		return false, ErrOutboxPreparedUnresolved
	}
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
