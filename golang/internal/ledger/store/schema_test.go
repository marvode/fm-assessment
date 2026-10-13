package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
)

func TestTheProviderSettlementAccountIsSeeded(t *testing.T) {
	s := ledgertest.NewStore(t)

	settlement, err := s.Queries().GetAccount(context.Background(), domain.ProviderSettlementID)

	require.NoError(t, err)
	require.Equal(t, "System", settlement.Type)
	require.Equal(t, domain.ProviderSettlementName, *settlement.Name)
	require.Nil(t, settlement.UserID)
	require.Zero(t, settlement.Balance)
	require.Equal(t, "NGN", settlement.Currency)
}

const insertWallet = `INSERT INTO accounts (id, user_id, type, currency, balance, available_balance, version, created_at)
VALUES ($1, $2, 'User', $3, $4, $5, 0, now())`

const insertTransaction = `INSERT INTO transactions (id, account_id, type, status, amount, reference_id,
provider_reference_id, request_hash, created_at) VALUES ($1, $2, 'TopUp', 'Pending', $3, $4, $5, 'h', now())`

func TestAUserHasOneWalletPerCurrency(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, userID := context.Background(), uuid.New()
	_, err := pool.Exec(ctx, insertWallet, uuid.New(), userID, "NGN", 0, 0)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insertWallet, uuid.New(), userID, "NGN", 0, 0)

	ledgertest.RequirePgCode(t, err, "23505")
}

func TestReferenceIDsAreUniquePerWalletAndProviderReferencesAreUnique(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, walletID := context.Background(), uuid.New()
	_, err := pool.Exec(ctx, insertWallet, walletID, uuid.New(), "NGN", 0, 0)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-1", "chg_1")
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-1", "chg_2")
	ledgertest.RequirePgCode(t, err, "23505")

	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-2", "chg_1")
	ledgertest.RequirePgCode(t, err, "23505")
}

func TestBalanceAndAmountRulesAreCheckConstraints(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, walletID := context.Background(), uuid.New()

	_, err := pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "NGN", -1, -1)
	ledgertest.RequirePgCode(t, err, "23514")
	_, err = pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "NGN", 10, 11)
	ledgertest.RequirePgCode(t, err, "23514")
	_, err = pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "USD", 0, 0)
	ledgertest.RequirePgCode(t, err, "23514")

	_, err = pool.Exec(ctx, insertWallet, walletID, uuid.New(), "NGN", 0, 0)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 0, "ref-0", nil)
	ledgertest.RequirePgCode(t, err, "23514")
}
