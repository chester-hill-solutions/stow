package runthrough

import (
	"context"
	"time"
)

type propagationClaim struct {
	provider ClaimableOutbox
	entry    OutboxEntry
	acquired bool
}

func (a *Adapter) claimPropagation(entry OutboxEntry) (propagationClaim, error) {
	provider, ok := a.outbox.(ClaimableOutbox)
	if !ok {
		return propagationClaim{entry: entry, acquired: true}, nil
	}
	claimed, acquired, err := provider.Claim(entry.ID, a.claimOwner, a.claimLease)
	if err != nil {
		return propagationClaim{}, err
	}
	if !acquired {
		return propagationClaim{}, nil
	}
	return propagationClaim{provider: provider, entry: claimed, acquired: true}, nil
}

func (claim propagationClaim) success() error {
	if claim.provider == nil {
		return nil
	}
	return claim.provider.MarkClaimedSuccess(claim.entry.ID, claim.entry.ClaimOwner, claim.entry.ClaimToken)
}

func (claim propagationClaim) failure(cause error, retryAt time.Time) error {
	if claim.provider == nil {
		return nil
	}
	return claim.provider.MarkClaimedFailure(claim.entry.ID, claim.entry.ClaimOwner, claim.entry.ClaimToken, cause, retryAt)
}

func (claim propagationClaim) propagate(ctx context.Context, a *Adapter) error {
	if claim.provider == nil || a.claimLease <= 0 {
		return a.propagateEntry(ctx, claim.entry)
	}
	claimCtx, cancel := context.WithCancel(ctx)
	renewed := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := a.claimLease / 3
		if interval <= 0 {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-claimCtx.Done():
				return
			case <-ticker.C:
				if err := claim.provider.Renew(claim.entry.ID, claim.entry.ClaimOwner, claim.entry.ClaimToken, a.claimLease); err != nil {
					select {
					case renewed <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	err := a.propagateEntry(claimCtx, claim.entry)
	cancel()
	<-done
	if err != nil {
		return err
	}
	select {
	case renewErr := <-renewed:
		return renewErr
	default:
		return nil
	}
}
