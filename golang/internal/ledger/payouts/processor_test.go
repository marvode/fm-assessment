package payouts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s        *store.Store
	clock    *common.FakeClock
	provider *providers.FakePaymentProvider
	userID   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start),
		provider: providers.NewFakePaymentProvider(), userID: uuid.New(),
	}
}

func (f *fixture) release() *withdrawals.ReleaseHandler {
	return withdrawals.NewReleaseHandler(f.s, f.clock, ledgertest.Logger())
}

func (f *fixture) processor() *payouts.Processor {
	return payouts.NewProcessor(f.s, f.provider, f.release(), ledgertest.Logger())
}

func (f *fixture) pendingWithdrawal(t *testing.T) uuid.UUID {
	return ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
}

func TestAnAcceptedPayoutStoresTheProviderReferenceAndStaysPending(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	var sent providers.PayoutInstruction
	f.provider.SetPayoutBehaviour(func(instruction providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		sent = instruction
		return providers.AcceptedPayout("trf_accepted"), nil
	})

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Initiated, outcome)
	require.Equal(t, providers.PayoutInstruction{
		ReferenceID: providers.PayoutKey(transactionID), Amount: 4_000,
		BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace",
	}, sent)
	transaction := ledgertest.Transaction(t, f.s, transactionID)
	require.Equal(t, "trf_accepted", *transaction.ProviderReferenceID)
	require.False(t, transaction.IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestARejectedPayoutReleasesTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.RejectedPayout("Invalid account"), nil
	})

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Rejected, outcome)
	require.True(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestATransientProviderErrorPropagatesAndKeepsTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	timeout := errors.New("provider timeout")
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.PayoutInitiation{}, timeout
	})

	_, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.ErrorIs(t, err, timeout)
	transaction := ledgertest.Transaction(t, f.s, transactionID)
	require.False(t, transaction.IsFinal())
	require.Nil(t, transaction.ProviderReferenceID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnAcceptanceWithoutAProviderReferenceKeepsTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.PayoutInitiation{Outcome: providers.Accepted}, nil
	})

	_, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.Error(t, err)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestARedeliveredMessageAfterAcceptanceDoesNotPayTwice(t *testing.T) {
	f := newFixture(t)
	message := messages.PayoutRequested{TransactionID: f.pendingWithdrawal(t)}
	_, err := f.processor().Process(ctx, message)
	require.NoError(t, err)

	redelivery, err := f.processor().Process(ctx, message)

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, redelivery)
	require.Equal(t, 1, f.provider.PayoutAttempts())
}

func TestAMessageForAFinalWithdrawalIsSkipped(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	_, err := f.release().Handle(ctx, transactionID, "test")
	require.NoError(t, err)

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, outcome)
	require.Zero(t, f.provider.PayoutAttempts())
}

func TestAMessageForAnUnknownTransactionIsSkipped(t *testing.T) {
	f := newFixture(t)

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: uuid.New()})

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, outcome)
}
