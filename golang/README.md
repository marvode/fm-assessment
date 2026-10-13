# Wallet service (Go)

A single-currency (NGN) wallet backed by a double-entry ledger. It is a **behaviour-compatible port** of the .NET
implementation in [`../dotnet`](../dotnet/README.md): the HTTP contract, error codes, ledger rules and
outbox → RabbitMQ → payout semantics are the same. Go owns its own idiomatic Postgres schema (snake_case), so the two
do not share a database.

- **Stack:**
  - Go 1.26+, stdlib `net/http`
  - pgx/v5 + sqlc (PostgreSQL), goose migrations (embedded)
  - amqp091-go (RabbitMQ)
  - `log/slog`, `errgroup`, caarlos0/env
  - testify + testcontainers-go
- **Design:** [`../docs/superpowers/specs/2026-10-13-golang-port-design.md`](../docs/superpowers/specs/2026-10-13-golang-port-design.md).
- **Contributor rules:** [`AGENTS.md`](AGENTS.md).

---

## Ledger model

| Concept | Meaning |
|---|---|
| **Account** | A user wallet (`User`, one per user) or a system account (`System`). The seeded `ProviderSettlement` account (`00000000-0000-0000-0000-000000000001`) mirrors money held at the provider. |
| **Balance** | Settled money. Always equals the net of the account's ledger entries. |
| **AvailableBalance** | `Balance` minus active withdrawal holds. This is what a user can spend. |
| **Transaction** | A `TopUp` or `Withdrawal`: `Pending` → `Succeeded` or `Failed`. Final states never change. |
| **Entry** | One side of a posting. A settled transaction has exactly one `Debit` and one `Credit` of equal amount. Append-only. |
| **Hold** | A withdrawal lowers `AvailableBalance` only. Settling debits `Balance`; releasing restores `AvailableBalance` and writes no entries. |

Amounts are `int64` kobo. User wallets can never go negative (enforced in `domain` and by check constraints in the
migration); system accounts can.

The `domain` package is pure. Handlers load entities, call domain methods (`PlaceHold`, `ReleaseHold`, `Credit`,
`Debit`, `domain.Post`) and let the store persist the result.

**Invariants**, checked by `reconciliation.Checker`, which logs `ledger invariant violated` and never corrects:

| Invariant | Rule |
|---|---|
| I1 | Each posted transaction has 2 entries, 1 debit, and debits equal credits. |
| I3 | An account's balance equals the net of its entries. |
| I4 | User wallets have balance ≥ 0, available ≥ 0, and available ≤ balance. |
| I6 | All balances sum to zero. |

### Persistence: the unit of work

`internal/ledger/store` reproduces EF Core's change tracking on top of pgx and sqlc:

- **`store.Run(ctx, s, fn)`** runs `fn(ctx, tx)` in one database transaction.
  - Entities loaded through `*store.Tx` are tracked (an identity map), and `tx.Add*` stages inserts.
  - When `fn` returns, changes are written in foreign-key order.
  - A changed account saves only if its `version` is unchanged (`UPDATE … WHERE id = $1 AND version = $loaded`).
  - A changed transaction saves only if its `status` is unchanged.
  - Zero rows updated, or a unique violation, is `store.ErrConflict`, and nothing is written.
- **`store.WithRetry`** is the port of .NET's `ConcurrencyRetry`. It reruns from fresh state on `ErrConflict`, up to
  3 attempts, then returns `Concurrency.Exhausted`.
- **`store.WithBeforeWrite(hook)`** runs a hook just before the writes. Tests use it to commit a competing write from
  another connection, which forces races deterministically.

**Idempotency:**

| Repeat request | Result |
|---|---|
| Same `referenceId` (unique per wallet) and same request hash | Replays the original (200) |
| Same `referenceId` and a different request | `Transaction.DuplicateReference` (409) |

| Provider key | Format |
|---|---|
| Charge key | `<wallet id hex>-<referenceId>` |
| Payout key | The withdrawal's transaction id as 32 hex characters |

---

## HTTP API

The router is `internal/api` (stdlib `ServeMux`). Errors are `application/problem+json`
`{type, title: <error code>, status, detail}`. Unexpected errors and panics return a generic 500, logged as
`unhandled error`.

