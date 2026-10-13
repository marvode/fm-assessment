package ledgertest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

func WithdrawalCommand(userID uuid.UUID, amount int64, referenceID string) withdrawals.RequestCommand {
	return withdrawals.RequestCommand{
		UserID: userID, Amount: amount, ReferenceID: referenceID,
		BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace",
	}
}

// PendingWithdrawal seeds a funded wallet with one held, Pending withdrawal ("wd-1") and returns its transaction id.
func PendingWithdrawal(t *testing.T, s *store.Store, clock common.Clock, userID uuid.UUID, balance, amount int64) uuid.UUID {
	t.Helper()
	SeedWallet(t, s, userID, balance, clock.Now())
	result, err := withdrawals.NewRequestHandler(s, clock).Handle(context.Background(), WithdrawalCommand(userID, amount, "wd-1"))
	require.NoError(t, err)
	return result.Transaction.ID
}

func AssignProviderReference(t *testing.T, s *store.Store, transactionID uuid.UUID, providerReferenceID string) {
	t.Helper()
	_, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		transaction, err := tx.Transaction(ctx, transactionID)
		if err != nil {
			return struct{}{}, err
		}
		transaction.AssignProviderReference(providerReferenceID)
		return struct{}{}, nil
	})
	require.NoError(t, err)
}
