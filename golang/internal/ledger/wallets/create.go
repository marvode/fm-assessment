package wallets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
)

type CreateHandler struct {
	store *store.Store
	clock common.Clock
}

func NewCreateHandler(s *store.Store, clock common.Clock) *CreateHandler {
	return &CreateHandler{store: s, clock: clock}
}

func (h *CreateHandler) Handle(ctx context.Context, userID uuid.UUID) (Creation, error) {
	creation, err := store.Run(ctx, h.store, func(ctx context.Context, tx *store.Tx) (Creation, error) {
		wallet, created, err := GetOrCreate(ctx, tx, userID, h.clock.Now())
		if err != nil {
			return Creation{}, err
		}
		return Creation{Wallet: FromAccount(wallet), Created: created}, nil
	})
	if !errors.Is(err, store.ErrConflict) {
		return creation, err
	}
	// A concurrent create won the unique (user_id, currency) index; return the winner's wallet.
	winner, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return Creation{}, err
	}
	if winner == nil {
		return Creation{}, fmt.Errorf("wallet for user %s missing after a conflicting create", userID)
	}
	return Creation{Wallet: FromAccount(winner)}, nil
}
