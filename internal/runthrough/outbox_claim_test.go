package runthrough_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestFileOutboxSharedInstancesReloadBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("first outbox: %v", err)
	}
	second, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("second outbox: %v", err)
	}
	if _, err := first.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "one"}); err != nil {
		t.Fatalf("enqueue one: %v", err)
	}
	if _, err := second.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "two"}); err != nil {
		t.Fatalf("enqueue two: %v", err)
	}
	if pending := first.Pending(); len(pending) != 2 {
		t.Fatalf("first pending = %+v, want both entries", pending)
	}
	if pending := second.Pending(); len(pending) != 2 {
		t.Fatalf("second pending = %+v, want both entries", pending)
	}
}

func TestFileOutboxProcessHelper(t *testing.T) {
	if os.Getenv("STOW_OUTBOX_HELPER") != "1" {
		return
	}
	outbox, err := runthrough.NewFileOutbox(os.Getenv("STOW_OUTBOX_PATH"))
	if err != nil {
		t.Fatalf("open helper outbox: %v", err)
	}
	if _, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: os.Getenv("STOW_OUTBOX_KEY")}); err != nil {
		t.Fatalf("enqueue helper entry: %v", err)
	}
}

func TestFileOutboxConcurrentProcessesDoNotLoseEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	commands := make([]*exec.Cmd, 2)
	for i, key := range []string{"one", "two"} {
		command := exec.Command(os.Args[0], "-test.run=^TestFileOutboxProcessHelper$")
		command.Env = append(os.Environ(), "STOW_OUTBOX_HELPER=1", "STOW_OUTBOX_PATH="+path, "STOW_OUTBOX_KEY="+key)
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
		commands[i] = command
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d: %v", i, err)
		}
	}
	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("reopen outbox: %v", err)
	}
	if pending := outbox.Pending(); len(pending) != 2 {
		t.Fatalf("pending = %+v, want two process entries", pending)
	}
}

func TestFileOutboxClaimIsExclusiveAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("first outbox: %v", err)
	}
	second, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("second outbox: %v", err)
	}
	entry, err := first.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, ok, err := first.Claim(entry.ID, "owner-a", 500*time.Millisecond)
	if err != nil || !ok || claimed.ClaimOwner != "owner-a" {
		t.Fatalf("first claim = %+v, ok=%v, err=%v", claimed, ok, err)
	}
	if _, ok, err := second.Claim(entry.ID, "owner-b", time.Minute); err != nil || ok {
		t.Fatalf("competing claim ok=%v err=%v, want refusal", ok, err)
	}
	if err := second.MarkClaimedSuccess(entry.ID, "owner-b"); err == nil {
		t.Fatal("stale owner was allowed to mark success")
	}
	time.Sleep(600 * time.Millisecond)
	if _, ok, err := second.Claim(entry.ID, "owner-b", time.Minute); err != nil || !ok {
		t.Fatalf("expired claim ok=%v err=%v, want takeover", ok, err)
	}
}

func TestConcurrentRetryClaimsOnePropagation(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("first outbox: %v", err)
	}
	if _, err := first.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key", Version: meta.VersionID}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	second, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("second outbox: %v", err)
	}
	up1 := newMockUpstream()
	up2 := newMockUpstream()
	a1 := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up1, first)
	a2 := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, up2, second)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a1.RetryPending(ctx) }()
	go func() { defer wg.Done(); _ = a2.RetryPending(ctx) }()
	wg.Wait()
	if calls := up1.putCalls + up2.putCalls; calls != 1 {
		t.Fatalf("upstream calls = %d, want exactly one", calls)
	}
	if pending := first.Pending(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}
