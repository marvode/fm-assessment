-- +goose Up
CREATE TABLE accounts (
    id                uuid PRIMARY KEY,
    user_id           uuid,
    type              varchar(16) NOT NULL,
    name              varchar(64),
    currency          varchar(3)  NOT NULL,
    balance           bigint      NOT NULL,
    available_balance bigint      NOT NULL,
    version           bigint      NOT NULL,
    created_at        timestamptz NOT NULL,
    CONSTRAINT ck_accounts_user_wallet_balances
        CHECK (type <> 'User' OR (balance >= 0 AND available_balance >= 0 AND available_balance <= balance)),
    CONSTRAINT ck_accounts_currency CHECK (currency = 'NGN')
);
CREATE UNIQUE INDEX ix_accounts_user_id_currency ON accounts (user_id, currency) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX ix_accounts_name ON accounts (name) WHERE name IS NOT NULL;

CREATE TABLE transactions (
    id                    uuid PRIMARY KEY,
    account_id            uuid         NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    type                  varchar(16)  NOT NULL,
    status                varchar(16)  NOT NULL,
    amount                bigint       NOT NULL,
    reference_id          varchar(128) NOT NULL,
    provider_reference_id varchar(128),
    request_hash          varchar(64)  NOT NULL,
    created_at            timestamptz  NOT NULL,
    completed_at          timestamptz,
    CONSTRAINT ck_transactions_amount CHECK (amount > 0)
);
-- Client referenceIds are idempotency keys scoped to one wallet.
CREATE UNIQUE INDEX ix_transactions_account_id_reference_id ON transactions (account_id, reference_id);
CREATE UNIQUE INDEX ix_transactions_provider_reference_id ON transactions (provider_reference_id)
    WHERE provider_reference_id IS NOT NULL;
CREATE INDEX ix_transactions_account_id_created_at ON transactions (account_id, created_at);
CREATE INDEX ix_transactions_type_status_created_at ON transactions (type, status, created_at);

CREATE TABLE ledger_entries (
    id             uuid PRIMARY KEY,
    transaction_id uuid        NOT NULL REFERENCES transactions (id) ON DELETE RESTRICT,
    account_id     uuid        NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    direction      varchar(8)  NOT NULL,
    amount         bigint      NOT NULL,
    currency       varchar(3)  NOT NULL,
    balance_before bigint      NOT NULL,
    balance_after  bigint      NOT NULL,
    created_at     timestamptz NOT NULL,
    CONSTRAINT ck_ledger_entries_amount CHECK (amount > 0)
);
CREATE INDEX ix_ledger_entries_transaction_id ON ledger_entries (transaction_id);
CREATE INDEX ix_ledger_entries_account_id_created_at ON ledger_entries (account_id, created_at);

CREATE TABLE withdrawals (
    transaction_id uuid PRIMARY KEY REFERENCES transactions (id) ON DELETE RESTRICT,
    bank_code      varchar(16)  NOT NULL,
    account_number varchar(10)  NOT NULL,
    account_name   varchar(128) NOT NULL
);

CREATE TABLE outbox_messages (
    id           uuid PRIMARY KEY,
    type         varchar(128)  NOT NULL,
    payload      jsonb         NOT NULL,
    occurred_at  timestamptz   NOT NULL,
    processed_at timestamptz,
    attempts     integer       NOT NULL DEFAULT 0,
    last_error   varchar(2000)
);
CREATE INDEX ix_outbox_messages_unprocessed ON outbox_messages (occurred_at, id) WHERE processed_at IS NULL;

INSERT INTO accounts (id, user_id, type, name, currency, balance, available_balance, version, created_at)
VALUES ('00000000-0000-0000-0000-000000000001', NULL, 'System', 'ProviderSettlement', 'NGN', 0, 0, 0,
        '2026-01-01T00:00:00Z');

-- +goose Down
DROP TABLE outbox_messages;
DROP TABLE withdrawals;
DROP TABLE ledger_entries;
DROP TABLE transactions;
DROP TABLE accounts;
