package withdrawals_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s      *store.Store
	clock  *common.FakeClock
	userID uuid.UUID
}

func newFixture(t *testing.T, balance int64) *fixture {
	f := &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
	ledgertest.SeedWallet(t, f.s, f.userID, balance, ledgertest.Start)
	return f
}

func (f *fixture) request(s *store.Store) *withdrawals.RequestHandler {
	return withdrawals.NewRequestHandler(s, f.clock)
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return ledgertest.Count(t, f.s, sql, args...)
}

func TestPlacesAHoldAndRecordsTheDetailsAndPayoutEventTogether(t *testing.T) {
	f := newFixture(t, 10_000)

	result, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, "Withdrawal", result.Transaction.Type)
	require.Equal(t, "Pending", result.Transaction.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	details, err := f.s.Withdrawal(ctx, result.Transaction.ID)
	require.NoError(t, err)
	require.Equal(t, "0123456789", details.AccountNumber)
	require.Equal(t, "058", details.BankCode)
	pending, err := f.s.PendingOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, messages.PayoutRequestedType, pending[0].Type)
	require.Nil(t, pending[0].ProcessedAt)
	var payload messages.PayoutRequested
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, result.Transaction.ID, payload.TransactionID)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.Transaction.ID))
}

func TestWithdrawingTheEntireAvailableBalanceSucceeds(t *testing.T) {
	f := newFixture(t, 10_000)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 10_000, "wd-all"))
	require.NoError(t, err)
	_, err = f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 1, "wd-one-more"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestInsufficientFundsChangesNothing(t *testing.T) {
	f := newFixture(t, 10_000)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 20_000, "wd-1"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
	require.Zero(t, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestAUserWithoutAWalletGetsNotFound(t *testing.T) {
	f := newFixture(t, 0)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(uuid.New(), 100, "wd-1"))

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestInvalidRequestsAreRejected(t *testing.T) {
	cases := []struct {
		amount                  int64
		accountNumber, bankCode string
		code                    string
	}{
		{0, "0123456789", "058", "Money.InvalidAmount"},
		{100, "12345", "058", "Validation.AccountNumber"},
		{100, "01234567AB", "058", "Validation.AccountNumber"},
		{100, "0123456789", "", "Validation.BankCode"},
	}
	f := newFixture(t, 10_000)
	for _, c := range cases {
		command := ledgertest.WithdrawalCommand(f.userID, c.amount, "wd-1")
		command.AccountNumber, command.BankCode = c.accountNumber, c.bankCode

		_, err := f.request(f.s).Handle(ctx, command)

		require.Equal(t, c.code, common.CodeOf(err), "%+v", c)
	}
}

func TestOversizedFieldsAreRejected(t *testing.T) {
	f := newFixture(t, 10_000)
	for _, field := range []string{"ReferenceId", "BankCode", "AccountName"} {
		command := ledgertest.WithdrawalCommand(f.userID, 100, "wd-1")
		switch field {
		case "ReferenceId":
			command.ReferenceID = strings.Repeat("r", 129)
		case "BankCode":
			command.BankCode = strings.Repeat("1", 17)
		default:
			command.AccountName = strings.Repeat("a", 129)
		}

		_, err := f.request(f.s).Handle(ctx, command)

		require.Equal(t, "Validation."+field, common.CodeOf(err))
	}
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnIdenticalRetryReturnsTheOriginalWithoutASecondHold(t *testing.T) {
	f := newFixture(t, 10_000)

	first, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)
	retry, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)

	require.True(t, retry.Replayed)
	require.Equal(t, first.Transaction.ID, retry.Transaction.ID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestAReusedReferenceWithADifferentAmountIsRejected(t *testing.T) {
	f := newFixture(t, 10_000)
	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)

	_, err = f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 5_000, "wd-1"))

	require.Equal(t, "Transaction.DuplicateReference", common.CodeOf(err))
}

func TestConcurrentWithdrawalsCannotSpendTheSameMoney(t *testing.T) {
	f := newFixture(t, 10_000)
	competitor := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 5_000, "wd-competing"))
		return err
	})

	_, err := f.request(competitor).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 8_000, "wd-mine"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestConcurrentIdenticalRequestsCreateOneWithdrawal(t *testing.T) {
	f := newFixture(t, 10_000)
	twin := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
		return err
	})

	result, err := f.request(twin).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))

	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
}

func TestWithdrawalReferenceIDsAreScopedToEachUsersWallet(t *testing.T) {
	f := newFixture(t, 10_000)
	otherUserID := uuid.New()
	ledgertest.SeedWallet(t, f.s, otherUserID, 10_000, ledgertest.Start)

	mine, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)
	theirs, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(otherUserID, 4_000, "wd-1"))
	require.NoError(t, err)

	require.False(t, theirs.Replayed)
	require.NotEqual(t, mine.Transaction.ID, theirs.Transaction.ID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, otherUserID).AvailableBalance)
}
