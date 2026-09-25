package runthrough_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func expireFileOutboxClaim(t *testing.T, path, id string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var state struct {
		Entries map[string]map[string]interface{} `json:"entries"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode outbox: %v", err)
	}
	entry, ok := state.Entries[id]
	if !ok {
		t.Fatalf("claim entry %q not found", id)
	}
	entry["claim_until"] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatalf("encode outbox: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write expired claim: %v", err)
	}
}

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
	claimed, ok, err := first.Claim(entry.ID, "owner-a", time.Minute)
	if err != nil || !ok || claimed.ClaimOwner != "owner-a" {
		t.Fatalf("first claim = %+v, ok=%v, err=%v", claimed, ok, err)
	}
	if _, ok, err := second.Claim(entry.ID, "owner-b", time.Minute); err != nil || ok {
		t.Fatalf("competing claim ok=%v err=%v, want refusal", ok, err)
	}
	if err := second.MarkClaimedSuccess(entry.ID, "owner-b", claimed.ClaimToken); err == nil {
		t.Fatal("stale owner was allowed to mark success")
	}
	expireFileOutboxClaim(t, path, entry.ID)
	if _, ok, err := second.Claim(entry.ID, "owner-b", time.Minute); err != nil || !ok {
		t.Fatalf("expired claim ok=%v err=%v, want takeover", ok, err)
	}
}

func TestPreparedIntentIsOwnedAndRejectsDuplicateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("first outbox: %v", err)
	}
	owner := "stow-" + strconv.Itoa(os.Getpid()) + "-prepared"
	entry, err := first.PrepareOwned(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"}, owner, time.Minute)
	if err != nil {
		t.Fatalf("prepare owned: %v", err)
	}
	second, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("second outbox: %v", err)
	}
	if _, err := second.PrepareOwned(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"}, "other", time.Minute); !errors.Is(err, runthrough.ErrOutboxPreparedUnresolved) {
		t.Fatalf("duplicate prepare error = %v", err)
	}
	if _, err := second.CommitPrepared(entry.ID, owner, entry.PreparedToken, "version-1"); err != nil {
		t.Fatalf("commit owned prepared intent: %v", err)
	}
	if pending := second.Pending(); len(pending) != 1 || pending[0].Prepared {
		t.Fatalf("committed prepared state = %+v", pending)
	}
}

func TestRecoverPreparedSkipsLiveOwner(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	owner := "stow-" + strconv.Itoa(os.Getpid()) + "-prepared"
	if _, err := outbox.PrepareOwned(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"}, owner, time.Minute); err != nil {
		t.Fatalf("prepare owned: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}, local, local, newMockUpstream(), outbox)
	if err := adapter.RetryPending(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if prepared := outbox.Prepared(); len(prepared) != 1 {
		t.Fatalf("prepared = %+v, want live-owner entry", prepared)
	}
}

func TestLegacyMutationsCannotBypassClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := runthrough.NewFileOutbox(path)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	entry, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, ok, err := outbox.Claim(entry.ID, "owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %+v, ok=%v, err=%v", claimed, ok, err)
	}
	if err := outbox.MarkSuccess(entry.ID); !errors.Is(err, runthrough.ErrOutboxClaimHeld) {
		t.Fatalf("mark success error = %v", err)
	}
	if err := outbox.MarkFailure(entry.ID, errors.New("late"), time.Now()); !errors.Is(err, runthrough.ErrOutboxClaimHeld) {
		t.Fatalf("mark failure error = %v", err)
	}
	if err := outbox.Discard(entry.ID); !errors.Is(err, runthrough.ErrOutboxClaimHeld) {
		t.Fatalf("discard error = %v", err)
	}
}

func TestClaimTokenFencesStaleOwner(t *testing.T) {
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
	old, ok, err := first.Claim(entry.ID, "old-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("old claim = %+v, ok=%v, err=%v", old, ok, err)
	}
	expireFileOutboxClaim(t, path, entry.ID)
	current, ok, err := second.Claim(entry.ID, "new-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover claim = %+v, ok=%v, err=%v", current, ok, err)
	}
	if err := first.MarkClaimedSuccess(entry.ID, "old-owner", old.ClaimToken); !errors.Is(err, runthrough.ErrOutboxClaimLost) {
		t.Fatalf("stale completion error = %v", err)
	}
	if err := second.MarkClaimedSuccess(entry.ID, "new-owner", current.ClaimToken); err != nil {
		t.Fatalf("current completion: %v", err)
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
