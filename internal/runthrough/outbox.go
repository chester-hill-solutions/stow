package runthrough

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type OutboxEntry struct {
	ID           string    `json:"id"`
	Operation    string    `json:"operation"`
	Bucket       string    `json:"bucket"`
	Key          string    `json:"key"`
	SourceBucket string    `json:"source_bucket,omitempty"`
	SourceKey    string    `json:"source_key,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	Attempts     int       `json:"attempts"`
	NextAttempt  time.Time `json:"next_attempt"`
	LastError    string    `json:"last_error,omitempty"`
}

type Outbox interface {
	Enqueue(entry OutboxEntry) error
	Pending() []OutboxEntry
	MarkSuccess(id string) error
	MarkFailure(id string, cause error, retryAt time.Time) error
	Close() error
}

type MemoryOutbox struct {
	mu      sync.Mutex
	entries map[string]OutboxEntry
	seq     uint64
}

func NewMemoryOutbox() *MemoryOutbox {
	return &MemoryOutbox{entries: make(map[string]OutboxEntry)}
}

func (o *MemoryOutbox) Enqueue(entry OutboxEntry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("outbox-%d", atomic.AddUint64(&o.seq, 1))
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	o.entries[entry.ID] = entry
	return nil
}

func (o *MemoryOutbox) Pending() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return pendingEntries(o.entries)
}

func (o *MemoryOutbox) MarkSuccess(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.entries, id)
	return nil
}

func (o *MemoryOutbox) MarkFailure(id string, cause error, retryAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	entry.Attempts++
	entry.NextAttempt = retryAt
	if cause != nil {
		entry.LastError = cause.Error()
	}
	o.entries[id] = entry
	return nil
}

func (o *MemoryOutbox) Close() error { return nil }

type FileOutbox struct {
	path    string
	mu      sync.Mutex
	entries map[string]OutboxEntry
	seq     uint64
}

func NewFileOutbox(path string) (*FileOutbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	o := &FileOutbox{path: path, entries: make(map[string]OutboxEntry)}
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
	if err := json.Unmarshal(data, &o.entries); err != nil {
		return nil, fmt.Errorf("decode outbox: %w", err)
	}
	return o, nil
}

func (o *FileOutbox) Enqueue(entry OutboxEntry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("outbox-%d", atomic.AddUint64(&o.seq, 1))
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	o.entries[entry.ID] = entry
	return o.persistLocked()
}

func (o *FileOutbox) Pending() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return pendingEntries(o.entries)
}

func (o *FileOutbox) MarkSuccess(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.entries, id)
	return o.persistLocked()
}

func (o *FileOutbox) MarkFailure(id string, cause error, retryAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	entry.Attempts++
	entry.NextAttempt = retryAt
	if cause != nil {
		entry.LastError = cause.Error()
	}
	o.entries[id] = entry
	return o.persistLocked()
}

func (o *FileOutbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.persistLocked()
}

func (o *FileOutbox) persistLocked() error {
	data, err := json.MarshalIndent(o.entries, "", "  ")
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
	return os.Rename(tmpName, o.path)
}

func pendingEntries(entries map[string]OutboxEntry) []OutboxEntry {
	out := make([]OutboxEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}
