package runthrough_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestRetryPendingContinuesAcrossKeys(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	for key, body := range map[string]string{"a": "alpha", "b": "beta"} {
		meta, err := local.PutObject(ctx, "bucket", key, bytes.NewReader([]byte(body)), storage.PutOptions{})
		if err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		if _, err := outbox.Enqueue(runthrough.OutboxEntry{
			Operation: runthrough.OutboxPut,
			Bucket:    "bucket",
			Key:       key,
			Version:   meta.VersionID,
		}); err != nil {
			t.Fatalf("enqueue %s: %v", key, err)
		}
	}

	up := newMockUpstream()
	up.putErrors[objectKey("bucket", "a")] = errors.New("temporary failure")
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err == nil {
		t.Fatal("expected first key retry to fail")
	}
	if _, ok := up.bodies[objectKey("bucket", "b")]; !ok {
		t.Fatal("expected independent key to be retried")
	}
	pending := outbox.Pending()
	if len(pending) != 1 || pending[0].Key != "a" {
		t.Fatalf("pending = %+v, want only failed key a", pending)
	}
}

func TestMemoryOutboxLifecycle(t *testing.T) {
	outbox := runthrough.NewMemoryOutbox()
	entry := runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"}
	if _, err := outbox.Enqueue(entry); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pending := outbox.Pending()
	if len(pending) != 1 || pending[0].ID == "" {
		t.Fatalf("pending = %+v", pending)
	}
	if err := outbox.MarkFailure(pending[0].ID, errors.New("temporary"), time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark failure: %v", err)
	}
	if got := outbox.Pending()[0].LastError; got != "temporary" {
		t.Fatalf("last error = %q", got)
	}
	if err := outbox.MarkFailure(pending[0].ID, runthrough.ErrOutboxVersionConflict, time.Now()); err != nil {
		t.Fatalf("mark terminal failure: %v", err)
	}
	if !outbox.Pending()[0].Terminal {
		t.Fatal("expected version conflict to be terminal")
	}
	if err := outbox.MarkSuccess(pending[0].ID); err != nil {
		t.Fatalf("mark success: %v", err)
	}
	if len(outbox.Pending()) != 0 {
		t.Fatal("expected empty outbox after success")
	}
}

type legacyDurableOutbox struct {
	inner *runthrough.MemoryOutbox
}

func (o *legacyDurableOutbox) Enqueue(entry runthrough.OutboxEntry) (runthrough.OutboxEntry, error) {
	return o.inner.Enqueue(entry)
}
func (o *legacyDurableOutbox) Pending() []runthrough.OutboxEntry { return o.inner.Pending() }
func (o *legacyDurableOutbox) MarkSuccess(id string) error       { return o.inner.MarkSuccess(id) }
func (o *legacyDurableOutbox) MarkFailure(id string, cause error, retryAt time.Time) error {
	return o.inner.MarkFailure(id, cause, retryAt)
}
func (o *legacyDurableOutbox) Discard(id string) error { return o.inner.Discard(id) }
func (o *legacyDurableOutbox) Close() error            { return o.inner.Close() }
func (o *legacyDurableOutbox) Durable() bool           { return true }

type failingEnqueueOutbox struct {
	*runthrough.MemoryOutbox
	failAt   int
	calls    int
	failWith error
}

func (o *failingEnqueueOutbox) Enqueue(entry runthrough.OutboxEntry) (runthrough.OutboxEntry, error) {
	o.calls++
	if o.calls == o.failAt {
		return runthrough.OutboxEntry{}, o.failWith
	}
	return o.MemoryOutbox.Enqueue(entry)
}

func (o *failingEnqueueOutbox) Prepare(entry runthrough.OutboxEntry) (runthrough.OutboxEntry, error) {
	o.calls++
	if o.calls == o.failAt {
		return runthrough.OutboxEntry{}, o.failWith
	}
	return o.MemoryOutbox.Prepare(entry)
}

func (o *failingEnqueueOutbox) Durable() bool { return true }

type failingMarkOutbox struct {
	*runthrough.MemoryOutbox
	failure error
}