| Method & path | Body | Success |
|---|---|---|
| `POST /users/{userId}/wallet` | – | `201` + `Location`, or `200` if it already exists |
| `GET /users/{userId}/wallet` | – | `200` `{accountId, userId, balance, availableBalance, currency}` |
| `GET /users/{userId}/wallet/transactions` | – | `200`: the newest 100, newest first (`[]` when empty) |
| `POST /users/{userId}/top-ups` | `{referenceId, amount}` | `201` Pending top-up + `Location`, or `200` on replay. Opens the wallet on the first top-up. |
| `POST /users/{userId}/withdrawals` | `{referenceId, amount, bankCode, accountNumber, accountName}` | `202 Accepted`, or `200` on replay |
| `POST /webhooks/provider` | `{event, providerReferenceId, reference?, amount?}` | `200` with the transaction |
| `POST /dev/provider/{providerReferenceId}/complete?outcome=success\|failed` | – | `200`. Only when `APP_ENV=development`. |
| `GET /alive` | – | `200 Healthy` (liveness, all environments) |
| `GET /health` | – | `200 Healthy` / `503 Unhealthy` (Postgres ping + RabbitMQ connection, all environments) |

`{userId}` must be a UUID (otherwise 404). There is no `/` route. Request bodies are limited to 1 MiB.

**Transaction JSON fields:** `id`, `type`, `status`, `amount`, `currency`, `referenceId`, `providerReferenceId`,
`createdAt` (RFC 3339 UTC), `completedAt`.

**Webhook events:**

| Event | Effect |
|---|---|
| `charge.success` | Credits the wallet. If the amount doesn't match, the top-up is marked `Failed` instead. |
| `charge.failed` | Marks the top-up `Failed`. |
| `transfer.success` | Settles the withdrawal. |
| `transfer.failed` | Releases the hold. |

Transfers match the withdrawal by `providerReferenceId`, or by `reference` (the payout key). Every webhook is
idempotent.

**Validation:**

| Field | Rule |
|---|---|
| `amount` | Must be > 0 |
| `referenceId` | Required, ≤ 128 characters |
| `bankCode` | Required, ≤ 16 characters |
| `accountNumber` | Exactly 10 digits |
| `accountName` | Required, ≤ 128 characters |
| Any text field | Must not contain a NUL byte |

A missing, null or malformed field returns 400 `Validation.<Field>` or `Validation.Body`.

**Error codes → status** (`api.StatusFor`):

| Code | Status |
|---|---|
| `Money.*`, `Validation.*` | 400 |
| `Wallet.NotFound`, `Transaction.NotFound` | 404 |
| `Transaction.DuplicateReference`, `Concurrency.Exhausted`, `Ledger.*` | 409 |
| `Wallet.InsufficientFunds` | 422 |
| anything else | 500 |

---

## Flows

```
Top-up
  tx 1: get-or-create wallet, replay check
  provider.InitiateCharge            ◄── no DB transaction or connection held during the provider call
  tx 2: replay re-check, insert Pending TopUp (+ providerReferenceId); a lost unique race retries (max 3)
  charge.success ──► debit ProviderSettlement, credit wallet, 2 entries, Succeeded           (WithRetry)

Withdrawal
  POST ──► PlaceHold + Pending Withdrawal + withdrawals row + outbox "wallet.payout-requested" (one tx, WithRetry)
  outbox.Relay (every 1s, batch 50, oldest first)
        ──► RabbitPublisher: confirm-mode channel, mandatory publish to wallet.payouts, wait for the confirm
            (a returned message = failure) ──► mark processed / record failure (attempts, last_error)
  payouts.Consumer ──► payouts.Processor
        Accepted + reference ──► store providerReferenceId (still Pending), ack
        Rejected             ──► release hold, ack
        error / ambiguous    ──► wait 2s, republish with x-wallet-attempts+1 (confirmed), ack original
        5th failure          ──► nack(no requeue) ──► wallet.payouts.dead
        unreadable message   ──► nack(no requeue) ──► wallet.payouts.dead
  transfer.success ──► debit wallet from hold, credit ProviderSettlement, Succeeded
  transfer.failed  ──► release hold, Failed

Reconciliation (every WALLET_SWEEPER_INTERVAL) — reconciliation.RunOnce
  Sweeper: Pending withdrawals older than WALLET_PENDING_THRESHOLD → provider status:
        Succeeded → settle · Failed → release · NotFound → clear a stale providerReferenceId and
        enqueue a new payout request (hold kept) · otherwise still pending
        logs: payout sweep {settled, released, redriven, still_pending, errors}
  Checker: I1/I3/I4/I6
```

**RabbitMQ topology** (`payouts.DeclareTopology`, declared by both publisher and consumer):

