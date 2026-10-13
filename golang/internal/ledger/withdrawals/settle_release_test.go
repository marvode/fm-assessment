package withdrawals_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

func (f *fixture) settle(s *store.Store) *withdrawals.SettleHandler {
	return withdrawals.NewSettleHandler(s, f.clock)
}

func (f *fixture) release(s *store.Store) *withdrawals.ReleaseHandler {
	return withdrawals.NewReleaseHandler(s, f.clock, ledgertest.Logger())
}

func pendingWithdrawal(t *testing.T) (*fixture, uuid.UUID) {
	f := &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
	return f, ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
}

func TestSettleDebitsTheWalletFromTheHoldAndCreditsSettlement(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	result, err := f.settle(f.s).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.Balance)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
	require.EqualValues(t, -6_000, ledgertest.Settlement(t, f.s).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
	require.EqualValues(t, 1, f.count(t, `SELECT count(*) FROM ledger_entries
		WHERE transaction_id = $1 AND direction = 'Debit' AND account_id = $2 AND balance_after = 6000`, transactionID, wallet.ID))
	require.Zero(t, f.count(t, "SELECT COALESCE(sum(balance), 0)::bigint FROM accounts"))
}

func TestSettlingTwicePostsOnce(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	_, err := f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)
	_, err = f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)

	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}

func TestReleaseRestoresTheAvailableBalanceWithoutEntries(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	result, err := f.release(f.s).Handle(ctx, transactionID, "test")

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 10_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}

func TestReleaseAfterSettleChangesNothing(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	_, err := f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)

	result, err := f.release(f.s).Handle(ctx, transactionID, "late failure")

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.Balance)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
}

func TestSettleAfterReleaseChangesNothing(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	_, err := f.release(f.s).Handle(ctx, transactionID, "test")
	require.NoError(t, err)

	result, err := f.settle(f.s).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestSettlingOrReleasingAnUnknownWithdrawalReturnsNotFound(t *testing.T) {
	f, _ := pendingWithdrawal(t)

	_, settleErr := f.settle(f.s).Handle(ctx, uuid.New())
	_, releaseErr := f.release(f.s).Handle(ctx, uuid.New(), "test")

	require.Equal(t, "Transaction.NotFound", common.CodeOf(settleErr))
	require.Equal(t, "Transaction.NotFound", common.CodeOf(releaseErr))
}

func TestSettleAndReleaseRacingApplyExactlyOne(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	releaseFirst := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.release(f.s).Handle(ctx, transactionID, "webhook said failed")
		return err
	})

	result, err := f.settle(releaseFirst).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.EqualValues(t, 10_000, wallet.AvailableBalance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}
