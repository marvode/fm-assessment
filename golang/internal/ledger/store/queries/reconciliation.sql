-- name: UnbalancedTransactions :many
SELECT transaction_id,
       count(*)::bigint                                                AS entry_count,
       COALESCE(sum(amount) FILTER (WHERE direction = 'Debit'), 0)::bigint  AS debits,
       COALESCE(sum(amount) FILTER (WHERE direction = 'Credit'), 0)::bigint AS credits
FROM ledger_entries
GROUP BY transaction_id
HAVING count(*) <> 2
    OR count(*) FILTER (WHERE direction = 'Debit') <> 1
    OR COALESCE(sum(amount) FILTER (WHERE direction = 'Debit'), 0)
       <> COALESCE(sum(amount) FILTER (WHERE direction = 'Credit'), 0);

-- name: DriftedAccounts :many
SELECT a.id,
       a.balance,
       COALESCE(sum(CASE WHEN e.direction = 'Credit' THEN e.amount ELSE -e.amount END), 0)::bigint AS net
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.id, a.balance
HAVING a.balance <> COALESCE(sum(CASE WHEN e.direction = 'Credit' THEN e.amount ELSE -e.amount END), 0);

-- name: InvalidWallets :many
SELECT id, balance, available_balance FROM accounts
WHERE type = 'User' AND (balance < 0 OR available_balance < 0 OR available_balance > balance);

-- name: TotalBalance :one
SELECT COALESCE(sum(balance), 0)::bigint AS total FROM accounts;
