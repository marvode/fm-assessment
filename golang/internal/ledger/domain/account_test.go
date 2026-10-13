package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
)

var now = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func funded(t *testing.T, amount int64) *domain.Account {
	t.Helper()
	account := domain.OpenUserWallet(uuid.New(), now)
	account.Credit(uuid.New(), amount, now)
	return account
}

func TestOpenUserWalletStartsEmptyInNGN(t *testing.T) {
	userID := uuid.New()

	account := domain.OpenUserWallet(userID, now)

	require.Equal(t, userID, *account.UserID)
	require.Equal(t, domain.AccountUser, account.Type)
	require.Equal(t, "NGN", account.Currency)
	require.Zero(t, account.Balance)
	require.Zero(t, account.AvailableBalance)
	require.Equal(t, now, account.CreatedAt)
}

func TestCreditRaisesBothBalancesAndRecordsTheRunningBalance(t *testing.T) {
	account := domain.OpenUserWallet(uuid.New(), now)
	transactionID := uuid.New()

	entry := account.Credit(transactionID, 500, now)

	require.EqualValues(t, 500, account.Balance)
	require.EqualValues(t, 500, account.AvailableBalance)
	require.Equal(t, domain.Credit, entry.Direction)
	require.Equal(t, transactionID, entry.TransactionID)
	require.Equal(t, account.ID, entry.AccountID)
	require.EqualValues(t, 500, entry.Amount)
	require.Equal(t, "NGN", entry.Currency)
	require.Zero(t, entry.BalanceBefore)
	require.EqualValues(t, 500, entry.BalanceAfter)
}

func TestPlaceHoldReducesOnlyTheAvailableBalance(t *testing.T) {
	account := funded(t, 500)

	_, err := account.PlaceHold(200)

	require.NoError(t, err)
	require.EqualValues(t, 300, account.AvailableBalance)
	require.EqualValues(t, 500, account.Balance)
}

func TestPlaceHoldForTheEntireAvailableBalanceSucceeds(t *testing.T) {
	account := funded(t, 500)

	_, err := account.PlaceHold(500)

	require.NoError(t, err)
	require.Zero(t, account.AvailableBalance)
}

func TestPlaceHoldBeyondTheAvailableBalanceFailsAndChangesNothing(t *testing.T) {
	account := funded(t, 100)
	versionBefore := account.Version

	_, err := account.PlaceHold(101)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 100, account.AvailableBalance)
	require.Equal(t, versionBefore, account.Version)
}

func TestPlaceHoldRejectsNonPositiveAmounts(t *testing.T) {
	for _, amount := range []int64{0, -1} {
		_, err := funded(t, 100).PlaceHold(amount)
		require.Equal(t, "Money.InvalidAmount", common.CodeOf(err), "amount %d", amount)
	}
}

func TestReleaseHoldRestoresExactlyTheHeldAmount(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(200)
	require.NoError(t, err)

	_, err = account.ReleaseHold(200)

	require.NoError(t, err)
	require.EqualValues(t, 500, account.AvailableBalance)
	require.EqualValues(t, 500, account.Balance)
}

func TestReleaseHoldWithoutAMatchingHoldFails(t *testing.T) {
	account := funded(t, 500)

	_, err := account.ReleaseHold(1)

	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(err))
	require.EqualValues(t, 500, account.AvailableBalance)
}

func TestDebitFromAHoldLowersTheBalanceWithoutLoweringAvailableAgain(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(200)
	require.NoError(t, err)

	entry := account.Debit(uuid.New(), 200, true, now)

	require.EqualValues(t, 300, account.Balance)
	require.EqualValues(t, 300, account.AvailableBalance)
	require.Equal(t, domain.Debit, entry.Direction)
	require.EqualValues(t, 500, entry.BalanceBefore)
	require.EqualValues(t, 300, entry.BalanceAfter)
}

func TestCanDebitWithoutAHoldRequiresAvailableFunds(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(400)
	require.NoError(t, err)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(account.CanDebit(200, false)))
}

func TestCanDebitFromAHoldRequiresAMatchingHold(t *testing.T) {
	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(funded(t, 500).CanDebit(200, true)))
}

func TestASystemAccountCanBeDebitedBelowZero(t *testing.T) {
	settlement := domain.OpenSystem(domain.ProviderSettlementID, domain.ProviderSettlementName, now)

	require.NoError(t, settlement.CanDebit(1_000, false))
	settlement.Debit(uuid.New(), 1_000, false, now)

	require.EqualValues(t, -1_000, settlement.Balance)
	require.Equal(t, domain.AccountSystem, settlement.Type)
	require.Nil(t, settlement.UserID)
}

func TestEveryChangeBumpsTheVersion(t *testing.T) {
	account := funded(t, 500)
	versionBefore := account.Version

	_, err := account.PlaceHold(100)

	require.NoError(t, err)
	require.Equal(t, versionBefore+1, account.Version)
}
