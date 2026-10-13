-- name: InsertWithdrawal :exec
INSERT INTO withdrawals (transaction_id, bank_code, account_number, account_name) VALUES ($1, $2, $3, $4);

-- name: GetWithdrawal :one
SELECT * FROM withdrawals WHERE transaction_id = $1;
