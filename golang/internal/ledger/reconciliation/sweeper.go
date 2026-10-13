// Package reconciliation repairs payouts that went quiet and checks the ledger invariants.
package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

type Summary struct {
	Settled      int
	Released     int
	Redriven     int
	StillPending int
	Errors       int
}

// Sweeper asks the provider about withdrawals still Pending after the threshold, and settles or releases them
// through the same handlers as the webhooks. A payout the provider has never seen stays held and is re-queued:
// releasing could both pay out and refund if a delayed message still submits it.
type Sweeper struct {
	store            *store.Store
	provider         providers.PaymentProvider
	settle           *withdrawals.SettleHandler
	release          *withdrawals.ReleaseHandler
	clock            common.Clock
	pendingThreshold time.Duration
	log              *slog.Logger
}

func NewSweeper(s *store.Store, provider providers.PaymentProvider, settle *withdrawals.SettleHandler,
	release *withdrawals.ReleaseHandler, clock common.Clock, pendingThreshold time.Duration, log *slog.Logger) *Sweeper {
	return &Sweeper{store: s, provider: provider, settle: settle, release: release, clock: clock, pendingThreshold: pendingThreshold, log: log}
}

func (s *Sweeper) Sweep(ctx context.Context) (Summary, error) {
	stale, err := s.store.StalePendingWithdrawals(ctx, s.clock.Now().Add(-s.pendingThreshold))
	if err != nil {
		return Summary{}, err
	}
	var summary Summary
	for _, transactionID := range stale {
		status, err := s.provider.GetPayoutStatus(ctx, providers.PayoutKey(transactionID))
		if err != nil {
			if ctx.Err() != nil {
				return summary, ctx.Err()
			}
			summary.Errors++
			s.log.Warn("querying the provider for withdrawal failed", "transaction_id", transactionID, "error", err)
			continue
		}
		switch status {
		case providers.StatusSucceeded:
			if _, err := s.settle.Handle(ctx, transactionID); err != nil {
				summary.Errors++
			} else {
				summary.Settled++
			}
		case providers.StatusFailed:
			if _, err := s.release.Handle(ctx, transactionID, "Provider reported the payout failed during reconciliation."); err != nil {
				summary.Errors++
			} else {
				summary.Released++
			}
		case providers.StatusNotFound:
			if err := s.redrive(ctx, transactionID); err != nil {
				summary.Errors++
				s.log.Warn("re-queueing payout failed", "transaction_id", transactionID, "error", err)
			} else {
				summary.Redriven++
			}
		default:
			summary.StillPending++
		}
	}
	return summary, nil
}

// redrive keeps the hold and queues the payout request again. The provider never received it (for example the
// message was dead-lettered) or has lost it. The payout key is idempotent and the processor skips final withdrawals.
func (s *Sweeper) redrive(ctx context.Context, transactionID uuid.UUID) error {
	_, err := store.Run(ctx, s.store, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		withdrawal, err := tx.Transaction(ctx, transactionID)
		if err != nil {
			return struct{}{}, err
		}
		if withdrawal == nil {
			return struct{}{}, fmt.Errorf("withdrawal %s not found", transactionID)
		}
		// A stored provider reference would make the processor skip the resubmission, so it goes in the same save.
		if withdrawal.ProviderReferenceID != nil {
			s.log.Error("provider lost accepted payout; resubmitting it",
				"transaction_id", transactionID, "provider_reference_id", *withdrawal.ProviderReferenceID)
			withdrawal.ClearProviderReference()
		}
		tx.AddOutbox(messages.NewPayoutRequested(transactionID, s.clock.Now()))
		return struct{}{}, nil
	})
	if err == nil {
		s.log.Warn("withdrawal is past the pending threshold but unknown to the provider; keeping the hold and re-queueing the payout",
			"transaction_id", transactionID)
	}
	return err
}
