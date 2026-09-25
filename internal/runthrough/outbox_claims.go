package runthrough

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrOutboxClaimLost = errors.New("outbox claim is no longer owned")

const defaultOutboxClaimLease = 30 * time.Second

// ClaimableOutbox adds expiring ownership around propagation attempts. A
// claimant must finish with MarkClaimedSuccess or MarkClaimedFailure; an
// abandoned claim becomes available after ClaimUntil.
type ClaimableOutbox interface {
	Outbox
	Claim(id, owner string, lease time.Duration) (OutboxEntry, bool, error)
	Renew(id, owner string, lease time.Duration) error
	Release(id, owner string) error
	MarkClaimedSuccess(id, owner string) error
	MarkClaimedFailure(id, owner string, cause error, retryAt time.Time) error
}

func newOutboxOwner() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err == nil {
		return fmt.Sprintf("stow-%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	}
	return fmt.Sprintf("stow-%d", os.Getpid())
}

func (s *outboxState) claim(id, owner string, lease time.Duration) (OutboxEntry, bool, error) {
	entry, ok := s.entries[id]
	if !ok || entry.Terminal || !outboxEntryDue(entry, time.Now()) || !s.firstForKey(entry) {
		return OutboxEntry{}, false, nil
	}
	now := time.Now().UTC()
	if entry.ClaimOwner != "" && entry.ClaimUntil.After(now) && entry.ClaimOwner != owner {
		return OutboxEntry{}, false, nil
	}
	entry.ClaimOwner = owner
	entry.ClaimUntil = now.Add(lease)
	s.entries[id] = entry
	return entry, true, nil
}

func (s *outboxState) firstForKey(target OutboxEntry) bool {
	for id, entry := range s.entries {
		if id != target.ID && entry.Bucket == target.Bucket && entry.Key == target.Key && outboxEntryBefore(entry, target) {
			return false
		}
	}
	for id, entry := range s.prepared {
		if id != target.ID && entry.Bucket == target.Bucket && entry.Key == target.Key && outboxEntryBefore(entry, target) {
			return false
		}
	}
	return true
}

func (s *outboxState) renewClaim(id, owner string, lease time.Duration) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || !entry.ClaimUntil.After(time.Now().UTC()) {
		return ErrOutboxClaimLost
	}
	entry.ClaimUntil = time.Now().UTC().Add(lease)
	s.entries[id] = entry
	return nil
}

func (s *outboxState) releaseClaim(id, owner string) error {
	entry, ok := s.entries[id]
	if !ok {
		return nil
	}
	if entry.ClaimOwner != owner {
		return ErrOutboxClaimLost
	}
	entry.ClaimOwner = ""
	entry.ClaimUntil = time.Time{}
	s.entries[id] = entry
	return nil
}

func (s *outboxState) markClaimedSuccess(id, owner string) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || !entry.ClaimUntil.After(time.Now().UTC()) {
		return ErrOutboxClaimLost
	}
	delete(s.entries, id)
	return nil
}

func (s *outboxState) markClaimedFailure(id, owner string, cause error, retryAt time.Time) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || !entry.ClaimUntil.After(time.Now().UTC()) {
		return ErrOutboxClaimLost
	}
	if err := s.markFailure(id, cause, retryAt); err != nil {
		return err
	}
	entry = s.entries[id]
	entry.ClaimOwner = ""
	entry.ClaimUntil = time.Time{}
	s.entries[id] = entry
	return nil
}

func (o *FileOutbox) Claim(id, owner string, lease time.Duration) (OutboxEntry, bool, error) {
	var claimed OutboxEntry
	var ok bool
	err := o.withState(func(state *outboxState) error {
		var err error
		claimed, ok, err = state.claim(id, owner, lease)
		return err
	})
	if err != nil {
		return OutboxEntry{}, false, err
	}
	return claimed, ok, nil
}

func (o *FileOutbox) Renew(id, owner string, lease time.Duration) error {
	return o.withState(func(state *outboxState) error {
		return state.renewClaim(id, owner, lease)
	})
}

func (o *FileOutbox) Release(id, owner string) error {
	return o.withState(func(state *outboxState) error {
		return state.releaseClaim(id, owner)
	})
}

func (o *FileOutbox) MarkClaimedSuccess(id, owner string) error {
	return o.withState(func(state *outboxState) error {
		return state.markClaimedSuccess(id, owner)
	})
}

func (o *FileOutbox) MarkClaimedFailure(id, owner string, cause error, retryAt time.Time) error {
	return o.withState(func(state *outboxState) error {
		return state.markClaimedFailure(id, owner, cause, retryAt)
	})
}
