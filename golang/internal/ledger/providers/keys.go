package providers

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// The idempotency keys we send the provider. Client referenceIds are only unique per wallet, so they are never sent
// raw: a charge key adds the wallet id, a payout key is the withdrawal's transaction id. Both are stable across retries.

func ChargeKey(accountID uuid.UUID, referenceID string) string {
	return compact(accountID) + "-" + referenceID
}

func PayoutKey(transactionID uuid.UUID) string { return compact(transactionID) }

// ParsePayoutKey reads a payout key back into the transaction id. Only the 32-hex form is a payout key.
func ParsePayoutKey(key string) (uuid.UUID, bool) {
	if len(key) != 32 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(key)
	return id, err == nil
}

func compact(id uuid.UUID) string { return hex.EncodeToString(id[:]) }