func (o *failingMarkOutbox) MarkFailure(string, error, time.Time) error { return o.failure }
func (o *failingMarkOutbox) Durable() bool                              { return true }

type blockingUpstream struct {
	*mockUpstream
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newBlockingUpstream() *blockingUpstream {
	return &blockingUpstream{
		mockUpstream: newMockUpstream(),
		started:      make(chan struct{}, 4),
		release:      make(chan struct{}),
	}
}

func (u *blockingUpstream) PutObject(ctx context.Context, _, _ string, _ io.Reader, _ storage.PutOptions) error {
	u.calls.Add(1)
	select {
	case u.started <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-u.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runFileOutboxPersistenceFailure(t *testing.T, operation func(*runthrough.FileOutbox, runthrough.OutboxEntry) error) (*runthrough.FileOutbox, string, runthrough.OutboxEntry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new file outbox: %v", err)
	}
	entry, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
	})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	backup := path + ".saved"
	if err := os.Rename(path, backup); err != nil {
		t.Fatalf("save outbox: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("block outbox persistence: %v", err)
	}
	err = operation(outbox, entry)
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove persistence blocker: %v", err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatalf("restore outbox: %v", err)
	}
	return outbox, path, entry
}

func assertFileOutboxEntryUnchanged(t *testing.T, pending []runthrough.OutboxEntry, entry runthrough.OutboxEntry) {
	t.Helper()
	if len(pending) != 1 || pending[0].ID != entry.ID || pending[0].Attempts != 0 || pending[0].LastError != "" || !pending[0].NextAttempt.IsZero() {
		t.Fatalf("outbox state changed after failed persistence: %+v", pending)
	}
}

func TestFileOutboxEnqueueDoesNotSwapMemoryWhenPersistenceFails(t *testing.T) {
	outbox, path, entry := runFileOutboxPersistenceFailure(t, func(o *runthrough.FileOutbox, _ runthrough.OutboxEntry) error {
		_, err := o.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "new"})
		return err
	})
	assertFileOutboxEntryUnchanged(t, outbox.Pending(), entry)
	reopened, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	assertFileOutboxEntryUnchanged(t, reopened.Pending(), entry)
}

func TestFileOutboxMarkFailureDoesNotSwapMemoryWhenPersistenceFails(t *testing.T) {
	outbox, _, entry := runFileOutboxPersistenceFailure(t, func(o *runthrough.FileOutbox, e runthrough.OutboxEntry) error {
		return o.MarkFailure(e.ID, errors.New("temporary"), time.Now().Add(time.Minute))
	})
	assertFileOutboxEntryUnchanged(t, outbox.Pending(), entry)
}

func TestFileOutboxMarkSuccessDoesNotSwapMemoryWhenPersistenceFails(t *testing.T) {
	outbox, _, entry := runFileOutboxPersistenceFailure(t, func(o *runthrough.FileOutbox, e runthrough.OutboxEntry) error {
		return o.MarkSuccess(e.ID)
	})
	assertFileOutboxEntryUnchanged(t, outbox.Pending(), entry)
}

func TestFileOutboxDiscardDoesNotSwapMemoryWhenPersistenceFails(t *testing.T) {
	outbox, _, entry := runFileOutboxPersistenceFailure(t, func(o *runthrough.FileOutbox, e runthrough.OutboxEntry) error {
		return o.Discard(e.ID)
	})
	assertFileOutboxEntryUnchanged(t, outbox.Pending(), entry)
}

func TestAdapterPreservesMarkFailureError(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	up := newMockUpstream()
	up.putErr = errors.New("upstream failed")
	markErr := errors.New("outbox persistence failed")
	outbox := &failingMarkOutbox{MemoryOutbox: runthrough.NewMemoryOutbox(), failure: markErr}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	_, err := adapter.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err == nil {
		t.Fatal("expected propagation failure")
	}
	if !errors.Is(err, markErr) {
		t.Fatalf("error = %v, want mark failure error", err)
	}
}

