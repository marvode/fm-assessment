// Package topups starts charges with the provider and applies the provider's verdict.
package topups

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

const maxInitiateAttempts = 3

type InitiateCommand struct {
	UserID      uuid.UUID
	ReferenceID string
	Amount      int64
}

// InitiateHandler starts a charge with the provider and records a Pending top-up. Money moves only when the
// provider's webhook confirms it. The user's wallet is opened on the first top-up.
type InitiateHandler struct {
	store    *store.Store
	provider providers.PaymentProvider
	clock    common.Clock
}

func NewInitiateHandler(s *store.Store, provider providers.PaymentProvider, clock common.Clock) *InitiateHandler {
	return &InitiateHandler{store: s, provider: provider, clock: clock}
}

func (h *InitiateHandler) Handle(ctx context.Context, command InitiateCommand) (wallets.IdempotentResponse, error) {
	if command.Amount <= 0 {
		return wallets.IdempotentResponse{}, domain.ErrInvalidAmount
	}
	if domain.BlankOrLonger(command.ReferenceID, domain.ReferenceIDMaxLength) {
		return wallets.IdempotentResponse{}, domain.Validation("ReferenceId",
			fmt.Sprintf("ReferenceId is required and at most %d characters.", domain.ReferenceIDMaxLength))
	}
	requestHash := domain.RequestHash(string(domain.TopUp), command.UserID, command.Amount)

	for attempt := 1; ; attempt++ {
		response, err := h.attempt(ctx, command, requestHash)
		if !errors.Is(err, store.ErrConflict) {
			return response, err
		}
		// A concurrent request committed this referenceId or opened this user's wallet first (unique index).
		// Retry against what it committed: the replay check or the existing wallet now finds it.
		if attempt == maxInitiateAttempts {
			return wallets.IdempotentResponse{}, domain.ErrConcurrencyExhausted
		}
	}
}

// attempt opens the wallet (or replays), charges the provider with no database transaction open, then records the
// Pending top-up. Holding a pooled connection across the provider call would let slow charges starve the pool.
func (h *InitiateHandler) attempt(ctx context.Context, command InitiateCommand, requestHash string) (wallets.IdempotentResponse, error) {
	type prepared struct {
		walletID uuid.UUID
		replay   *wallets.IdempotentResponse
	}
	ready, err := store.Run(ctx, h.store, func(ctx context.Context, tx *store.Tx) (prepared, error) {
		wallet, created, err := wallets.GetOrCreate(ctx, tx, command.UserID, h.clock.Now())
		if err != nil || created {
			return prepared{walletID: walletID(wallet)}, err
		}
		replay, err := wallets.FindReplay(ctx, tx, wallet.ID, command.ReferenceID, requestHash)
		return prepared{walletID: wallet.ID, replay: replay}, err
	})
	if err != nil {
		return wallets.IdempotentResponse{}, err
	}
	if ready.replay != nil {
		return *ready.replay, nil
	}

	// Idempotent per key, so a retry after a lost race reuses the same provider charge.
	charge, err := h.provider.InitiateCharge(ctx, providers.ChargeKey(ready.walletID, command.ReferenceID), command.Amount)
	if err != nil {
		return wallets.IdempotentResponse{}, err
	}

	return store.Run(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.IdempotentResponse, error) {
		replay, err := wallets.FindReplay(ctx, tx, ready.walletID, command.ReferenceID, requestHash)
		if err != nil {
			return wallets.IdempotentResponse{}, err
		}
		if replay != nil {
			return *replay, nil
		}
		topUp := domain.StartTopUp(ready.walletID, command.Amount, command.ReferenceID, requestHash, charge.ProviderReferenceID, h.clock.Now())
		tx.AddTransaction(topUp)
		return wallets.IdempotentResponse{Transaction: wallets.FromTransaction(topUp)}, nil
	})
}

func walletID(wallet *domain.Account) uuid.UUID {
	if wallet == nil {
		return uuid.Nil
	}
	return wallet.ID
}
