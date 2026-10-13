package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/store/db"
)

const maxOutboxErrorLength = 2_000

// OutboxMessage is an event saved in the same database transaction as the ledger change it describes, then relayed
// to the queue.
type OutboxMessage struct {
	ID          uuid.UUID
	Type        string
	Payload     []byte
	OccurredAt  time.Time
	ProcessedAt *time.Time
	Attempts    int32
	LastError   *string
}

func NewOutboxMessage(messageType string, payload []byte, now time.Time) OutboxMessage {
	return OutboxMessage{ID: uuid.Must(uuid.NewV7()), Type: messageType, Payload: payload, OccurredAt: now}
}

// PendingOutbox returns up to limit unprocessed messages, oldest first.
func (s *Store) PendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error) {
	rows, err := s.queries.PendingOutboxMessages(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	messages := make([]OutboxMessage, len(rows))
	for i, row := range rows {
		messages[i] = toOutboxMessage(row)
	}
	return messages, nil
}

func (s *Store) MarkOutboxProcessed(ctx context.Context, id uuid.UUID, now time.Time) error {
	return s.queries.MarkOutboxProcessed(ctx, db.MarkOutboxProcessedParams{ProcessedAt: now, ID: id})
}

func (s *Store) RecordOutboxFailure(ctx context.Context, id uuid.UUID, message string) error {
	if runes := []rune(message); len(runes) > maxOutboxErrorLength {
		message = string(runes[:maxOutboxErrorLength])
	}
	return s.queries.RecordOutboxFailure(ctx, db.RecordOutboxFailureParams{LastError: message, ID: id})
}
