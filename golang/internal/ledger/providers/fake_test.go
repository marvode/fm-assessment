package providers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/providers"
)

var ctx = context.Background()

func instruction(referenceID string) providers.PayoutInstruction {
	return providers.PayoutInstruction{ReferenceID: referenceID, Amount: 4_000, BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace"}
}

func TestAChargeIsIdempotentPerReference(t *testing.T) {
	p := providers.NewFakePaymentProvider()

	first, err := p.InitiateCharge(ctx, "topup-1", 5_000)
	require.NoError(t, err)
	second, err := p.InitiateCharge(ctx, "topup-1", 5_000)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(first.ProviderReferenceID, "chg_"))
	require.Equal(t, first.ProviderReferenceID, second.ProviderReferenceID)
	record, ok := p.FindByProviderReference(first.ProviderReferenceID)
	require.True(t, ok)
	require.False(t, record.IsPayout)
	require.EqualValues(t, 5_000, record.Amount)
	require.Equal(t, "topup-1", record.ReferenceID)
}

func TestAPayoutIsAcceptedByDefaultAndIdempotentPerReference(t *testing.T) {
	p := providers.NewFakePaymentProvider()

	first, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)
	second, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)

	require.Equal(t, providers.Accepted, first.Outcome)
	require.True(t, strings.HasPrefix(*first.ProviderReferenceID, "trf_"))
	require.Equal(t, *first.ProviderReferenceID, *second.ProviderReferenceID)
	require.Equal(t, 2, p.PayoutAttempts())
	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusPending, status)
}

func TestAScriptedRejectionIsReturnedAndNotRecorded(t *testing.T) {
	p := providers.NewFakePaymentProvider()
	p.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.RejectedPayout("Invalid account"), nil
	})

	result, err := p.InitiatePayout(ctx, instruction("wd-1"))

	require.NoError(t, err)
	require.Equal(t, providers.Rejected, result.Outcome)
	require.Equal(t, "Invalid account", *result.FailureReason)
	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusNotFound, status)
}

func TestSetPayoutStatusChangesTheReportedStatus(t *testing.T) {
	p := providers.NewFakePaymentProvider()
	_, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)

	p.SetPayoutStatus("wd-1", providers.StatusSucceeded)

	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusSucceeded, status)
}

func TestAnUnknownPayoutIsNotFound(t *testing.T) {
	status, err := providers.NewFakePaymentProvider().GetPayoutStatus(ctx, "missing")

	require.NoError(t, err)
	require.Equal(t, providers.StatusNotFound, status)
}
