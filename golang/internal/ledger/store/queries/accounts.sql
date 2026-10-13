-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: GetWalletByUser :one
SELECT * FROM accounts WHERE user_id = sqlc.arg(user_id)::uuid AND type = 'User';

-- name: InsertAccount :exec
INSERT INTO accounts (id, user_id, type, name, currency, balance, available_balance, version, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdateAccount :execrows
UPDATE accounts
SET balance = sqlc.arg(balance), available_balance = sqlc.arg(available_balance), version = sqlc.arg(version)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);
