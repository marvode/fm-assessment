-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- name: GetTransactionByProviderReference :one
SELECT * FROM transactions
WHERE provider_reference_id = sqlc.arg(provider_reference_id)::text AND type = sqlc.arg(type);

-- name: GetTransactionByReference :one
SELECT * FROM transactions WHERE account_id = $1 AND reference_id = $2;

-- name: ListTransactions :many
SELECT * FROM transactions WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: InsertTransaction :exec
INSERT INTO transactions (id, account_id, type, status, amount, reference_id, provider_reference_id, request_hash,
                          created_at, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateTransaction :execrows
UPDATE transactions
SET status = sqlc.arg(status),
    provider_reference_id = sqlc.narg(provider_reference_id),
    completed_at = sqlc.narg(completed_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: StalePendingWithdrawals :many
SELECT id FROM transactions
WHERE type = 'Withdrawal' AND status = 'Pending' AND created_at <= $1
ORDER BY created_at;

-- name: FindWithdrawalID :one
SELECT id FROM transactions
WHERE type = 'Withdrawal'
  AND (provider_reference_id = sqlc.arg(provider_reference_id)::text OR id = sqlc.narg(keyed_id))
LIMIT 1;
