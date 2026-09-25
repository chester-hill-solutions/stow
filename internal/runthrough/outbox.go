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
)

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

type outboxState struct {
	entries  map[string]OutboxEntry
	prepared map[string]OutboxEntry
	seq      uint64
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
	return outboxState{entries: entries, prepared: prepared, seq: s.seq}
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

func (s *outboxState) pending() []OutboxEntry {
	return pendingEntries(s.entries)
}

func (s *outboxState) preparedEntries() []OutboxEntry {
	return pendingEntries(s.prepared)
}

func (s *outboxState) markSuccess(id string) error {
	delete(s.entries, id)
	return nil
}

func (s *outboxState) discard(id string) error {
	if _, ok := s.entries[id]; !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	delete(s.entries, id)
	return nil
}

func (s *outboxState) markFailure(id string, cause error, retryAt time.Time) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	entry.Attempts++
	entry.NextAttempt = retryAt
	entry.ClaimOwner = ""
	entry.ClaimUntil = time.Time{}
	if cause != nil {
		entry.LastError = cause.Error()
	}
	if classifyRetry(cause) == RetryClassDeterministic {
		entry.Terminal = true
		entry.NextAttempt = time.Time{}
	}
	s.entries[id] = entry
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
	delete(s.prepared, id)
	entry.Prepared = false
	entry.Version = version
	s.entries[id] = entry
	return entry, nil
}

func (s *outboxState) discardPrepared(id string) error {
	if _, ok := s.prepared[id]; !ok {
		return fmt.Errorf("outbox prepared entry %q not found", id)
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
