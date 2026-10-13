-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (id, transaction_id, account_id, direction, amount, currency, balance_before,
                            balance_after, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);
