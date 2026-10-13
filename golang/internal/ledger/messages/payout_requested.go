// Package messages defines the events the wallet puts on its outbox. It is a leaf package so every slice that
// enqueues or consumes an event can import it.
package messages

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/store"
)

// PayoutRequestedType asks the payout worker to send a held withdrawal to the provider.
const PayoutRequestedType = "wallet.payout-requested"

type PayoutRequested struct {
	TransactionID uuid.UUID `json:"transactionId"`
}

func NewPayoutRequested(transactionID uuid.UUID, now time.Time) store.OutboxMessage {
	payload, err := json.Marshal(PayoutRequested{TransactionID: transactionID})
	if err != nil {
		panic(err) // a struct holding one UUID always marshals
	}
	return store.NewOutboxMessage(PayoutRequestedType, payload, now)
}
