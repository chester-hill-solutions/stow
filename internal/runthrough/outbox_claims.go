package runthrough

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

var (
	ErrOutboxClaimLost  = errors.New("outbox claim is no longer owned")
	ErrOutboxClaimHeld  = errors.New("outbox entry is claimed")
	ErrOutboxClaimLease = errors.New("outbox claim lease must be positive")
)

const defaultOutboxClaimLease = 30 * time.Second

// ClaimedEntry is an outbox entry leased to a single claimant. Reconcile
// reports that an earlier attempt may already have reached upstream, so the
// claimant must compare upstream state against the immutable local version
// before propagating again. It is a property of this claim, not durable entry
// state: a worker that dies mid-flight is recovered by the next claimant, which
// sees the persisted Attempted marker and is told to reconcile.
type ClaimedEntry struct {
	Entry     OutboxEntry
	Reconcile bool
}

// ClaimableOutbox adds expiring ownership around propagation attempts. A
// claimant must finish with MarkClaimedSuccess or MarkClaimedFailure; an
// abandoned claim becomes available after ClaimUntil.
type ClaimableOutbox interface {
	Outbox
	Claim(id, owner string, lease time.Duration) (ClaimedEntry, bool, error)
	Renew(id, owner string, token uint64, lease time.Duration) error
	Release(id, owner string, token uint64) error
	MarkClaimedSuccess(id, owner string, token uint64) error
	MarkClaimedFailure(id, owner string, token uint64, cause error, retryAt time.Time) error
}

// OwnedPreparedOutbox extends the coordinated prepare phase with an owner and
// fencing token. A process may recover an owned intent only after its lease
// expires; an active owner must acknowledge the same token it prepared.
type OwnedPreparedOutbox interface {
	CoordinatedOutbox
	PrepareOwned(entry OutboxEntry, owner string, lease time.Duration) (OutboxEntry, error)
	RenewPrepared(id, owner string, token uint64, lease time.Duration) error
	CommitPrepared(id, owner string, token uint64, version string) (OutboxEntry, error)
	DiscardPreparedOwned(id, owner string, token uint64) error
}

func newOutboxOwner() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err == nil {
		return fmt.Sprintf("stow-%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	}
	return fmt.Sprintf("stow-%d", os.Getpid())
}

func (s *outboxState) claim(id, owner string, lease time.Duration) (ClaimedEntry, bool, error) {
	if owner == "" || lease <= 0 {
		return ClaimedEntry{}, false, ErrOutboxClaimLease
	}
	entry, ok := s.entries[id]
	if !ok || entry.Terminal || !outboxEntryDue(entry, time.Now()) || !s.firstForKey(entry) {
		return ClaimedEntry{}, false, nil
	}
	now := time.Now().UTC()
	if entry.ClaimOwner != "" {
		if entry.ClaimUntil.After(now) || outboxOwnerAlive(entry.ClaimOwner) {
			return ClaimedEntry{}, false, nil
		}
	}
	claimed := ClaimedEntry{Entry: entry, Reconcile: entry.Attempted}
	entry.ClaimOwner = owner
	entry.ClaimUntil = now.Add(lease)
	entry.ClaimToken = s.assignToken()
	entry.Attempted = true
	s.entries[id] = entry
	claimed.Entry = entry
	return claimed, true, nil
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

func (s *outboxState) renewClaim(id, owner string, token uint64, lease time.Duration) error {
	if lease <= 0 {
		return ErrOutboxClaimLease
	}
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || entry.ClaimToken != token {
		return ErrOutboxClaimLost
	}
	entry.ClaimUntil = time.Now().UTC().Add(lease)
	s.entries[id] = entry
	return nil
}

func (s *outboxState) releaseClaim(id, owner string, token uint64) error {
	entry, ok := s.entries[id]
	if !ok {
		return nil
	}
	if entry.ClaimOwner != owner || entry.ClaimToken != token {
		return ErrOutboxClaimLost
	}
	entry.ClaimOwner = ""
	entry.ClaimUntil = time.Time{}
	entry.ClaimToken = 0
	s.entries[id] = entry
	return nil
}

func (s *outboxState) markClaimedSuccess(id, owner string, token uint64) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || entry.ClaimToken != token {
		return ErrOutboxClaimLost
	}
	delete(s.entries, id)
	return nil
}

func (s *outboxState) markClaimedFailure(id, owner string, token uint64, cause error, retryAt time.Time) error {
	entry, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("outbox entry %q not found", id)
	}
	if entry.ClaimOwner != owner || entry.ClaimToken != token {
		return ErrOutboxClaimLost
	}
	if err := s.markFailureUnchecked(entry, cause, retryAt); err != nil {
		return err
	}
	entry = s.entries[id]
	entry.ClaimOwner = ""
	entry.ClaimUntil = time.Time{}
	entry.ClaimToken = 0
	s.entries[id] = entry
	return nil
}

func (o *FileOutbox) PrepareOwned(entry OutboxEntry, owner string, lease time.Duration) (OutboxEntry, error) {
	var prepared OutboxEntry
	err := o.withState(func(state *outboxState) error {
		var err error
		prepared, err = state.prepareOwned(entry, owner, lease)
		return err
	})
	if err != nil {
		return OutboxEntry{}, err
	}
	return prepared, nil
}

func (o *FileOutbox) RenewPrepared(id, owner string, token uint64, lease time.Duration) error {
	return o.withState(func(state *outboxState) error {
		return state.renewPrepared(id, owner, token, lease)
	})
}

func (o *FileOutbox) CommitPrepared(id, owner string, token uint64, version string) (OutboxEntry, error) {
	var committed OutboxEntry
	err := o.withState(func(state *outboxState) error {
		var err error
		committed, err = state.commitPrepared(id, owner, token, version)
		return err
	})
	if err != nil {
		return OutboxEntry{}, err
	}
	return committed, nil
}

func (o *FileOutbox) DiscardPreparedOwned(id, owner string, token uint64) error {
	return o.withState(func(state *outboxState) error {
		return state.discardPreparedOwned(id, owner, token)
	})
}

func (o *FileOutbox) Claim(id, owner string, lease time.Duration) (ClaimedEntry, bool, error) {
	var claimed ClaimedEntry
	var ok bool
	err := o.withState(func(state *outboxState) error {
		var err error
		claimed, ok, err = state.claim(id, owner, lease)
		return err
	})
	if err != nil {
		return ClaimedEntry{}, false, err
	}
	return claimed, ok, nil
}

func (o *FileOutbox) Renew(id, owner string, token uint64, lease time.Duration) error {
	return o.withState(func(state *outboxState) error {
		return state.renewClaim(id, owner, token, lease)
	})
}

func (o *FileOutbox) Release(id, owner string, token uint64) error {
	return o.withState(func(state *outboxState) error {
		return state.releaseClaim(id, owner, token)
	})
}

func (o *FileOutbox) MarkClaimedSuccess(id, owner string, token uint64) error {
	return o.withState(func(state *outboxState) error {
		return state.markClaimedSuccess(id, owner, token)
	})
}

func (o *FileOutbox) MarkClaimedFailure(id, owner string, token uint64, cause error, retryAt time.Time) error {
	return o.withState(func(state *outboxState) error {
		return state.markClaimedFailure(id, owner, token, cause, retryAt)
	})
}

var _ ClaimableOutbox = (*FileOutbox)(nil)
var _ OwnedPreparedOutbox = (*FileOutbox)(nil)
