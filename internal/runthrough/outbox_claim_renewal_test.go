package runthrough

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// countingClaimOutbox records the claim lifecycle calls a propagation makes, so
// a test can assert that a slow propagation renewed its lease instead of
// letting it expire.
type countingClaimOutbox struct {
	*FileOutbox
	mu      sync.Mutex
	renewed int
}

func (o *countingClaimOutbox) Renew(id, owner string, token uint64, lease time.Duration) error {
	o.mu.Lock()
	o.renewed++
	o.mu.Unlock()
	return o.FileOutbox.Renew(id, owner, token, lease)
}

func (o *countingClaimOutbox) renewals() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.renewed
}

// slowUpstream is a minimal Client whose PutObject blocks, so propagation
// outlives several claim leases. It lives in this package because the shared
// mock upstream is defined in the external test package.
type slowUpstream struct {
	delay  time.Duration
	held   chan struct{}
	stored map[string][]byte
	meta   map[string]storage.ObjectMeta
}

func newSlowUpstream(delay time.Duration) *slowUpstream {
	return &slowUpstream{
		delay:  delay,
		held:   make(chan struct{}, 1),
		stored: make(map[string][]byte),
		meta:   make(map[string]storage.ObjectMeta),
	}
}

func (u *slowUpstream) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) error {
	select {
	case u.held <- struct{}{}:
	default:
	}
	time.Sleep(u.delay)
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	location := bucket + "/" + key
	u.stored[location] = data
	u.meta[location] = storage.ObjectMeta{
		Bucket:      bucket,
		Key:         key,
		Size:        int64(len(data)),
		ETag:        strings.Repeat("e", 32),
		ContentType: opts.ContentType,
		Metadata:    opts.Metadata,
	}
	return nil
}

func (u *slowUpstream) HeadObject(_ context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	meta, ok := u.meta[bucket+"/"+key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	return &meta, nil
}

func (u *slowUpstream) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	location := bucket + "/" + key
	data, ok := u.stored[location]
	if !ok {
		return nil, nil, storage.ErrObjectNotFound
	}
	meta := u.meta[location]
	return io.NopCloser(bytes.NewReader(data)), &meta, nil
}

func (u *slowUpstream) DeleteObject(_ context.Context, bucket, key string) error {
	location := bucket + "/" + key
	delete(u.stored, location)
	delete(u.meta, location)
	return nil
}

func (u *slowUpstream) ListObjectsV2(_ context.Context, _ string, _ storage.ListOptions) (*storage.ListResult, error) {
	return &storage.ListResult{}, nil
}

// The claim lease is renewed while a slow propagation is in flight, so a second
// worker cannot take the entry over mid-propagation. Without renewal this test
// is timing dependent: the statement it covers only runs when a lease tick fires
// before propagation finishes.
func TestClaimLeaseIsRenewedDuringSlowPropagation(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	meta, err := local.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put local: %v", err)
	}

	outbox, err := NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("file outbox: %v", err)
	}
	counting := &countingClaimOutbox{FileOutbox: outbox}
	entry, err := counting.Enqueue(OutboxEntry{
		Operation: OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
		Version:   meta.VersionID,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	upstream := newSlowUpstream(120 * time.Millisecond)
	adapter := NewWithOutbox(Config{Policy: PolicyMirrorWrites, AllowLiveWrites: true}, local, local, upstream, counting)
	// A 30ms lease renews every 10ms, so a 120ms propagation crosses several
	// renewal intervals deterministically instead of relying on wall-clock luck.
	adapter.claimLease = 30 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- adapter.RetryPending(ctx) }()

	select {
	case <-upstream.held:
	case <-time.After(5 * time.Second):
		t.Fatal("propagation never reached the upstream")
	}

	// While propagation is in flight, a competing claim must be refused because
	// the lease keeps being renewed.
	if _, ok, err := outbox.Claim(entry.ID, "competing-owner", time.Minute); err != nil {
		t.Fatalf("competing claim: %v", err)
	} else if ok {
		t.Fatal("a second worker claimed an entry whose lease was still being renewed")
	}

	if err := <-done; err != nil {
		t.Fatalf("retry pending: %v", err)
	}
	if counting.renewals() == 0 {
		t.Fatal("claim lease was never renewed during propagation")
	}
	if pending := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}
