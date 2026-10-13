package withdrawals

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// ReleaseHandler: the payout definitely failed, so give the held amount back to AvailableBalance. There are no
// ledger entries, because no money moved.
type ReleaseHandler struct {
	store *store.Store
	clock common.Clock
	log   *slog.Logger
}

func NewReleaseHandler(s *store.Store, clock common.Clock, log *slog.Logger) *ReleaseHandler {
	return &ReleaseHandler{store: s, clock: clock, log: log}
}

func (h *ReleaseHandler) Handle(ctx context.Context, transactionID uuid.UUID, reason string) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		withdrawal, err := findWithdrawal(ctx, tx, transactionID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if withdrawal.IsFinal() {
			return wallets.FromTransaction(withdrawal), nil
		}
		wallet, err := tx.Account(ctx, withdrawal.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if _, err := wallet.ReleaseHold(withdrawal.Amount); err != nil {
			return wallets.TransactionResponse{}, err
		}
		withdrawal.MarkFailed(h.clock.Now())
		h.log.Warn("released withdrawal hold", "transaction_id", withdrawal.ID, "amount", withdrawal.Amount, "reason", reason)
		return wallets.FromTransaction(withdrawal), nil
	})
}