func TestDeleteObjectsEnqueuesEveryIntentBeforePropagation(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := local.PutObject(ctx, "bucket", key, bytes.NewReader([]byte(key)), storage.PutOptions{}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	up := newMockUpstream()
	up.delErr = errors.New("temporary delete failure")
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	deleted, err := adapter.DeleteObjects(ctx, "bucket", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected delete propagation failure")
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want both keys", deleted)
	}
	if got := len(outbox.Pending()); got != 2 {
		t.Fatalf("pending intents = %d, want 2: %+v", got, outbox.Pending())
	}
}

func TestDeleteObjectsDoesNotPropagateBeforeAllEnqueuesSucceed(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := local.PutObject(ctx, "bucket", key, bytes.NewReader([]byte(key)), storage.PutOptions{}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	up := newMockUpstream()
	enqueueErr := errors.New("enqueue failed")
	outbox := &failingEnqueueOutbox{
		MemoryOutbox: runthrough.NewMemoryOutbox(),
		failAt:       2,
		failWith:     enqueueErr,
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	if _, err := adapter.DeleteObjects(ctx, "bucket", []string{"a", "b"}); !errors.Is(err, enqueueErr) {
		t.Fatalf("error = %v, want enqueue error", err)
	}
	if up.delCalls != 0 {
		t.Fatalf("upstream delete calls = %d, want 0", up.delCalls)
	}
	if got := len(outbox.Prepared()); got != 1 {
		t.Fatalf("prepared intents = %d, want only successfully prepared first intent", got)
	}
	if got := len(outbox.Pending()); got != 0 {
		t.Fatalf("active intents = %d, want none before local batch commit", got)
	}
}

func TestAdapterDoesNotOvertakeOlderPendingSameKeyIntent(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	oldMeta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("old")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put old: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation:   runthrough.OutboxPut,
		Bucket:      "bucket",
		Key:         "key",
		Version:     oldMeta.VersionID,
		NextAttempt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("enqueue old intent: %v", err)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	if _, err := adapter.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("new")), storage.PutOptions{}); err != nil {
		t.Fatalf("new local put: %v", err)
	}
	if up.putCalls != 0 {
		t.Fatalf("upstream put calls = %d, want 0 while older intent is pending", up.putCalls)
	}
	if got := len(outbox.Pending()); got != 2 {
		t.Fatalf("pending intents = %d, want 2", got)
	}
}

func TestRetryPendingSerializesConcurrentAttemptsForOneEntry(t *testing.T) {
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
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
		Version:   meta.VersionID,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	up := newBlockingUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	firstDone := make(chan error, 1)
	go func() { firstDone <- adapter.RetryPending(ctx) }()
	select {
	case <-up.started:
	case <-time.After(time.Second):
		t.Fatal("first retry did not reach upstream")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- adapter.RetryPending(ctx) }()
	select {
	case <-up.started:
		t.Fatal("second retry processed the same entry concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(up.release)

	if err := <-firstDone; err != nil {
		t.Fatalf("first retry: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second retry: %v", err)
	}
	if got := up.calls.Load(); got != 1 {
		t.Fatalf("upstream attempts = %d, want 1", got)
	}
}

func TestImmediatePropagationWaitsForInFlightRetryOnSameKey(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("old")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put old: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
		Version:   meta.VersionID,
	}); err != nil {
		t.Fatalf("enqueue old intent: %v", err)
	}
	up := newBlockingUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)

	retryDone := make(chan error, 1)
	go func() { retryDone <- adapter.RetryPending(ctx) }()
	select {
	case <-up.started:
	case <-time.After(time.Second):
		t.Fatal("retry did not reach upstream")
	}
	putDone := make(chan error, 1)
	go func() {
		_, err := adapter.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("new")), storage.PutOptions{})
		putDone <- err
	}()
	select {
	case <-up.started:
		t.Fatal("immediate propagation ran concurrently with retry")
	case <-time.After(100 * time.Millisecond):
	}
	close(up.release)

	if err := <-retryDone; err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := <-putDone; err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := up.calls.Load(); got != 2 {
		t.Fatalf("upstream attempts = %d, want sequential old and new attempts", got)
	}
}
