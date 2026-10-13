package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

const pageSize = 100

type ListHandler struct {
	store *store.Store
}

func NewListHandler(s *store.Store) *ListHandler { return &ListHandler{store: s} }

// Handle returns the wallet's most recent transactions, newest first.
func (h *ListHandler) Handle(ctx context.Context, userID uuid.UUID) ([]TransactionResponse, error) {
	account, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, domain.ErrWalletNotFound
	}
	transactions, err := h.store.Transactions(ctx, account.ID, pageSize)
	if err != nil {
		return nil, err
	}
	responses := make([]TransactionResponse, len(transactions))
	for i, t := range transactions {
		responses[i] = FromTransaction(t)
	}
	return responses, nil
}
