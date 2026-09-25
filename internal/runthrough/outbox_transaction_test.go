package runthrough_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestFileOutboxPersistsPreparedCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	entry, err := outbox.Prepare(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(outbox.Pending()) != 0 || len(outbox.Prepared()) != 1 {
		t.Fatalf("prepared state = pending %d prepared %d", len(outbox.Pending()), len(outbox.Prepared()))
	}
	if _, err := outbox.Commit(entry.ID, "version-1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	reopened, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	pending := reopened.Pending()
	if len(pending) != 1 || pending[0].Prepared || pending[0].Version != "version-1" {
		t.Fatalf("reopened pending = %+v", pending)
	}
}

func TestOutboxInspectionSnapshotsActiveAndPreparedEntries(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "active"}); err != nil {
		t.Fatalf("enqueue active: %v", err)
	}
	if _, err := outbox.Prepare(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "prepared"}); err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, newMockUpstream(), outbox)
	pending, terminal := adapter.OutboxStats()
	if pending != 1 || terminal != 0 {
		t.Fatalf("outbox stats = %d/%d, want 1/0", pending, terminal)
	}
	if prepared := adapter.OutboxPreparedStats(); prepared != 1 {
		t.Fatalf("prepared stats = %d, want 1", prepared)
	}
	if len(adapter.OutboxEntries()) != 1 || len(adapter.OutboxPreparedEntries()) != 1 {
		t.Fatalf("inspection entries = %d active/%d prepared", len(adapter.OutboxEntries()), len(adapter.OutboxPreparedEntries()))
	}
}

func TestFileOutboxPersistsPreparedIntentAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	entry, err := outbox.Prepare(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatalf("close outbox: %v", err)
	}
	reopened, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	if len(reopened.Pending()) != 0 || len(reopened.Prepared()) != 1 {
		t.Fatalf("reopened state = pending %d prepared %d", len(reopened.Pending()), len(reopened.Prepared()))
	}
	if err := reopened.DiscardPrepared(entry.ID); err != nil {
		t.Fatalf("discard prepared: %v", err)
	}
}

func TestRetryPendingCommitsPreparedIntentAfterLocalMutation(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Prepare(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
		Prepared:  true,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("enqueue prepared put: %v", err)
	}
	if _, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("committed")), storage.PutOptions{}); err != nil {
		t.Fatalf("local put: %v", err)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry pending: %v", err)
	}
	if up.putCalls != 1 {
		t.Fatalf("upstream put calls = %d, want 1", up.putCalls)
	}
	if pending := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("pending entries = %+v, want none", pending)
	}
}

func TestRetryPendingDiscardsPreparedIntentWhenLocalMutationDidNotCommit(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	old, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("old")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put old: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	entry, err := outbox.Prepare(runthrough.OutboxEntry{
		Operation:       runthrough.OutboxPut,
		Bucket:          "bucket",
		Key:             "key",
		PreviousVersion: old.VersionID,
		Prepared:        true,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("enqueue prepared put: %v", err)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry pending: %v", err)
	}
	if up.putCalls != 0 {
		t.Fatalf("upstream put calls = %d, want 0", up.putCalls)
	}
	if pending := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("pending entries = %+v, want prepared intent discarded", pending)
	}
	if entry.ID == "" {
		t.Fatal("expected prepared entry id")
	}
}

func TestRetryPendingDiscardsPreparedDeleteWhenLocalObjectRemains(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Prepare(runthrough.OutboxEntry{
		Operation: runthrough.OutboxDelete,
		Bucket:    "bucket",
		Key:       "key",
		Version:   meta.VersionID,
		Prepared:  true,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("enqueue prepared delete: %v", err)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry pending: %v", err)
	}
	if up.delCalls != 0 {
		t.Fatalf("upstream delete calls = %d, want 0", up.delCalls)
	}
	if pending := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("pending entries = %+v, want prepared intent discarded", pending)
	}
}
