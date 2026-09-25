package runthrough

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type OutboxOperation string

var ErrOutboxVersionConflict = errors.New("outbox object version no longer matches the committed record")

const (
	OutboxPut    OutboxOperation = "put"
	OutboxDelete OutboxOperation = "delete"
)

type OutboxEntry struct {
	ID          string          `json:"id"`
	Operation   OutboxOperation `json:"operation"`
	Bucket      string          `json:"bucket"`
	Key         string          `json:"key"`
	Version     string          `json:"version,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	Attempts    int             `json:"attempts"`
	NextAttempt time.Time       `json:"next_attempt"`
	LastError   string          `json:"last_error,omitempty"`
	Terminal    bool            `json:"terminal,omitempty"`
}

type Outbox interface {
	Enqueue(entry OutboxEntry) (OutboxEntry, error)
	Pending() []OutboxEntry
	MarkSuccess(id string) error
	MarkFailure(id string, cause error, retryAt time.Time) error
	Discard(id string) error
	Close() error
}

type DurableOutbox interface {
	Outbox
	Durable() bool
}

type outboxState struct {
	entries map[string]OutboxEntry
	seq     uint64
}

func newOutboxState() outboxState {
	return outboxState{entries: make(map[string]OutboxEntry)}
}

func (s outboxState) clone() outboxState {
	entries := make(map[string]OutboxEntry, len(s.entries))
	for id, entry := range s.entries {
		entries[id] = entry
	}
	return outboxState{entries: entries, seq: s.seq}
}

func (s *outboxState) enqueue(entry OutboxEntry) OutboxEntry {
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
	s.entries[entry.ID] = entry
	return entry
}

func (s *outboxState) pending() []OutboxEntry {
	return pendingEntries(s.entries)
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

func (o *MemoryOutbox) Discard(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.discard(id)
}

func (o *MemoryOutbox) Close() error { return nil }

func (o *MemoryOutbox) Durable() bool { return false }

type FileOutbox struct {
	path  string
	mu    sync.Mutex
	state outboxState
}

type persistedOutbox struct {
	Entries map[string]OutboxEntry `json:"entries"`
	Seq     uint64                 `json:"seq"`
}

func NewFileOutbox(path string) (*FileOutbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	o := &FileOutbox{path: path, state: newOutboxState()}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return o, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return o, nil
	}
	var persisted persistedOutbox
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Entries == nil {
		if err := json.Unmarshal(data, &o.state.entries); err != nil {
			return nil, fmt.Errorf("decode outbox: %w", err)
		}
	} else {
		o.state.entries = persisted.Entries
		o.state.seq = persisted.Seq
	}
	for id := range o.state.entries {
		if !strings.HasPrefix(id, "outbox-") {
			continue
		}
		sequence, parseErr := strconv.ParseUint(strings.TrimPrefix(id, "outbox-"), 10, 64)
		if parseErr == nil && sequence > o.state.seq {
			o.state.seq = sequence
		}
	}
	return o, nil
}

func (o *FileOutbox) Enqueue(entry OutboxEntry) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	enqueued := next.enqueue(entry)
	if err := o.persistState(next); err != nil {
		return OutboxEntry{}, err
	}
	o.state = next
	return enqueued, nil
}

func (o *FileOutbox) Pending() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.pending()
}

func (o *FileOutbox) MarkSuccess(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	if err := next.markSuccess(id); err != nil {
		return err
	}
	if err := o.persistState(next); err != nil {
		return err
	}
	o.state = next
	return nil
}

func (o *FileOutbox) MarkFailure(id string, cause error, retryAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	if err := next.markFailure(id, cause, retryAt); err != nil {
		return err
	}
	if err := o.persistState(next); err != nil {
		return err
	}
	o.state = next
	return nil
}

func (o *FileOutbox) Discard(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	if err := next.discard(id); err != nil {
		return err
	}
	if err := o.persistState(next); err != nil {
		return err
	}
	o.state = next
	return nil
}

func (o *FileOutbox) Durable() bool { return true }

func (o *FileOutbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.persistLocked()
}

func (o *FileOutbox) persistLocked() error {
	return o.persistState(o.state)
}

func (o *FileOutbox) persistState(state outboxState) error {
	data, err := json.MarshalIndent(persistedOutbox{Entries: state.entries, Seq: state.seq}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(o.path), ".outbox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, o.path); err != nil {
		return err
	}
	if dirFile, err := os.Open(filepath.Dir(o.path)); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

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
