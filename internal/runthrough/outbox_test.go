package runthrough_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestFileOutboxRestartsWithMonotonicIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new file outbox: %v", err)
	}
	if _, err := first.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "one"}); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	firstPending := first.Pending()
	if len(firstPending) != 1 {
		t.Fatalf("first pending = %+v", firstPending)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("reopen file outbox: %v", err)
	}
	if _, err := second.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "two"}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	pending := second.Pending()
	if len(pending) != 2 {
		t.Fatalf("second pending = %+v", pending)
	}
	if pending[0].ID == pending[1].ID {
		t.Fatalf("duplicate IDs after restart: %q", pending[0].ID)
	}
}

func TestMemoryOutboxPreservesPerKeyOrder(t *testing.T) {
	outbox := runthrough.NewMemoryOutbox()
	created := time.Unix(100, 0).UTC()
	entries := []runthrough.OutboxEntry{
		{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key", Version: "one", CreatedAt: created},
		{Operation: runthrough.OutboxDelete, Bucket: "bucket", Key: "key", Version: "one", CreatedAt: created},
		{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key", Version: "two", CreatedAt: created},
	}
	for _, entry := range entries {
		if _, err := outbox.Enqueue(entry); err != nil {
			t.Fatalf("enqueue %s: %v", entry.Operation, err)
		}
	}

	pending := outbox.Pending()
	if len(pending) != len(entries) {
		t.Fatalf("pending entries = %d, want %d", len(pending), len(entries))
	}
	for i, want := range entries {
		if pending[i].Operation != want.Operation || pending[i].Version != want.Version {
			t.Fatalf("pending[%d] = %+v, want operation/version %s/%s", i, pending[i], want.Operation, want.Version)
		}
	}
}

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
			Version:   meta.ETag,
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
