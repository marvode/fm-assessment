package reconciliation_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/reconciliation"
	"wallet/internal/ledger/withdrawals"
)

func (f *fixture) invariants(t *testing.T) []string {
	t.Helper()
	violations, err := reconciliation.NewChecker(f.s, ledgertest.Logger()).Check(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, v := range violations {
		names = append(names, v.Invariant)
	}
	return names
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.s.Pool().Exec(ctx, sql, args...)
	require.NoError(t, err)
}

func TestAHealthyLedgerHasNoViolations(t *testing.T) {
	f := newFixture(t)
	settledID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	_, err := withdrawals.NewSettleHandler(f.s, f.clock).Handle(ctx, settledID)
	require.NoError(t, err)
	_, err = withdrawals.NewRequestHandler(f.s, f.clock).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 1_000, "wd-2"))
	require.NoError(t, err)

	require.Empty(t, f.invariants(t))
}

// Postgres enforces the foreign key InMemory ignored, so the stray entry joins the wallet's seed top-up.
func TestDetectsAnUnbalancedTransactionAndTheAccountDriftItCauses(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	var topUpID uuid.UUID
	require.NoError(t, f.s.Pool().QueryRow(ctx, "SELECT id FROM transactions WHERE account_id = $1", walletID).Scan(&topUpID))
	f.exec(t, `INSERT INTO ledger_entries (id, transaction_id, account_id, direction, amount, currency, balance_before,
		balance_after, created_at) VALUES ($1, $2, $3, 'Credit', 50, 'NGN', 10000, 10050, $4)`,
		uuid.New(), topUpID, walletID, ledgertest.Start)

	invariants := f.invariants(t)

	require.Contains(t, invariants, "I1")
	require.Contains(t, invariants, "I3")
}

func TestDetectsABalanceEditedOutsideTheLedger(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "UPDATE accounts SET balance = 20000 WHERE id = $1", walletID)

	invariants := f.invariants(t)

	require.Contains(t, invariants, "I3")
	require.Contains(t, invariants, "I6")
}

// The check constraint forbids this state, so the test drops it in its throwaway database to prove the checker
// would still catch it.
func TestDetectsAvailableBalanceAboveBalance(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "ALTER TABLE accounts DROP CONSTRAINT ck_accounts_user_wallet_balances")
	f.exec(t, "UPDATE accounts SET available_balance = 10001 WHERE id = $1", walletID)

	require.Equal(t, []string{"I4"}, f.invariants(t))
}
