package webhooks_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s      *store.Store
	clock  *common.FakeClock
	userID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
}

func (f *fixture) handler() *webhooks.Handler {
	log := ledgertest.Logger()
	return webhooks.NewHandler(f.s,
		topups.NewCompleteHandler(f.s, f.clock, log),
		withdrawals.NewSettleHandler(f.s, f.clock),
		withdrawals.NewReleaseHandler(f.s, f.clock, log))
}

func (f *fixture) startTopUp(t *testing.T, amount int64) string {
	t.Helper()
	started, err := topups.NewInitiateHandler(f.s, providers.NewFakePaymentProvider(), f.clock).
		Handle(ctx, topups.InitiateCommand{UserID: f.userID, ReferenceID: "topup-1", Amount: amount})
	require.NoError(t, err)
	return *started.Transaction.ProviderReferenceID
}

func webhook(event, providerReferenceID string, amount int64) webhooks.Webhook {
	return webhooks.Webhook{Event: event, ProviderReferenceID: providerReferenceID, Amount: amount}
}

func TestChargeSuccessCreditsTheWallet(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.handler().Handle(ctx, webhook(webhooks.ChargeSuccess, providerReferenceID, 5_000))

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestADuplicateChargeSuccessCreditsOnce(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	w := webhook(webhooks.ChargeSuccess, providerReferenceID, 5_000)

	_, err := f.handler().Handle(ctx, w)
	require.NoError(t, err)
	_, err = f.handler().Handle(ctx, w)
	require.NoError(t, err)

	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestChargeFailedFailsTheTopUp(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.handler().Handle(ctx, webhook(webhooks.ChargeFailed, providerReferenceID, 5_000))

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferSuccessSettlesTheWithdrawalFoundByProviderReference(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	ledgertest.AssignProviderReference(t, f.s, transactionID, "trf_abc")

	result, err := f.handler().Handle(ctx, webhook(webhooks.TransferSuccess, "trf_abc", 4_000))

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferSuccessBeforeTheProviderReferenceIsStoredMatchesOurReference(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	w := webhook(webhooks.TransferSuccess, "trf_not_yet_stored", 4_000)
	reference := providers.PayoutKey(transactionID)
	w.Reference = &reference

	result, err := f.handler().Handle(ctx, w)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferFailedReleasesTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	ledgertest.AssignProviderReference(t, f.s, transactionID, "trf_abc")

	result, err := f.handler().Handle(ctx, webhook(webhooks.TransferFailed, "trf_abc", 4_000))

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnUnknownEventIsRejected(t *testing.T) {
	f := newFixture(t)

	_, err := f.handler().Handle(ctx, webhook("refund.created", "x", 1))

	require.Equal(t, "Validation.Event", common.CodeOf(err))
}

func TestAnUnknownTransferReferenceReturnsNotFound(t *testing.T) {
	f := newFixture(t)
	w := webhook(webhooks.TransferSuccess, "trf_missing", 1)
	reference := "not-a-guid"
	w.Reference = &reference

	_, err := f.handler().Handle(ctx, w)

	require.Equal(t, "Transaction.NotFound", common.CodeOf(err))
}
