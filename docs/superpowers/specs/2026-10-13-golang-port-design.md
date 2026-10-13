# Go port of the wallet — design

Date: 2026-10-13
Status: Draft, awaiting review

## Goal

Add a `golang/` folder next to `dotnet/` containing a Go implementation of the wallet that is
**behaviour-compatible** with the .NET one:

- the same HTTP contract (routes, status codes, JSON shapes, ProblemDetails error codes);
- the same ledger rules (double entry, holds, idempotency, optimistic concurrency);
- the same outbox → RabbitMQ → payout processor → webhook/sweeper semantics.

Go owns its own idiomatic Postgres schema (snake_case). The two implementations do not share a database.
Success means the ported test suite (every .NET test scenario) and `golang/scripts/e2e.sh` (the same checks as
`dotnet/scripts/e2e.sh`) both pass.

### Intentional differences from .NET

- `GET /health` and `GET /alive` are served in every environment, not only in Development.
- There is no `GET /` route (the .NET `HomeController` is not ported).
- Local infrastructure comes from `compose.yaml` instead of the Aspire AppHost. Redis and Kafka, which the
  AppHost declares but the API never uses, are left out.
- Tests run against real Postgres (testcontainers) instead of EF InMemory plus interceptors. Two invariant-checker
  tests corrupt the ledger differently because Postgres enforces what InMemory did not. The stray ledger entry
  references an existing transaction (foreign key), and the available>balance test drops the check constraint in its
  throwaway database.
- A top-up that loses the unique-index race twice returns `Concurrency.Exhausted` (409). In .NET the exception
  escapes as a 500.
- When the sweeper re-queues a payout and its save conflicts, it counts an error and moves on, instead of
  aborting the sweep.
- A body that cannot be bound (malformed JSON, missing field, wrong type) still returns 400 problem JSON, but its
  title is `Validation.<Field>` or `Validation.Body` instead of ASP.NET's "One or more validation errors
  occurred." Clients and tests only rely on the status code for these.

### Out of scope

- OpenAPI/Scalar documentation.
- OpenTelemetry. ServiceDefaults wires it up in .NET; it can be added later with `otelhttp` and `otelpgx`.
- Making the outbox relay safe for multiple instances (`FOR UPDATE SKIP LOCKED`). The .NET relay does not do
  this either.

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.25+, module `wallet` |
| HTTP | stdlib `net/http` with the Go 1.22+ `ServeMux` method/path patterns |
| Postgres driver | `github.com/jackc/pgx/v5` (`pgxpool`) |
| Queries | `sqlc` (pgx/v5 engine). The generated code is committed, so a build does not need `sqlc`. |
| Migrations | `github.com/pressly/goose/v3` with SQL files embedded via `embed.FS` |
| RabbitMQ | `github.com/rabbitmq/amqp091-go` |
| IDs | `github.com/google/uuid` (`uuid.NewV7`) |
| Config | `github.com/caarlos0/env/v11` (environment variables) |
| Logging | `log/slog` (JSON handler) |
| Concurrency/lifecycle | `golang.org/x/sync/errgroup`, `signal.NotifyContext` |
| Tests | `testing`, `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go` (postgres + rabbitmq modules) |

## Layout

