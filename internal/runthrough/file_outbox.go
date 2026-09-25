package runthrough

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FileOutbox struct {
	path  string
	mu    sync.Mutex
	state outboxState
}

type persistedOutbox struct {
	Entries  map[string]OutboxEntry `json:"entries"`
	Prepared map[string]OutboxEntry `json:"prepared,omitempty"`
	Seq      uint64                 `json:"seq"`
}

func NewFileOutbox(path string) (*FileOutbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	o := &FileOutbox{path: path, state: newOutboxState()}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return o, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return o, nil
	}
	state, err := decodeOutboxState(data)
	if err != nil {
		return nil, err
	}
	o.state = state
	o.updateSequences()
	return o, nil
}

func decodeOutboxState(data []byte) (outboxState, error) {
	var persisted persistedOutbox
	if err := json.Unmarshal(data, &persisted); err != nil {
		return outboxState{}, fmt.Errorf("decode outbox: %w", err)
	}
	if persisted.Entries == nil && persisted.Prepared == nil {
		var legacy map[string]OutboxEntry
		if err := json.Unmarshal(data, &legacy); err != nil {
			return outboxState{}, fmt.Errorf("decode legacy outbox: %w", err)
		}
		persisted.Entries = legacy
	}
	if persisted.Entries == nil {
		persisted.Entries = make(map[string]OutboxEntry)
	}
	if persisted.Prepared == nil {
		persisted.Prepared = make(map[string]OutboxEntry)
	}
	for id, entry := range persisted.Entries {
		if !entry.Prepared {
			continue
		}
		delete(persisted.Entries, id)
		persisted.Prepared[id] = entry
	}
	for id := range persisted.Prepared {
		if _, exists := persisted.Entries[id]; exists {
			return outboxState{}, fmt.Errorf("outbox entry %q is both prepared and active", id)
		}
	}
	return outboxState{entries: persisted.Entries, prepared: persisted.Prepared, seq: persisted.Seq}, nil
}

func (o *FileOutbox) updateSequences() {
	for id := range o.state.entries {
		o.updateSequence(id)
	}
	for id := range o.state.prepared {
		o.updateSequence(id)
	}
}

func (o *FileOutbox) updateSequence(id string) {
	if !strings.HasPrefix(id, "outbox-") {
		return
	}
	sequence, err := strconv.ParseUint(strings.TrimPrefix(id, "outbox-"), 10, 64)
	if err == nil && sequence > o.state.seq {
		o.state.seq = sequence
	}
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

func (o *FileOutbox) Prepare(entry OutboxEntry) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	prepared := next.prepare(entry)
	if err := o.persistState(next); err != nil {
		return OutboxEntry{}, err
	}
	o.state = next
	return prepared, nil
}

func (o *FileOutbox) Prepared() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.preparedEntries()
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

func (o *FileOutbox) Commit(id, version string) (OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	committed, err := next.commit(id, version)
	if err != nil {
		return OutboxEntry{}, err
	}
	if err := o.persistState(next); err != nil {
		return OutboxEntry{}, err
	}
	o.state = next
	return committed, nil
}

func (o *FileOutbox) DiscardPrepared(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.state.clone()
	if err := next.discardPrepared(id); err != nil {
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
	data, err := json.MarshalIndent(persistedOutbox{Entries: state.entries, Prepared: state.prepared, Seq: state.seq}, "", "  ")
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
