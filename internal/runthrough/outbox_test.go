package runthrough_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestFileOutboxRejectsNewerFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	writeOutboxFile(t, path, `{"version":99,"entries":{},"seq":0}`)

	if _, err := runthrough.NewFileOutbox(path); !errors.Is(err, runthrough.ErrOutboxFormatVersion) {
		t.Fatalf("NewFileOutbox error = %v, want ErrOutboxFormatVersion", err)
	}
}

func TestFileOutboxUpgradesLegacyAttemptedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	writeOutboxFile(t, path, `{"entries":{"outbox-1":{"id":"outbox-1","operation":"put",`+
		`"bucket":"bucket","key":"key","created_at":"2026-01-01T00:00:00Z","attempts":2}},`+
		`"seq":1}`)

	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new file outbox: %v", err)
	}
	pending, err := outbox.PendingSnapshot()
	if err != nil {
		t.Fatalf("pending snapshot: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want one entry", pending)
	}
	if !pending[0].Attempted {
		t.Fatalf("legacy entry was not recorded as attempted: %+v", pending[0])
	}

	claimed, ok, err := outbox.Claim(pending[0].ID, "owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %+v, ok=%v, err=%v", claimed, ok, err)
	}
	if !claimed.Reconcile {
		t.Fatal("legacy attempted entry was claimed without reconciliation")
	}
	if err := outbox.MarkClaimedSuccess(pending[0].ID, "owner", claimed.Entry.ClaimToken); err != nil {
		t.Fatalf("mark success: %v", err)
	}

	if _, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "later"}); err != nil {
		t.Fatalf("enqueue after upgrade: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var written struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("decode rewritten outbox: %v", err)
	}
	if written.Version == 0 {
		t.Fatalf("rewritten outbox is still unversioned: %s", data)
	}
}

func writeOutboxFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write outbox file: %v", err)
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

func TestRetryPendingBlocksLaterSameKeyIntent(t *testing.T) {
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
	created := time.Unix(100, 0).UTC()
	entries := []runthrough.OutboxEntry{
		{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key", Version: meta.VersionID, CreatedAt: created, NextAttempt: time.Now().Add(time.Hour)},
		{Operation: runthrough.OutboxDelete, Bucket: "bucket", Key: "key", Version: meta.VersionID, CreatedAt: created},
	}
	for _, entry := range entries {
		if _, err := outbox.Enqueue(entry); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, newMockUpstream(), outbox)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry pending: %v", err)
	}
	if len(outbox.Pending()) != 2 {
		t.Fatalf("pending = %+v, want both intents retained", outbox.Pending())
	}
}

func TestOutboxDeleteDoesNotRemoveRecreatedSameETagObject(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	first, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("same")), storage.PutOptions{
		Metadata: map[string]string{"generation": "one"},
	})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation: runthrough.OutboxDelete,
		Bucket:    "bucket",
		Key:       "key",
		Version:   first.VersionID,
	}); err != nil {
		t.Fatalf("enqueue delete: %v", err)
	}
	if err := local.DeleteObject(ctx, "bucket", "key"); err != nil {
		t.Fatalf("delete local: %v", err)
	}
	second, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("same")), storage.PutOptions{
		Metadata: map[string]string{"generation": "two"},
	})
	if err != nil {
		t.Fatalf("recreate local: %v", err)
	}
	if second.ETag != first.ETag || second.VersionID == first.VersionID {
		t.Fatalf("recreated versions = %q/%q and %q/%q", first.ETag, first.VersionID, second.ETag, second.VersionID)
	}
	up := newMockUpstream()
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
	}, local, local, up, outbox)
	if err := adapter.RetryPending(ctx); err == nil {
		t.Fatal("expected version conflict")
	}
	if up.delCalls != 0 {
		t.Fatalf("upstream delete calls = %d, want 0", up.delCalls)
	}
	pending := outbox.Pending()
	if len(pending) != 1 || !pending[0].Terminal {
		t.Fatalf("pending = %+v, want terminal conflict", pending)
	}
}
