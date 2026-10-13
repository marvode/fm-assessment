-- name: InsertOutboxMessage :exec
INSERT INTO outbox_messages (id, type, payload, occurred_at) VALUES ($1, $2, $3, $4);

-- name: PendingOutboxMessages :many
SELECT * FROM outbox_messages WHERE processed_at IS NULL ORDER BY occurred_at, id LIMIT $1;

-- name: MarkOutboxProcessed :exec
UPDATE outbox_messages
SET processed_at = sqlc.arg(processed_at)::timestamptz, attempts = attempts + 1
WHERE id = sqlc.arg(id);

-- name: RecordOutboxFailure :exec
UPDATE outbox_messages
SET last_error = sqlc.arg(last_error)::text, attempts = attempts + 1
WHERE id = sqlc.arg(id);
