package runthrough

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (a *Adapter) localVersion(ctx context.Context, bucket, key string) string {
	meta, err := a.local.HeadObject(ctx, bucket, key)
	if err != nil {
		return ""
	}
	return meta.ETag
}

func (a *Adapter) enqueueIntent(ctx context.Context, operation OutboxOperation, bucket, key string, versions ...string) (OutboxEntry, error) {
	version := ""
	if len(versions) > 0 {
		version = versions[0]
	}
	if operation == OutboxPut && version == "" {
		meta, err := a.local.HeadObject(ctx, bucket, key)
		if err != nil {
			return OutboxEntry{}, err
		}
		version = meta.ETag
	}
	entry := OutboxEntry{Operation: operation, Bucket: bucket, Key: key, Version: version, CreatedAt: time.Now().UTC()}
	if err := a.outbox.Enqueue(entry); err != nil {
		return OutboxEntry{}, err
	}
	pending := a.outbox.Pending()
	for i := len(pending) - 1; i >= 0; i-- {
		if pending[i].Bucket == bucket && pending[i].Key == key && pending[i].Operation == operation {
			return pending[i], nil
		}
	}
	return OutboxEntry{}, fmt.Errorf("outbox entry was not retained")
}

func (a *Adapter) propagateEntry(ctx context.Context, entry OutboxEntry) error {
	switch entry.Operation {
	case OutboxPut:
		rc, meta, err := a.local.GetObject(ctx, entry.Bucket, entry.Key)
		if err != nil {
			return err
		}
		if entry.Version != "" && entry.Version != meta.ETag {
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
			if err == nil && current.ETag != entry.Version {
				return ErrOutboxVersionConflict
			}
			if err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
				return err
			}
		}
		return a.upstream.DeleteObject(ctx, entry.Bucket, entry.Key)
	default:
		return fmt.Errorf("unsupported outbox operation %q", entry.Operation)
	}
}

func (a *Adapter) completeIntent(ctx context.Context, entry OutboxEntry) error {
	if err := a.propagateEntry(ctx, entry); err != nil {
		_ = a.outbox.MarkFailure(entry.ID, err, time.Now().Add(outboxRetryDelay(entry.Attempts)))
		return err
	}
	return a.outbox.MarkSuccess(entry.ID)
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
	for _, entry := range a.outbox.Pending() {
		if !entry.NextAttempt.IsZero() && entry.NextAttempt.After(now) {
			continue
		}
		if !a.upstreamEnabled(entry.Bucket) {
			continue
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}
