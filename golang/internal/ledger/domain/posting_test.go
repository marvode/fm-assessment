package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
)

func settlement() *domain.Account {
	return domain.OpenSystem(domain.ProviderSettlementID, domain.ProviderSettlementName, now)
}

func wallet() *domain.Account { return domain.OpenUserWallet(uuid.New(), now) }

func fund(t *testing.T, w, s *domain.Account, amount int64) []domain.Entry {
	t.Helper()
	topUp := domain.StartTopUp(w.ID, amount, "ref-"+uuid.NewString(), "hash", "chg_"+uuid.NewString(), now)
	entries, err := domain.Post(topUp, s, w, false, now)
	require.NoError(t, err)
	return entries
}

func single(t *testing.T, entries []domain.Entry, direction domain.Direction) domain.Entry {
	t.Helper()
	var found []domain.Entry
	for _, e := range entries {
		if e.Direction == direction {
			found = append(found, e)
		}
	}
	require.Len(t, found, 1)
	return found[0]
}

func TestATopUpPostingIsBalancedAndMovesMoneyFromSettlementToTheWallet(t *testing.T) {
	s, w := settlement(), wallet()
	topUp := domain.StartTopUp(w.ID, 500, "ref-1", "hash", "chg_1", now)

	entries, err := domain.Post(topUp, s, w, false, now)

	require.NoError(t, err)
	require.Len(t, entries, 2)
	debit, credit := single(t, entries, domain.Debit), single(t, entries, domain.Credit)
	require.Equal(t, s.ID, debit.AccountID)
	require.Equal(t, w.ID, credit.AccountID)
	for _, e := range entries {
		require.EqualValues(t, 500, e.Amount)
		require.Equal(t, topUp.ID, e.TransactionID)
		require.Equal(t, "NGN", e.Currency)
	}
	require.EqualValues(t, 500, w.Balance)
	require.EqualValues(t, 500, w.AvailableBalance)
	require.EqualValues(t, -500, s.Balance)
	require.Zero(t, w.Balance+s.Balance)
}

func TestConsecutivePostingsChainRunningBalances(t *testing.T) {
	s, w := settlement(), wallet()

	first := fund(t, w, s, 500)
	second := fund(t, w, s, 300)

	require.Equal(t, single(t, first, domain.Credit).BalanceAfter, single(t, second, domain.Credit).BalanceBefore)
	require.EqualValues(t, 800, single(t, second, domain.Credit).BalanceAfter)
	require.EqualValues(t, -500, single(t, second, domain.Debit).BalanceBefore)
	require.EqualValues(t, -800, single(t, second, domain.Debit).BalanceAfter)
}

func TestSettlingAWithdrawalConsumesTheHold(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 1_000)
	_, err := w.PlaceHold(400)
	require.NoError(t, err)
	withdrawal := domain.StartWithdrawal(w.ID, 400, "wd-1", "hash", now)

	_, err = domain.Post(withdrawal, w, s, true, now)

	require.NoError(t, err)
	require.EqualValues(t, 600, w.Balance)
	require.EqualValues(t, 600, w.AvailableBalance)
	require.EqualValues(t, -600, s.Balance)
}

func TestADebitWithoutFundsFailsAndLeavesBothAccountsUntouched(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 100)
	walletVersion, settlementVersion := w.Version, s.Version
	withdrawal := domain.StartWithdrawal(w.ID, 150, "wd-1", "hash", now)

	_, err := domain.Post(withdrawal, w, s, false, now)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 100, w.Balance)
	require.EqualValues(t, 100, w.AvailableBalance)
	require.EqualValues(t, -100, s.Balance)
	require.Equal(t, walletVersion, w.Version)
	require.Equal(t, settlementVersion, s.Version)
}

func TestSettlingWithoutAHoldFails(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 1_000)
	withdrawal := domain.StartWithdrawal(w.ID, 400, "wd-1", "hash", now)

	_, err := domain.Post(withdrawal, w, s, true, now)

	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(err))
}

func TestPostingToTheSameAccountIsRejected(t *testing.T) {
	w := wallet()
	topUp := domain.StartTopUp(w.ID, 10, "ref-1", "hash", "chg_1", now)

	_, err := domain.Post(topUp, w, w, false, now)

	require.Equal(t, "Ledger.SameAccount", common.CodeOf(err))
}
