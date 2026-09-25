package runthrough

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type OutboxOperation string

var (
	ErrOutboxVersionConflict    = errors.New("outbox object version no longer matches the committed record")
	ErrOutboxPreparedUnresolved = errors.New("outbox prepared intent cannot be reconciled")
	// ErrOutboxFormatVersion reports a durable outbox written by a different
	// Stow schema revision. Writers sharing one outbox file must run the same
	// implementation; refusing to open keeps fencing and reconciliation fields
	// from being silently dropped.
	ErrOutboxFormatVersion = errors.New("outbox file format version is not supported by this writer")
)

// outboxFormatVersion is the on-disk schema revision written by this build.
// Version 0 (unversioned) files are migrated on read; anything newer is
// rejected rather than downgraded.
const outboxFormatVersion = 2

const (
	OutboxPut       OutboxOperation = "put"
	OutboxDelete    OutboxOperation = "delete"
	OutboxCopy      OutboxOperation = "copy"
	OutboxMultipart OutboxOperation = "multipart_complete"
)

type OutboxEntry struct {
	ID              string          `json:"id"`
	Operation       OutboxOperation `json:"operation"`
	Bucket          string          `json:"bucket"`
	Key             string          `json:"key"`
	SourceBucket    string          `json:"source_bucket,omitempty"`
	SourceKey       string          `json:"source_key,omitempty"`
	Version         string          `json:"version,omitempty"`
	PreviousVersion string          `json:"previous_version,omitempty"`
	Prepared        bool            `json:"prepared,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	Attempts        int             `json:"attempts"`
	NextAttempt     time.Time       `json:"next_attempt"`
	LastError       string          `json:"last_error,omitempty"`
	Terminal        bool            `json:"terminal,omitempty"`
	ClaimOwner      string          `json:"claim_owner,omitempty"`
	ClaimUntil      time.Time       `json:"claim_until,omitempty"`
	ClaimToken      uint64          `json:"claim_token,omitempty"`
	AttemptedAt     time.Time       `json:"attempted_at,omitempty"`
	NeedsReconcile  bool            `json:"needs_reconcile,omitempty"`
	PreparedOwner   string          `json:"prepared_owner,omitempty"`
	PreparedUntil   time.Time       `json:"prepared_until,omitempty"`
	PreparedToken   uint64          `json:"prepared_token,omitempty"`
}

type Outbox interface {
	Enqueue(entry OutboxEntry) (OutboxEntry, error)
	Pending() []OutboxEntry
	MarkSuccess(id string) error
	MarkFailure(id string, cause error, retryAt time.Time) error
	Discard(id string) error
	Close() error
}

// CoordinatedOutbox extends the active outbox with a durable prepare/commit phase.
type CoordinatedOutbox interface {
	Outbox
	Prepare(entry OutboxEntry) (OutboxEntry, error)
	Commit(id, version string) (OutboxEntry, error)
	DiscardPrepared(id string) error
	Prepared() []OutboxEntry
}

type DurableOutbox interface {
	Outbox
	Durable() bool
}

type SnapshotOutbox interface {
	PendingSnapshot() ([]OutboxEntry, error)
	PreparedSnapshot() ([]OutboxEntry, error)
}

type outboxState struct {
	entries   map[string]OutboxEntry
	prepared  map[string]OutboxEntry
	seq       uint64
	nextToken uint64
}

func newOutboxState() outboxState {
	return outboxState{entries: make(map[string]OutboxEntry), prepared: make(map[string]OutboxEntry)}
}

func (s outboxState) clone() outboxState {
	entries := make(map[string]OutboxEntry, len(s.entries))
	for id, entry := range s.entries {
		entries[id] = entry
	}
	prepared := make(map[string]OutboxEntry, len(s.prepared))
	for id, entry := range s.prepared {
		prepared[id] = entry
	}
	return outboxState{entries: entries, prepared: prepared, seq: s.seq, nextToken: s.nextToken}
}

func (s *outboxState) assignID(entry OutboxEntry) OutboxEntry {
	if entry.ID == "" {
		s.seq++
		entry.ID = fmt.Sprintf("outbox-%d", s.seq)
	} else if strings.HasPrefix(entry.ID, "outbox-") {
		if sequence, err := strconv.ParseUint(strings.TrimPrefix(entry.ID, "outbox-"), 10, 64); err == nil && sequence > s.seq {
			s.seq = sequence
		}
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	return entry
}

func (s *outboxState) assignToken() uint64 {
	s.nextToken++
	return s.nextToken
}

func (s *outboxState) enqueue(entry OutboxEntry) OutboxEntry {
	entry = s.assignID(entry)
	entry.Prepared = false
	s.entries[entry.ID] = entry
	return entry
}

func (s *outboxState) prepare(entry OutboxEntry) OutboxEntry {
	entry = s.assignID(entry)
	entry.Prepared = true
	s.prepared[entry.ID] = entry
	return entry
}

func (s *outboxState) prepareOwned(entry OutboxEntry, owner string, lease time.Duration) (OutboxEntry, error) {
	if owner == "" || lease <= 0 {
		return OutboxEntry{}, ErrOutboxClaimLease
	}
	entry = s.assignID(entry)
	if s.hasPreparedKey(entry) {
		return OutboxEntry{}, ErrOutboxPreparedUnresolved
	}
	entry.Prepared = true
	entry.PreparedOwner = owner
	entry.PreparedUntil = time.Now().UTC().Add(lease)
	entry.PreparedToken = s.assignToken()
	s.prepared[entry.ID] = entry
	return entry, nil
}

func (s *outboxState) hasPreparedKey(entry OutboxEntry) bool {
	for _, prepared := range s.prepared {
		if prepared.Bucket == entry.Bucket && prepared.Key == entry.Key {
			return true
		}
	}
	return false
}

func (s *outboxState) renewPrepared(id, owner string, token uint64, lease time.Duration) error {
	if lease <= 0 {
		return ErrOutboxClaimLease
	}
	entry, ok := s.prepared[id]
	if !ok {
		return fmt.Errorf("outbox prepared entry %q not found", id)
	}
	if entry.PreparedOwner != owner || entry.PreparedToken != token {
		return ErrOutboxClaimLost
	}
	entry.PreparedUntil = time.Now().UTC().Add(lease)
	s.prepared[id] = entry
	return nil
}

func (s *outboxState) commitPrepared(id, owner string, token uint64, version string) (OutboxEntry, error) {
	entry, ok := s.prepared[id]
	if !ok {
		if active, activeOK := s.entries[id]; activeOK {
			return active, nil
		}
		return OutboxEntry{}, fmt.Errorf("outbox prepared entry %q not found", id)
	}
	if entry.PreparedOwner != owner || entry.PreparedToken != token || !entry.PreparedUntil.After(time.Now().UTC()) {
		return OutboxEntry{}, ErrOutboxClaimLost
	}
	delete(s.prepared, id)
	entry.Prepared = false
	entry.PreparedOwner = ""
	entry.PreparedUntil = time.Time{}
	entry.PreparedToken = 0
	entry.Version = version
	s.entries[id] = entry
	return entry, nil
}

func (s *outboxState) discardPreparedOwned(id, owner string, token uint64) error {
	entry, ok := s.prepared[id]
	if !ok {
		return nil
	}
	if entry.PreparedOwner != owner || entry.PreparedToken != token {
		return ErrOutboxClaimLost
	}
	delete(s.prepared, id)
	return nil
}

func (s *outboxState) pending() []OutboxEntry {
	return pendingEntries(s.entries)
}

func (s *outboxState) preparedEntries() []OutboxEntry {
	return pendingEntries(s.prepared)
}

func (s *outboxState) markSuccess(id string) error {
	entry, ok := s.entries[id]
	if !ok {
		return nil
	}
	if entry.ClaimOwner != "" {
		return ErrOutboxClaimHeld
	}
	delete(s.entries, id)
	return nil
}

func (s *outboxState) discard(id string) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != "" {
		return ErrOutboxClaimHeld
	}
	delete(s.entries, id)
	return nil
}

func (s *outboxState) markFailure(id string, cause error, retryAt time.Time) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != "" {
		return ErrOutboxClaimHeld
	}
	return s.markFailureUnchecked(entry, cause, retryAt)
}

func (s *outboxState) markFailureUnchecked(entry OutboxEntry, cause error, retryAt time.Time) error {
	entry.Attempts++
	entry.NextAttempt = retryAt
	if cause != nil {
		entry.LastError = cause.Error()
	}
	if classifyRetry(cause) == RetryClassDeterministic {
		entry.Terminal = true
		entry.NextAttempt = time.Time{}
	}
	s.entries[entry.ID] = entry
	return nil
}

func (s *outboxState) commit(id, version string) (OutboxEntry, error) {
	entry, ok := s.prepared[id]
	if !ok {
		if active, activeOK := s.entries[id]; activeOK {
			return active, nil
		}
		return OutboxEntry{}, fmt.Errorf("outbox prepared entry %q not found", id)
	}
	if entry.PreparedOwner != "" {
		return OutboxEntry{}, ErrOutboxClaimHeld
	}
	delete(s.prepared, id)
	entry.Prepared = false
	entry.PreparedOwner = ""
	entry.PreparedUntil = time.Time{}
	entry.PreparedToken = 0
	entry.Version = version
	s.entries[id] = entry
	return entry, nil
}

func (s *outboxState) discardPrepared(id string) error {
	entry, ok := s.prepared[id]
	if !ok {
		return fmt.Errorf("outbox prepared entry %q not found", id)
	}
	if entry.PreparedOwner != "" {
		return ErrOutboxClaimHeld
	}
	delete(s.prepared, id)
	return nil
}

type MemoryOutbox struct {
	mu    sync.Mutex
	state outboxState
}

func NewMemoryOutbox() *MemoryOutbox {
	return &MemoryOutbox{state: newOutboxState()}
}

func (o *MemoryOutbox) Enqueue(entry OutboxEntry) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.enqueue(entry), nil
}

func (o *MemoryOutbox) Prepare(entry OutboxEntry) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.prepare(entry), nil
}

func (o *MemoryOutbox) Prepared() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.preparedEntries()
}

func (o *MemoryOutbox) Pending() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.pending()
}

func (o *MemoryOutbox) MarkSuccess(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.markSuccess(id)
}

func (o *MemoryOutbox) MarkFailure(id string, cause error, retryAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.markFailure(id, cause, retryAt)
}

func (o *MemoryOutbox) Commit(id, version string) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.commit(id, version)
}

func (o *MemoryOutbox) DiscardPrepared(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.discardPrepared(id)
}

func (o *MemoryOutbox) Discard(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.discard(id)
}

func (o *MemoryOutbox) Close() error { return nil }

func (o *MemoryOutbox) Durable() bool { return false }

func pendingEntries(entries map[string]OutboxEntry) []OutboxEntry {
	out := make([]OutboxEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return outboxSequence(out[i].ID) < outboxSequence(out[j].ID)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func outboxSequenceOK(id string) (uint64, bool) {
	if !strings.HasPrefix(id, "outbox-") {
		return 0, false
	}
	sequence, err := strconv.ParseUint(strings.TrimPrefix(id, "outbox-"), 10, 64)
	return sequence, err == nil
}

func outboxSequence(id string) uint64 {
	sequence, ok := outboxSequenceOK(id)
	if !ok {
		return ^uint64(0)
	}
	return sequence
}
