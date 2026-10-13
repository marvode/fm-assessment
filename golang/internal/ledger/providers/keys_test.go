package providers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/providers"
)

func TestPayoutKeysRoundTripOnlyInTheCompactForm(t *testing.T) {
	id := uuid.New()
	key := providers.PayoutKey(id)

	parsed, ok := providers.ParsePayoutKey(key)

	require.Len(t, key, 32)
	require.True(t, ok)
	require.Equal(t, id, parsed)
	_, ok = providers.ParsePayoutKey(id.String())
	require.False(t, ok, "hyphenated ids are not payout keys")
	_, ok = providers.ParsePayoutKey("not-a-guid")
	require.False(t, ok)
}

func TestChargeKeysAreScopedToTheWallet(t *testing.T) {
	first, second := uuid.New(), uuid.New()

	require.NotEqual(t, providers.ChargeKey(first, "ref-1"), providers.ChargeKey(second, "ref-1"))
	require.Equal(t, providers.PayoutKey(first)+"-ref-1", providers.ChargeKey(first, "ref-1"))
}
