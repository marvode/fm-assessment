# AGENTS.md: Go wallet

Go port of `dotnet/`, behaviour-compatible with it: same HTTP contract, ledger rules, outbox → RabbitMQ → payout
semantics. Design: `docs/superpowers/specs/2026-10-13-golang-port-design.md`.

## Commands (run from `golang/`)
- **Test:** `go test ./...`. Needs Docker: Postgres/RabbitMQ come from testcontainers. To reuse running servers set
  `WALLET_TEST_DATABASE_URL` / `WALLET_TEST_RABBITMQ_URL` and run `go test -p 1 ./...`.
- **Lint:** `make lint` (gofmt, go vet, golangci-lint if installed)
- **Regenerate queries:** `make generate` (sqlc in Docker). Never edit `internal/ledger/store/db/` by hand.
- **End-to-end:** `make e2e` (needs docker, go, curl, jq, uuidgen)
- **Run locally:** `docker compose up --build` (full stack), or `make up && make run` (APP_ENV=development migrates and enables `/dev/provider`)

## Layout
Feature slices live under `internal/ledger/` (wallets, topups, withdrawals, webhooks, outbox, payouts,
reconciliation), on top of `domain` (pure model) and `store` (Postgres). `internal/api` is HTTP and `internal/app`
wires everything.

## Conventions
- **Errors:** expected failures are `*common.Error` values from `domain`; their codes map to HTTP statuses in
  `api.StatusFor`. Never compare error strings; use `errors.Is` or `common.CodeOf`.
- **Writes:** every write goes through `store.Run` or `store.WithRetry`. Load entities via the `*store.Tx`, mutate
  them with domain methods, and stage inserts with `Add*`. Never write SQL that bypasses the version/status checks.
- **Migrations:** schema changes are new goose files in `internal/ledger/store/migrations/`; never edit an applied
  one.
- **Logging:** use `log/slog` with structured attrs. The messages grepped by `scripts/e2e.sh` (`payout sweep`,
  `ledger invariant violated`, `provider lost accepted payout; resubmitting it`, `unhandled error`) are part of the
  contract.
- **Tests:** tests are external `_test` packages using testify `require`; shared helpers live in
  `internal/ledger/ledgertest`. Every feature or bug fix comes with a test.
- **Payout retries:** RabbitMQ 4 quorum queues ignore AMQP 0.9.1 nack-requeue for `x-delivery-limit`, so
  `payouts.Consumer` counts failures in the `x-wallet-attempts` header and dead-letters on the 5th.