```
golang/
  go.mod
  sqlc.yaml
  Makefile                    test, e2e, generate, run, lint
  compose.yaml                postgres:17, rabbitmq:4-management
  cmd/wallet-api/main.go      config → pgxpool → rabbit → (dev) migrate → HTTP server + workers under errgroup
  internal/
    common/                   Error{Code, Message}, Clock, SystemClock, FakeClock
    config/                   Config struct parsed from env
    rabbit/                   reconnecting connection wrapper
    ledger/
      domain/                 Account, Transaction, Entry, posting, WalletErrors, RequestHash, FieldLimits (no I/O)
      store/                  migrations/*.sql, queries/*.sql, sqlc output (store/db), WithRetry, unique-violation helper
      providers/              PaymentProvider interface, FakePaymentProvider, ProviderKeys
      wallets/                Create, Get, ListTransactions handlers, Idempotency, response DTOs
      topups/                 InitiateTopUp, CompleteTopUp (Succeed/Fail)
      withdrawals/            RequestWithdrawal, SettleWithdrawal, ReleaseWithdrawal
      webhooks/               ProviderWebhookHandler, event names
      outbox/                 Message, Relay, Publisher interface, RabbitPublisher, relay loop
      messages/               PayoutRequested + outbox message constructor (leaf package: withdrawals, payouts and
                              reconciliation all enqueue it, and payouts also uses withdrawals)
      payouts/                Processor, Consumer, topology
      ledgertest/             test-only helpers: Postgres/RabbitMQ containers, seeding, scenarios
      reconciliation/         PendingPayoutSweeper, InvariantChecker, reconciliation loop
    api/                      router, problem.go, one handler file per .NET controller, health
    app/                      composition root: builds every component from Config and runs them under errgroup
  scripts/e2e.sh
```

Dependencies point one way: `api` → feature slices → `store`/`domain`/`providers` → `common`. Slices may
depend on other slices where .NET handlers do: `webhooks` uses `topups` and `withdrawals`, `payouts` uses
`withdrawals`, and `reconciliation` uses `withdrawals` and `payouts`. There must be no cycles.

## Mapping .NET concepts to Go

| .NET | Go |
|---|---|
| `Result<T>` / `Error` | Functions return `(T, error)`. `*common.Error{Code, Message}` implements `error`, and `domain` defines the `WalletErrors` sentinels (`ErrInvalidAmount`, `ErrWalletNotFound`, …). Callers use `errors.Is`/`errors.As`. `Validation(field, msg)` builds `Validation.<Field>` errors. Any other error is unexpected (infrastructure failure, ambiguous provider result). |
| DI container | Constructors wired by hand in `main.go` (for example `withdrawals.NewRequestHandler(store, clock)`). |
| Scoped `DbContext` | `*store.Store` wraps a `pgxpool.Pool`; each operation runs in one `pgx.Tx`. |
| EF tracked entities + `Version` concurrency token | Rows load into `domain` structs, the domain methods (`PlaceHold`, `ReleaseHold`, `Credit`, `Debit`) mutate them in memory, and the store persists them. Account writes are `UPDATE accounts SET … , version = $new WHERE id = $id AND version = $old`; zero rows affected is a conflict. |
| EF change tracking + `SaveChanges` | `store.Tx` is a small unit of work. Entities loaded through it are tracked (identity map), `Add*` stages inserts, and `store.Run` writes every change in one database transaction after the operation returns. A tracked account is updated only if its `version` is unchanged (`WHERE version = $loaded`). A tracked transaction is updated only if its `status` is unchanged, matching the EF concurrency token on `Status`. Zero rows updated, or a unique violation, is `store.ErrConflict`. |
| `ConcurrencyRetry.ExecuteAsync` | `store.WithRetry(ctx, func(ctx, q *db.Queries) (T, error)) (T, error)`. It runs the function in a transaction. A domain error rolls back and returns. A version conflict or a Postgres `23505` rolls back and retries. After 3 attempts it returns `ErrConcurrencyExhausted`. |
| `IOptions<WalletOptions>` | `config.Wallet`: `OutboxPollInterval` (1s), `OutboxBatchSize` (50), `SweeperInterval` (5m), `PendingThreshold` (15m), read from `WALLET_OUTBOX_POLL_INTERVAL`, `WALLET_OUTBOX_BATCH_SIZE`, `WALLET_SWEEPER_INTERVAL`, `WALLET_PENDING_THRESHOLD`. |
| `BackgroundService` | Each worker exposes `Run(ctx) error` and loops on a `time.Ticker`. A failure in one tick is logged and never ends the loop. All workers and the HTTP server run under one `errgroup`, and shutdown is graceful on SIGINT/SIGTERM. |
| `IClock` | `common.Clock` interface; `FakeClock` for tests. |
| `ILogger` + `LoggerMessage` | `*slog.Logger` injected into each component, with the same messages and structured fields. |
| `InvalidOperationException` on an impossible transition (for example `MarkSucceeded` on a final transaction) | `panic`. As in .NET it signals a bug, and handlers check `IsFinal` first. |
| `Guid.CreateVersion7(now)` | `uuid.NewV7()` |

