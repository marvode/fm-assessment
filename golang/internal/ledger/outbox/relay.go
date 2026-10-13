package outbox

import (
	"context"
	"log/slog"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
)

// Relay publishes unprocessed outbox messages oldest first and marks each processed after the broker accepts it.
// A crash between publish and save re-publishes the message, so delivery is at-least-once and consumers must be
// idempotent.
type Relay struct {
	store     *store.Store
	publisher Publisher
	clock     common.Clock
	batchSize int
	log       *slog.Logger
}

func NewRelay(s *store.Store, publisher Publisher, clock common.Clock, batchSize int, log *slog.Logger) *Relay {
	return &Relay{store: s, publisher: publisher, clock: clock, batchSize: batchSize, log: log}
}

func (r *Relay) RelayPending(ctx context.Context) (int, error) {
	batch, err := r.store.PendingOutbox(ctx, r.batchSize)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, message := range batch {
		err := r.publisher.Publish(ctx, message.Type, message.Payload, message.ID)
		if err != nil && ctx.Err() != nil {
			return published, ctx.Err()
		}
		if err != nil {
			r.log.Warn("publishing outbox message failed", "message_id", message.ID, "attempts", message.Attempts+1, "error", err)
			if err := r.store.RecordOutboxFailure(ctx, message.ID, err.Error()); err != nil {
				return published, err
			}
			continue
		}
		if err := r.store.MarkOutboxProcessed(ctx, message.ID, r.clock.Now()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