| Name | Kind |
|---|---|
| `wallet.payouts` | Durable quorum queue: `x-delivery-limit: 5`, `x-dead-letter-exchange: wallet.payouts.dlx` |
| `wallet.payouts.dlx` | Fanout exchange |
| `wallet.payouts.dead` | Dead-letter queue |

**Why the consumer counts attempts itself.** On RabbitMQ 4, quorum queues do not count AMQP 0.9.1
`nack(requeue=true)` towards `x-delivery-limit`, so broker-side dead-lettering never triggers. The consumer instead
tracks failures in the `x-wallet-attempts` header and dead-letters on the 5th. If a crash happens between the
republish and the ack, one delivery is duplicated, and the idempotent processor absorbs it.

**Resilience:**
- `rabbit.Conn` re-dials when the connection is gone.
- The publisher reopens its channel when it closes.
- The consumer reconnects with exponential backoff, capped at 30s.
- A withdrawal is still accepted while the broker is down; its payout waits in the outbox and is delivered once the
  broker recovers.
- On shutdown, a delivery whose processing was cut short is neither acked nor nacked, so RabbitMQ redelivers it.

---

## Project layout

```
cmd/wallet-api/main.go        env config, SIGINT/SIGTERM handling, `migrate` subcommand, app.Run
internal/
  common/                     Error{Code,Message} (errors.Is by code), Clock / SystemClock / FakeClock, RunEvery
  config/                     Config parsed from environment variables
  rabbit/                     re-dialling RabbitMQ connection
  api/                        router, problem responses, JSON decoding, handlers, health, panic recovery
  app/                        composition root: builds everything, runs HTTP + workers under errgroup
  ledger/
    domain/                   Account, Transaction, Entry, Post, errors, RequestHash, field limits (no I/O)
    store/                    migrations/ (goose), queries/ (sqlc input), db/ (generated), unit of work, reads
    providers/                PaymentProvider interface, FakePaymentProvider, idempotency keys
    wallets/ topups/ withdrawals/ webhooks/      command handlers
    messages/                 PayoutRequested outbox event
    outbox/                   Relay, Publisher interface, RabbitPublisher
    payouts/                  Processor, Consumer, topology
    reconciliation/           Sweeper, Checker, RunOnce
    ledgertest/               test-only helpers: containers, seeding, scenarios, hooks
scripts/e2e.sh                end-to-end test (same checks as dotnet/scripts/e2e.sh)
Dockerfile, compose.yaml   container image and full local stack (Postgres, RabbitMQ, API)
Makefile, sqlc.yaml
```

The payment provider is an in-memory fake, and its state is lost on restart. The e2e script uses that to exercise
the sweeper's resubmission path.

---

## Running

**Prerequisites:** Go 1.26+ (the module minimum, raised by its dependencies), Docker.

**Everything in Docker** (Postgres, RabbitMQ and the API built from `Dockerfile`):

```bash
cd golang
docker compose up --build      # API on http://localhost:8080, RabbitMQ UI on http://localhost:15672
docker compose down -v         # stop and delete the data volumes
```

The compose stack runs with `APP_ENV=development`, which migrates on startup and enables `/dev/provider`. For a
production-style start:

```bash
docker compose up -d --wait postgres rabbitmq
APP_ENV=production docker compose run --rm api migrate
APP_ENV=production docker compose up -d api
```

**API on the host, dependencies in Docker:**

```bash
make up        # docker compose up -d postgres rabbitmq
make run       # APP_ENV=development: applies migrations, enables /dev/provider; listens on :8080
```

Production-style:

```bash
go build -o bin/wallet-api ./cmd/wallet-api
DATABASE_URL=... bin/wallet-api migrate                 # apply migrations, then exit
DATABASE_URL=... RABBITMQ_URL=... bin/wallet-api
```

### Configuration (environment variables)

| Variable | Meaning | Default |
|---|---|---|
| `APP_ENV` | `development` applies migrations on startup and enables `/dev/provider` | `production` |
| `HTTP_ADDR` | Listen address | `:8080` |
| `DATABASE_URL` | pgx connection string, for example `postgres://user:pass@host:5432/db?sslmode=disable` | **required** |
| `RABBITMQ_URL` | AMQP URL, for example `amqp://guest:guest@host:5672/` | required to serve (not for `migrate`) |
| `WALLET_OUTBOX_POLL_INTERVAL` | Outbox relay period | `1s` |
| `WALLET_OUTBOX_BATCH_SIZE` | Messages per relay run | `50` |
| `WALLET_SWEEPER_INTERVAL` | Reconciliation period | `5m` |
| `WALLET_PENDING_THRESHOLD` | Age at which a Pending withdrawal is swept | `15m` |