## Schema (goose migration `00001_init.sql`)

- `accounts`: `id uuid pk`, `user_id uuid null`, `type text` (`User`/`System`), `name text null`,
  `currency char(3)`, `balance bigint`, `available_balance bigint`, `version int`, `created_at timestamptz`.
  - Check constraints:
    - user wallets: `type <> 'User' OR (balance >= 0 AND available_balance >= 0 AND available_balance <= balance)`;
    - currency: `currency = 'NGN'`.
  - Partial unique indexes on `(user_id, currency) WHERE user_id IS NOT NULL` and `(name) WHERE name IS NOT NULL`.
  - Seeds the `ProviderSettlement` system account `00000000-0000-0000-0000-000000000001`.
- `transactions`: `id`, `account_id` fk, `type`, `status`, `amount`, `reference_id`, `provider_reference_id null`,
  `request_hash`, `created_at`, `completed_at null`.
  - Unique `(account_id, reference_id)`.
  - The lookup indexes .NET has (provider reference, status/created_at for the sweeper).
- `ledger_entries`: `id`, `transaction_id` fk, `account_id` fk, `direction`, `amount`, `currency`, `balance_before`,
  `balance_after`, `created_at`. Append-only.
- `withdrawals`: `transaction_id pk/fk`, `bank_code`, `account_number`, `account_name`.
- `outbox_messages`: `id`, `type`, `payload jsonb`, `occurred_at`, `processed_at null`, `attempts int`,
  `last_error null`, plus a partial index on unprocessed messages ordered by `(occurred_at, id)`.

Column lengths and constraints copy the EF configurations (`FieldLimits`). The implementation plan transcribes
them exactly from `dotnet/src/Wallet.Ledger/Persistence/*Configuration.cs`.

## Data flow

Each arrow labelled "tx" is one database transaction.

1. **Create wallet:** insert a user account. A unique violation means a concurrent create won, so re-read and
   return 200.
2. **Top-up:** validate, compute RequestHash, `provider.InitiateCharge`, then tx: idempotency check and insert a
   Pending TopUp with the provider reference. Replay → 200, new → 201.
3. **charge.success webhook:** tx: debit ProviderSettlement, credit the wallet, write two ledger entries, mark
   Succeeded. Idempotent on final transactions.
4. **charge.failed webhook:** mark Failed with no entries.
5. **Withdrawal:** tx: `PlaceHold`; insert the Pending Withdrawal, the withdrawal details and an outbox message
   `wallet.payout-requested` `{transactionId}`. Idempotent replay and DuplicateReference behave as in .NET.
6. **Outbox relay** (every `OutboxPollInterval`): select up to `OutboxBatchSize` unprocessed messages, oldest
   first. For each message, publish with a confirm; on success mark it processed, otherwise record the failure
   (attempts+1, `last_error` truncated to 2000). Save after each message. Delivery is at-least-once.
7. **Payout consumer → Processor**, with these outcomes:
   - unknown transaction → skip (ack);
   - final, or already has a provider reference → skip (ack);
   - provider `Accepted` with a reference → store the reference (ack);
   - `Rejected` → `ReleaseWithdrawal` (ack);
   - ambiguous result or transient error → log, wait 2s, nack with requeue. The quorum queue dead-letters the
     message after 5 deliveries.
   - JSON it cannot parse → nack without requeue (straight to the dead-letter queue).
