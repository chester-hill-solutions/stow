package runthrough_test

import (
	"errors"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestMemoryOutboxLifecycle(t *testing.T) {
	outbox := runthrough.NewMemoryOutbox()
	entry := runthrough.OutboxEntry{Operation: "put", Bucket: "bucket", Key: "key"}
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
	if err := outbox.MarkSuccess(pending[0].ID); err != nil {
		t.Fatalf("mark success: %v", err)
	}
	if len(outbox.Pending()) != 0 {
		t.Fatal("expected empty outbox after success")
	}
}
