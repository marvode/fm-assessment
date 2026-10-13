package reconciliation_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/reconciliation"
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

func (f *fixture) sweeper(provider providers.PaymentProvider) *reconciliation.Sweeper {
	return reconciliation.NewSweeper(f.s, provider, withdrawals.NewSettleHandler(f.s, f.clock), f.release(),
		f.clock, 15*time.Minute, ledgertest.Logger())
}

func (f *fixture) process(t *testing.T, provider providers.PaymentProvider, transactionID uuid.UUID) payouts.Outcome {
	t.Helper()
	outcome, err := payouts.NewProcessor(f.s, provider, f.release(), ledgertest.Logger()).
		Process(ctx, messages.PayoutRequested{TransactionID: transactionID})
	require.NoError(t, err)
	return outcome
}

// submittedWithdrawal is a Pending withdrawal the provider has accepted, so the provider knows the payout.
func (f *fixture) submittedWithdrawal(t *testing.T) uuid.UUID {
	t.Helper()
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	f.process(t, f.provider, transactionID)
	return transactionID
}

func (f *fixture) sweep(t *testing.T, provider providers.PaymentProvider) reconciliation.Summary {
	t.Helper()
	summary, err := f.sweeper(provider).Sweep(ctx)
	require.NoError(t, err)
	return summary
}

func TestSettlesAStaleWithdrawalTheProviderReportsSucceeded(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusSucceeded)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Settled)
	require.True(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestReleasesAStaleWithdrawalTheProviderReportsFailed(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusFailed)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Released)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestIgnoresWithdrawalsYoungerThanTheThreshold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusSucceeded)
	f.clock.Advance(10 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, reconciliation.Summary{}, summary)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
}

func TestKeepsAPayoutTheProviderHasNeverSeenOnHoldAndRequeuesIt(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Redriven)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	// the original request plus the sweeper's re-drive
	require.EqualValues(t, 2, ledgertest.Count(t, f.s,
		"SELECT count(*) FROM outbox_messages WHERE type = $1 AND payload->>'transactionId' = $2",
		messages.PayoutRequestedType, transactionID.String()))
}

func TestResubmitsAnAcceptedPayoutTheProviderHasLostInsteadOfRequeueingForever(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	restartedProvider := providers.NewFakePaymentProvider()
	f.clock.Advance(16 * time.Minute)

	require.Equal(t, 1, f.sweep(t, restartedProvider).Redriven)
	require.Nil(t, ledgertest.Transaction(t, f.s, transactionID).ProviderReferenceID, "the provider no longer knows that reference")

	require.Equal(t, payouts.Initiated, f.process(t, restartedProvider, transactionID))
	require.Equal(t, 1, restartedProvider.PayoutAttempts())

	require.Equal(t, reconciliation.Summary{StillPending: 1}, f.sweep(t, restartedProvider))
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestLeavesPayoutsTheProviderStillHasPending(t *testing.T) {
	f := newFixture(t)
	f.submittedWithdrawal(t)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.StillPending)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}
