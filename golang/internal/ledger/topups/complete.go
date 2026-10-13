package topups

import (
	"context"
	"log/slog"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// CompleteHandler applies the provider's verdict on a charge. Repeat deliveries for a final top-up change nothing.
type CompleteHandler struct {
	store *store.Store
	clock common.Clock
	log   *slog.Logger
}

func NewCompleteHandler(s *store.Store, clock common.Clock, log *slog.Logger) *CompleteHandler {
	return &CompleteHandler{store: s, clock: clock, log: log}
}

func (h *CompleteHandler) Succeed(ctx context.Context, providerReferenceID string, amount int64) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		topUp, err := find(ctx, tx, providerReferenceID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if topUp.IsFinal() {
			return wallets.FromTransaction(topUp), nil
		}
		now := h.clock.Now()
		if topUp.Amount != amount {
			h.log.Error("top-up amount mismatch; marking it failed",
				"transaction_id", topUp.ID, "expected_amount", topUp.Amount, "reported_amount", amount)
			topUp.MarkFailed(now)
			return wallets.FromTransaction(topUp), nil
		}
		wallet, err := tx.Account(ctx, topUp.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		entries, err := domain.Post(topUp, settlement, wallet, false, now)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		tx.AddEntries(entries...)
		topUp.MarkSucceeded(now)
		return wallets.FromTransaction(topUp), nil
	})
}

func (h *CompleteHandler) Fail(ctx context.Context, providerReferenceID string) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		topUp, err := find(ctx, tx, providerReferenceID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if !topUp.IsFinal() {
			topUp.MarkFailed(h.clock.Now())
		}
		return wallets.FromTransaction(topUp), nil
	})
}

func find(ctx context.Context, tx *store.Tx, providerReferenceID string) (*domain.Transaction, error) {
	topUp, err := tx.TransactionByProviderReference(ctx, providerReferenceID, domain.TopUp)
	if err != nil {
		return nil, err
	}
	if topUp == nil {
		return nil, domain.ErrTransactionNotFound
	}
	return topUp, nil
}