Logs are JSON (`log/slog`) on stdout.

---

## Development

| Task | Command |
|---|---|
| Tests | `go test ./...` (needs Docker) |
| Lint | `make lint` (gofmt, go vet, golangci-lint if installed) |
| Regenerate the DB layer after editing `store/queries` or migrations | `make generate` (sqlc 1.29 in Docker). Never edit `store/db/` by hand. |
| New schema change | Add a new goose file in `internal/ledger/store/migrations/`; never edit an applied one |

### Testing

- **Unit tests:**
  - `domain`
  - `common`
  - `providers`
  - the consumer's settle logic, which uses a fake acknowledger
- **Integration tests** run against a real `postgres:17`, started once per test binary by testcontainers:
  - Each test gets a fresh database cloned from a migrated template.
  - Concurrency races (version conflicts, unique-key races, settle vs release, webhook races) are forced with
    `ledgertest.Hooked`.
- **RabbitMQ tests** run against a real `rabbitmq:4-management` and cover:
  - publisher confirms and message properties;
  - reconnect after the broker drops connections;
  - consumer ack;
  - dead-lettering after 5 failures;
  - dead-lettering unreadable messages.
- **API tests** use `httptest` against the real router and Postgres. They cover every route, the problem format,
  bad and oversized bodies, `/alive` and `/health`, and panic recovery.
- **App tests** run the whole binary's composition: a withdrawal flows through RabbitMQ to the provider, the app
  serves while RabbitMQ is down, and shutdown is clean.
- **Reusing your own servers:** set `WALLET_TEST_DATABASE_URL` / `WALLET_TEST_RABBITMQ_URL` and run
  `go test -p 1 ./...`.
- **End-to-end:** `make e2e` (or `scripts/e2e.sh`; `KEEP=1` keeps the containers). It needs docker, go, curl, jq and
  uuidgen.
  - It builds the binary, starts throwaway Postgres and RabbitMQ, and runs 107 checks mirroring `dotnet/scripts/e2e.sh` (minus its OpenAPI/Scalar checks, plus `/alive`, `/health` and no-root-route checks).
    These include parallel overspend attempts, a broker outage, and an API restart that wipes the fake provider.
  - It finishes with SQL invariant checks.

---

## Differences from the .NET implementation

**Intentional:**

| Area | Go behaviour |
|---|---|
| Health endpoints | `/health` and `/alive` are served in every environment. There is no `/`, and no OpenAPI/Scalar. |
| Schema | Postgres schema in snake_case, owned by goose instead of EF migrations. |
| Payout retries | The consumer counts attempts and dead-letters on the 5th failure. On RabbitMQ 4, .NET retries forever. |
| Top-up provider call | The charge runs outside any database transaction. |
| Top-up races | Losing the unique-key race repeatedly returns 409 `Concurrency.Exhausted` instead of a 500. |
| Bad request bodies | Return `Validation.<Field>` / `Validation.Body` titles instead of ASP.NET's model-state message. |
| NUL bytes | Text containing a NUL byte is rejected with 400; .NET would hit a database error. |
| Tests | Run against real Postgres instead of EF InMemory + interceptors. |
| Local infrastructure | `compose.yaml` (full stack including the API) instead of the Aspire AppHost. There is no Redis or Kafka, which were unused, and no OpenTelemetry yet. |

**Known small divergences** (not yet addressed):
- `"amount":"5000"` as a string is rejected; ASP.NET accepts it.
- A webhook with `"amount": null` is treated as 0; ASP.NET returns 400.
- There is no 415 for a non-JSON `Content-Type`.
- Lengths are counted in characters, not UTF-16 units.

---

## Known limitations

- **No authentication or authorization.** Any caller can act on any `{userId}`. Same as .NET.
- **Unsigned webhooks.** `/webhooks/provider` does not verify a provider signature, so a forged `charge.success`
  credits a wallet. Add HMAC verification before using a real provider. Same as .NET.
- **The outbox relay isn't safe to run on several instances at once.** It has no `FOR UPDATE SKIP LOCKED`.
- **Operational rough edges** found in review and deferred:
  - `rabbit.Conn` has no dial backoff, so during an outage the relay dials once per message.
  - `/health` can stall for up to about 10s while a dial is in progress.
  - The HTTP server has no read or idle timeouts.
  - Shutdown exits with code 1 if a request outlives the 10s grace period.
  - A panic after a response has started produces a corrupted response.
  - There is no overflow guard on very large amounts.
- **The payment provider is an in-memory fake.**
