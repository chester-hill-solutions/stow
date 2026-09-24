package runthrough_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestFileOutboxRestartsWithMonotonicIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("new file outbox: %v", err)
	}
	if err := first.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "one"}); err != nil {
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
	if err := second.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "two"}); err != nil {
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

func TestMemoryOutboxLifecycle(t *testing.T) {
	outbox := runthrough.NewMemoryOutbox()
	entry := runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"}
	if err := outbox.Enqueue(entry); err != nil {
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
