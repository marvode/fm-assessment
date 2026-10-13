// Package outbox relays events saved with ledger changes to RabbitMQ.
package outbox

import (
	"context"

	"github.com/google/uuid"
)

// Publisher sends one outbox message. It returns only after the broker has the message.
type Publisher interface {
	Publish(ctx context.Context, messageType string, payload []byte, messageID uuid.UUID) error
}