8. **transfer.success / transfer.failed webhook:** find the withdrawal by provider reference, or by `reference`
   parsed as the transaction id (ProviderKeys.Payout), then settle it (debit the wallet from the hold, credit
   ProviderSettlement) or release it.
9. **Reconciliation loop** (every `SweeperInterval`):
   - **Sweeper:** for each Pending withdrawal older than `PendingThreshold`, query the provider for its status:
     - Succeeded → settle;
     - Failed → release;
     - NotFound → in one tx, clear the stored provider reference and insert a new outbox message;
     - otherwise → still pending.

     Log the summary.
   - **Invariant checker:** check I1 (each transaction has exactly 2 entries and 1 debit, and debits equal
     credits), I3 (account balance equals the net of its entries), I4 (user wallet balances are valid) and I6
     (all balances sum to 0). Log every violation and correct nothing.

## RabbitMQ

- **Topology** (`payouts.DeclareTopology`), declared by both the publisher and the consumer before use:
  - fanout exchange `wallet.payouts.dlx`;
  - queue `wallet.payouts.dead`, bound to it;
  - durable quorum queue `wallet.payouts` with `x-delivery-limit: 5` and `x-dead-letter-exchange: wallet.payouts.dlx`.
- **Publisher** (`outbox.RabbitPublisher`):
  - Rejects any message type other than `wallet.payout-requested`.
  - Publishes on a channel in confirm mode to the default exchange, routing key `wallet.payouts`, with
    `mandatory: true`, persistent delivery, MessageId = outbox id, Type, and `application/json`.
  - Waits for the confirm. A nack, or a message returned via `NotifyReturn`, is an error.
  - A mutex serialises publishes. If the channel is closed, it reopens it and declares the topology again.
- **Connection** (`rabbit.Conn`): re-dials with exponential backoff (capped at 30s) when the connection closes.
  `IsOpen()` feeds `/health`.
- **Consumer:** an outer loop that gets a channel, declares the topology, sets `Qos(10)`, consumes with manual
  ack, and handles deliveries one at a time. When the deliveries channel closes it reconnects; when the context
  is cancelled it stops.

## Payment provider

`providers.PaymentProvider` interface:

- `InitiateCharge(ctx, ref, amount)`
- `InitiatePayout(ctx, PayoutInstruction)`
- `GetPayoutStatus(ctx, ref)`

Transient faults come back as errors; a definite refusal is `PayoutInitiation{Outcome: Rejected}`.

`FakePaymentProvider` is an in-memory, mutex-guarded port of the .NET fake:

- it is idempotent per payout reference;
- `PayoutBehaviour func(PayoutInstruction) (PayoutInitiation, error)` scripts payout responses in tests;
- `PayoutAttempts` counts payout calls;
- `SetPayoutStatus` and `FindByProviderReference` serve `/dev/provider`.

## HTTP API

All routes use the stdlib mux. A `{userId}` that is not a UUID returns 404, matching the `:guid` route constraint.

| Method & path | Success | Notes |
|---|---|---|
| `POST /users/{userId}/wallet` | 201 + `Location`, or 200 if it already exists | `WalletResponse` |
| `GET /users/{userId}/wallet` | 200 | `WalletResponse` |
| `GET /users/{userId}/wallet/transactions` | 200 | `[]TransactionResponse` |
| `POST /users/{userId}/top-ups` | 201 + `Location: /users/{id}/wallet/transactions`, or 200 on replay | body `{referenceId, amount}` |
| `POST /users/{userId}/withdrawals` | 202 Accepted (no `Location`), or 200 on replay | body `{referenceId, amount, bankCode, accountNumber, accountName}` |
| `POST /webhooks/provider` | 200 + `TransactionResponse` | body `{event, providerReferenceId, reference?, amount?}`; `amount` defaults to 0 |
| `POST /dev/provider/{providerReferenceId}/complete?outcome=success\|failed` | 200 | Only when `APP_ENV=development`, otherwise 404. A bad outcome returns 400 `Validation.Outcome`. |
| `GET /alive` | 200 `Healthy` | Liveness: the process is up. All environments. |
| `GET /health` | 200 `Healthy` / 503 `Unhealthy` (text/plain, like ASP.NET health checks) | Readiness: Postgres ping plus an open RabbitMQ connection. All environments. |

