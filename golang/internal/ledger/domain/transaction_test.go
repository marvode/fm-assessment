package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
)

func TestANewWithdrawalIsPendingWithoutAProviderReference(t *testing.T) {
	accountID := uuid.New()

	withdrawal := domain.StartWithdrawal(accountID, 400, "wd-1", "hash", now)

	require.Equal(t, domain.Withdrawal, withdrawal.Type)
	require.Equal(t, domain.Pending, withdrawal.Status)
	require.False(t, withdrawal.IsFinal())
	require.Equal(t, accountID, withdrawal.AccountID)
	require.EqualValues(t, 400, withdrawal.Amount)
	require.Equal(t, "wd-1", withdrawal.ReferenceID)
	require.Equal(t, "hash", withdrawal.RequestHash)
	require.Nil(t, withdrawal.ProviderReferenceID)
	require.Equal(t, now, withdrawal.CreatedAt)
	require.Nil(t, withdrawal.CompletedAt)
}

func TestANewTopUpCarriesTheProviderReference(t *testing.T) {
	topUp := domain.StartTopUp(uuid.New(), 500, "ref-1", "hash", "chg_1", now)

	require.Equal(t, "chg_1", *topUp.ProviderReferenceID)
}

func TestATransactionCanBeFinalisedOnlyOnce(t *testing.T) {
	withdrawal := domain.StartWithdrawal(uuid.New(), 400, "wd-1", "hash", now)
	later := now.Add(time.Minute)

	withdrawal.MarkSucceeded(later)

	require.Equal(t, domain.Succeeded, withdrawal.Status)
	require.True(t, withdrawal.IsFinal())
	require.Equal(t, later, *withdrawal.CompletedAt)
	require.Panics(t, func() { withdrawal.MarkFailed(later) })
}
