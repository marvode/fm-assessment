package withdrawals

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// SettleHandler: the provider paid out, so post the held amount from the wallet to settlement. Called by the
// webhook and the sweeper.
type SettleHandler struct {
	store *store.Store
	clock common.Clock
}

func NewSettleHandler(s *store.Store, clock common.Clock) *SettleHandler {
	return &SettleHandler{store: s, clock: clock}
}

func (h *SettleHandler) Handle(ctx context.Context, transactionID uuid.UUID) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		withdrawal, err := findWithdrawal(ctx, tx, transactionID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if withdrawal.IsFinal() {
			return wallets.FromTransaction(withdrawal), nil
		}
		now := h.clock.Now()
		wallet, err := tx.Account(ctx, withdrawal.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		entries, err := domain.Post(withdrawal, wallet, settlement, true, now)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		tx.AddEntries(entries...)
		withdrawal.MarkSucceeded(now)
		return wallets.FromTransaction(withdrawal), nil
	})
}

func findWithdrawal(ctx context.Context, tx *store.Tx, transactionID uuid.UUID) (*domain.Transaction, error) {
	withdrawal, err := tx.Transaction(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	if withdrawal == nil || withdrawal.Type != domain.Withdrawal {
		return nil, domain.ErrTransactionNotFound
	}
	return withdrawal, nil
}