**JSON:**
- camelCase keys, the same fields as `WalletResponse` / `TransactionResponse`;
- enums as their .NET names (`TopUp`, `Withdrawal`, `Pending`, `Succeeded`, `Failed`);
- timestamps in RFC 3339 UTC; amounts as int64 kobo.

**Request bodies:** request structs use pointer fields so a missing required field can be detected. A missing
field or malformed JSON returns a 400 problem, with title `Validation.<Field>` or `Validation.Body`.

**Errors:** `api/problem.go` writes `application/problem+json` with `{type, title: <error code>, status, detail}`.
The status mapping copies `ProblemResults.StatusCodeFor`:

| Error code | Status |
|---|---|
| `Money.InvalidAmount`, `Money.UnsupportedCurrency`, `Validation.*` | 400 |
| `Wallet.NotFound`, `Transaction.NotFound` | 404 |
| `Transaction.DuplicateReference`, `Concurrency.Exhausted`, `Ledger.*` | 409 |
| `Wallet.InsufficientFunds` | 422 |
| anything else | 500 |

Unexpected errors are logged, and the response is a generic 500 with no internal detail.

## Configuration

| Variable | Meaning | Default |
|---|---|---|
| `APP_ENV` | `development` enables dev routes and migrations on startup | `production` |
| `HTTP_ADDR` | listen address | `:8080` |
| `DATABASE_URL` | pgx connection string | required |
| `RABBITMQ_URL` | AMQP URL | required |
| `WALLET_*` | the worker settings above | the .NET defaults |

Migrations run on startup only in development, mirroring `MigrateAsync`. In other environments
`wallet-api migrate` applies them as an explicit step.

## Testing

- **Domain:** pure unit tests ported from `Ledger/Domain/*` (Account, LedgerPosting, LedgerTransaction,
  RequestHash) and `FakeClockTests`.
- **Store / handlers / relay / processor / sweeper / invariants / webhooks / providers:**
  - These run against real Postgres via testcontainers-go. `TestMain` starts one container per package and
    migrates a template database; each test gets a fresh database created from the template.
  - Concurrency: `store.Store` has an optional `BeforeWrite func(ctx context.Context)` hook (nil in production).
    Tests use it to commit a competing write on another connection, which deterministically triggers version
    conflicts and unique-violation retries. This replaces `ConcurrentWriteInterceptor`.
  - Relay and processor tests use a `RecordingPublisher` and a scripted `FakePaymentProvider`, so they need no
    RabbitMQ.
- **RabbitMQ:** one integration test (testcontainers rabbitmq) covers publisher confirms, consumer ack, nack
  with requeue up to dead-lettering, and unreadable messages going straight to the dead-letter queue.
- **API:** `httptest` against the real router and Postgres, porting `WalletApiTests` and `ProblemResultsTests`.
- **Coverage target:** every scenario from the .NET suite (about 121 cases) has a Go counterpart.
  `ServiceRegistrationTests` and `WalletModelTests`/`WalletDbContextTests` become schema/migration tests (the
  constraints and indexes exist, the seed row is present).
- **e2e:** `golang/scripts/e2e.sh` copies the .NET script's checks. Startup uses `go build` plus environment
  variables, and the database invariant SQL uses the snake_case schema.

## Tooling

- `make generate` runs `sqlc generate`; `make test` runs `go test ./...`; `make e2e` runs `scripts/e2e.sh`;
  `make run` runs the API.
- `make lint` runs `gofmt -l`, `go vet` and `golangci-lint` (if installed).
- A `golang/AGENTS.md` records these commands and conventions, mirroring `dotnet/AGENTS.md`.
