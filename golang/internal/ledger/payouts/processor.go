// Package payouts sends held withdrawals to the payment provider: the RabbitMQ consumer feeds Processor.
package payouts

import (
	"context"
	"fmt"
	"log/slog"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

type Outcome string

const (
	Initiated Outcome = "Initiated"
	Rejected  Outcome = "Rejected"
	Skipped   Outcome = "Skipped"
)

// Processor sends one held withdrawal to the provider. Acceptance only records the provider reference: the webhook
// or the sweeper settles it later. A definite rejection releases the hold. Transient faults and ambiguous answers
// return an error so the message is redelivered.
type Processor struct {
	store    *store.Store
	provider providers.PaymentProvider
	release  *withdrawals.ReleaseHandler
	log      *slog.Logger
}

func NewProcessor(s *store.Store, provider providers.PaymentProvider, release *withdrawals.ReleaseHandler, log *slog.Logger) *Processor {
	return &Processor{store: s, provider: provider, release: release, log: log}
}

func (p *Processor) Process(ctx context.Context, message messages.PayoutRequested) (Outcome, error) {
	withdrawal, err := p.store.Transaction(ctx, message.TransactionID)
	if err != nil {
		return "", err
	}
	if withdrawal == nil || withdrawal.Type != domain.Withdrawal {
		p.log.Warn("payout requested for unknown withdrawal; skipping", "transaction_id", message.TransactionID)
		return Skipped, nil
	}
	// A provider reference means an earlier delivery already got this payout accepted.
	if withdrawal.IsFinal() || withdrawal.ProviderReferenceID != nil {
		return Skipped, nil
	}
	details, err := p.store.Withdrawal(ctx, withdrawal.ID)
	if err != nil {
		return "", err
	}
	initiation, err := p.provider.InitiatePayout(ctx, providers.PayoutInstruction{
		ReferenceID: providers.PayoutKey(withdrawal.ID), Amount: withdrawal.Amount,
		BankCode: details.BankCode, AccountNumber: details.AccountNumber, AccountName: details.AccountName,
	})
	if err != nil {
		return "", fmt.Errorf("initiate payout for withdrawal %s: %w", withdrawal.ID, err)
	}

	switch {
	case initiation.Outcome == providers.Accepted && initiation.ProviderReferenceID != nil:
		if err := p.assignProviderReference(ctx, withdrawal, *initiation.ProviderReferenceID); err != nil {
			return "", err
		}
		return Initiated, nil
	case initiation.Outcome == providers.Rejected:
		reason := "Provider rejected the payout."
		if initiation.FailureReason != nil {
			reason = *initiation.FailureReason
		}
		if _, err := p.release.Handle(ctx, withdrawal.ID, reason); err != nil {
			return "", err
		}
		return Rejected, nil
	default:
		// Ambiguous: the provider may have paid. Releasing could double-spend, so fail the delivery and let it retry.
		return "", fmt.Errorf("provider returned an unusable payout result (%s, no reference) for withdrawal %s",
			initiation.Outcome, withdrawal.ID)
	}
}

// assignProviderReference saves the reference only if the withdrawal's status has not changed since it was read, as
// the EF status concurrency token did. A changed status fails the delivery; the redelivery then skips the final
// withdrawal.
func (p *Processor) assignProviderReference(ctx context.Context, read *domain.Transaction, providerReferenceID string) error {
	_, err := store.Run(ctx, p.store, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		current, err := tx.Transaction(ctx, read.ID)
		if err != nil {
			return struct{}{}, err
		}
		if current == nil || current.Status != read.Status {
			return struct{}{}, store.ErrConflict
		}
		current.AssignProviderReference(providerReferenceID)
		return struct{}{}, nil
	})
	return err
}
