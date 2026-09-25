package runthrough_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
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

type postCommitErrorUpstream struct {
	*mockUpstream
	failNextPut bool
}

func (u *postCommitErrorUpstream) PutObject(ctx context.Context, bucket, key string, body io.Reader, options storage.PutOptions) error {
	if err := u.mockUpstream.PutObject(ctx, bucket, key, body, options); err != nil {
		return err
	}
	if u.failNextPut {
		u.failNextPut = false
		return runthrough.NewTransientUpstreamError(errors.New("upstream acknowledgement lost"))
	}
	return nil
}

func TestRetryReconcilesCommittedUpstreamPut(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put local: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key", Version: meta.VersionID}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	up := &postCommitErrorUpstream{mockUpstream: newMockUpstream(), failNextPut: true}
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err == nil {
		t.Fatal("expected first acknowledgement failure")
	}
	time.Sleep(1100 * time.Millisecond)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("reconciled retry: %v", err)
	}
	if up.putCalls != 1 {
		t.Fatalf("upstream put calls = %d, want one committed call", up.putCalls)
	}
	if len(outbox.Pending()) != 0 {
		t.Fatalf("pending = %+v, want none", outbox.Pending())
	}
}

type postCommitErrorDeleteUpstream struct {
	*mockUpstream
	failNextDelete bool
}

func (u *postCommitErrorDeleteUpstream) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := u.mockUpstream.DeleteObject(ctx, bucket, key); err != nil {
		return err
	}
	if u.failNextDelete {
		u.failNextDelete = false
		return runthrough.NewTransientUpstreamError(errors.New("upstream acknowledgement lost"))
	}
	return nil
}

func TestRetryReconcilesCommittedUpstreamDelete(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put local: %v", err)
	}
	if err := local.DeleteObject(ctx, "bucket", "key"); err != nil {
		t.Fatalf("delete local: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxDelete, Bucket: "bucket", Key: "key", Version: meta.VersionID}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	up := &postCommitErrorDeleteUpstream{mockUpstream: newMockUpstream(), failNextDelete: true}
	if err := up.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{}); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err == nil {
		t.Fatal("expected first delete acknowledgement failure")
	}
	time.Sleep(1100 * time.Millisecond)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("reconciled retry: %v", err)
	}
	if up.delCalls != 1 {
		t.Fatalf("upstream delete calls = %d, want one committed call", up.delCalls)
	}
	if len(outbox.Pending()) != 0 {
		t.Fatalf("pending = %+v, want none", outbox.Pending())
	}
}

type commitThenErrorStore struct {
	*storage.MemoryStore
}

func (s *commitThenErrorStore) PutObject(ctx context.Context, bucket, key string, body io.Reader, options storage.PutOptions) (*storage.ObjectMeta, error) {
	meta, err := s.MemoryStore.PutObject(ctx, bucket, key, body, options)
	if err != nil {
		return nil, err
	}
	return meta, errors.New("post-commit cleanup failed")
}

func TestMultipartIntentRetainsOperationIdentity(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	upload, err := local.CreateMultipartUpload(ctx, "bucket", "multipart-object")
	if err != nil {
		t.Fatalf("create multipart: %v", err)
	}
	part, err := local.UploadPart(ctx, upload.UploadID, 1, bytes.NewReader([]byte("part")))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	up := newMockUpstream()
	up.putErr = errors.New("upstream unavailable")
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if _, err := adapter.CompleteMultipartUpload(ctx, upload.UploadID, []storage.PartInfo{*part}); err == nil {
		t.Fatal("expected multipart propagation error")
	}
	pending := outbox.Pending()
	if len(pending) != 1 || pending[0].Operation != runthrough.OutboxMultipart {
		t.Fatalf("multipart intent = %+v", pending)
	}
}

func TestCopyIntentRetainsOperationIdentity(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := local.PutObject(ctx, "bucket", "source", bytes.NewReader([]byte("value")), storage.PutOptions{}); err != nil {
		t.Fatalf("put source: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	up := newMockUpstream()
	up.putErr = errors.New("upstream unavailable")
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if _, err := adapter.CopyObject(ctx, "bucket", "source", "bucket", "destination"); err == nil {
		t.Fatal("expected copy propagation error")
	}
	pending := outbox.Pending()
	if len(pending) != 1 || pending[0].Operation != runthrough.OutboxCopy || pending[0].SourceBucket != "bucket" || pending[0].SourceKey != "source" {
		t.Fatalf("copy intent = %+v", pending)
	}
}

func TestDeleteObjectsDiscardsPreparedIntentForMissingKey(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, newMockUpstream(), outbox)
	deleted, err := adapter.DeleteObjects(ctx, "bucket", []string{"missing"})
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if len(deleted) != 0 || len(outbox.Prepared()) != 0 || len(outbox.Pending()) != 0 {
		t.Fatalf("state = deleted %d prepared %d active %d", len(deleted), len(outbox.Prepared()), len(outbox.Pending()))
	}
}

func TestLocalCommitErrorLeavesRecoverableIntent(t *testing.T) {
	ctx := context.Background()
	local := &commitThenErrorStore{MemoryStore: storage.NewMemoryStore()}
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up, outbox)
	if _, err := adapter.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{}); err == nil {
		t.Fatal("expected post-commit error")
	}
	if len(outbox.Prepared()) != 0 || len(outbox.Pending()) != 1 {
		t.Fatalf("outbox state = prepared %d active %d", len(outbox.Prepared()), len(outbox.Pending()))
	}
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry recovered intent: %v", err)
	}
	if up.putCalls != 1 || len(outbox.Pending()) != 0 {
		t.Fatalf("recovery calls/state = %d/%d", up.putCalls, len(outbox.Pending()))
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
