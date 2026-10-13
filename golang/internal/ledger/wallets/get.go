package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

type GetHandler struct {
	store *store.Store
}

func NewGetHandler(s *store.Store) *GetHandler { return &GetHandler{store: s} }

func (h *GetHandler) Handle(ctx context.Context, userID uuid.UUID) (WalletResponse, error) {
	account, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return WalletResponse{}, err
	}
	if account == nil {
		return WalletResponse{}, domain.ErrWalletNotFound
	}
	return FromAccount(account), nil
}
