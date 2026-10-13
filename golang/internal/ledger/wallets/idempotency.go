package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// FindReplay returns nil when referenceID is new for this wallet. Otherwise it returns the original transaction when
// the request matches, or ErrDuplicateReference when the same referenceID arrives with a different request.
func FindReplay(ctx context.Context, tx *store.Tx, accountID uuid.UUID, referenceID, requestHash string) (*IdempotentResponse, error) {
	existing, err := tx.TransactionByReference(ctx, accountID, referenceID)
	if err != nil || existing == nil {
		return nil, err
	}
	if existing.RequestHash != requestHash {
		return nil, domain.ErrDuplicateReference
	}
	return &IdempotentResponse{Transaction: FromTransaction(existing), Replayed: true}, nil
}
