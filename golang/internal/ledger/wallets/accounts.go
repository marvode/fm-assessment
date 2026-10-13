package wallets

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// GetOrCreate returns the user's wallet, staging a new one in tx when none exists. created reports which.
func GetOrCreate(ctx context.Context, tx *store.Tx, userID uuid.UUID, now time.Time) (*domain.Account, bool, error) {
	existing, err := tx.WalletByUser(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	account := domain.OpenUserWallet(userID, now)
	tx.AddAccount(account)
	return account, true, nil
}
