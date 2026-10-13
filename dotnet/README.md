# Wallet service (.NET)

A single-currency (NGN) wallet backed by a double-entry ledger. Users top up through a payment provider and withdraw
to a bank account. Payouts go through a transactional outbox and RabbitMQ, and a reconciliation loop repairs payouts
that went quiet and checks the ledger's invariants.

This is the reference implementation. A behaviour-compatible Go port lives in [`../golang`](../golang/README.md).

- **Stack:** .NET 10, ASP.NET Core MVC controllers, EF Core 10 + Npgsql (PostgreSQL), RabbitMQ.Client, .NET Aspire
  (local orchestration, service defaults, OpenTelemetry), xUnit.
- **Contributor rules** (style, commands, do's and don'ts): [`AGENTS.md`](AGENTS.md).

---

## Ledger model

| Concept | Meaning |
|---|---|
| **Account** | A balance holder: a user wallet (`Type = User`, one per user) or a system account (`Type = System`). The seeded `ProviderSettlement` account (`00000000-0000-0000-0000-000000000001`) mirrors money held at the payment provider. |
| **Balance** | Settled money. Always equals the net of the account's ledger entries. |
| **AvailableBalance** | `Balance` minus active withdrawal holds. This is what a user can spend. |
| **Transaction** | One money movement (`TopUp` or `Withdrawal`) with a lifecycle: `Pending` → `Succeeded` or `Failed`. Final states never change. |
| **LedgerEntry** | One side of a posting. Every settled transaction has exactly one `Debit` and one `Credit` of the same amount. Entries are append-only. |
| **Hold** | Placing a withdrawal lowers `AvailableBalance` only. Settling it debits `Balance`; releasing it restores `AvailableBalance`, and no entries are written because no money moved. |

All amounts are `long` kobo. User wallets can never go negative (enforced in code and by database check
constraints); system accounts can.

**Invariants**, checked by `LedgerInvariantChecker`, which logs violations and never corrects them:

| Invariant | Rule |
|---|---|
| I1 | Each posted transaction has exactly two entries, one debit, and debits equal credits. |
| I3 | An account's balance equals the net of its entries. |
| I4 | User wallets have balance ≥ 0, available ≥ 0, and available ≤ balance. |
| I6 | All account balances sum to zero. |

**Concurrency.** `Account.Version` and `Transaction.Status` are EF concurrency tokens.
`ConcurrencyRetry.ExecuteAsync` runs load → change → save. If another writer changed a tracked row first (a stale
token) or committed the same unique key, it reruns from a clean change tracker, up to 3 attempts. After that it
returns `Concurrency.Exhausted`.

**Idempotency.** Every command carries a client `referenceId`, which is unique per wallet, plus a hash of the
request's meaningful fields:

| Repeat request | Result |
|---|---|
| Same `referenceId` and same request | Replays the original transaction (HTTP 200) |
| Same `referenceId` and a different request | `Transaction.DuplicateReference` (409) |

Provider keys are derived so they are stable across retries:

| Key | Format |
|---|---|
| Charge key | `<wallet id, 32 hex>-<referenceId>` |
| Payout key | The withdrawal's transaction id as 32 hex characters |

---

## HTTP API

Controllers live in `src/Wallet.Api/Controllers`. Errors are RFC 7807 `application/problem+json`, and the `title` is
a stable error code.

| Method & path | Body | Success |
|---|---|---|
| `POST /users/{userId}/wallet` | – | `201 Created` + `Location`, or `200` if the wallet already exists |
| `GET /users/{userId}/wallet` | – | `200` `{accountId, userId, balance, availableBalance, currency}` |
| `GET /users/{userId}/wallet/transactions` | – | `200`: the newest 100 transactions, newest first |
| `POST /users/{userId}/top-ups` | `{referenceId, amount}` | `201` (Pending top-up), or `200` on replay. Opens the wallet on the first top-up. |
| `POST /users/{userId}/withdrawals` | `{referenceId, amount, bankCode, accountNumber, accountName}` | `202 Accepted` (funds held), or `200` on replay |
| `POST /webhooks/provider` | `{event, providerReferenceId, reference?, amount}` | `200` with the transaction |
| `POST /dev/provider/{providerReferenceId}/complete?outcome=success\|failed` | – | `200` (Development only): plays the fake provider's webhook |
| `GET /health`, `GET /alive` | – | Readiness / liveness (Development only, via ServiceDefaults) |
| `GET /openapi/v1.json`, `GET /scalar/v1` | – | OpenAPI document and Scalar UI (Development only) |

`{userId}` must be a GUID; anything else returns 404.

**Transaction JSON fields:** `id`, `type` (`TopUp`/`Withdrawal`), `status` (`Pending`/`Succeeded`/`Failed`),
`amount`, `currency`, `referenceId`, `providerReferenceId`, `createdAt`, `completedAt`.

**Webhook events:**

| Event | Effect |
|---|---|
| `charge.success` | Credits the wallet. If the amount doesn't match, the top-up is marked `Failed` instead. |
| `charge.failed` | Marks the top-up `Failed`. |
| `transfer.success` | Settles the withdrawal. |
| `transfer.failed` | Releases the hold. |

Transfer events match the withdrawal by `providerReferenceId`, or by `reference` (the payout key), so a webhook
that arrives before the payout worker has stored the provider reference still matches. Every webhook is idempotent.

**Validation:**

| Field | Rule |
|---|---|
| `amount` | Must be > 0 |
| `referenceId` | Required, ≤ 128 characters |
| `bankCode` | Required, ≤ 16 characters |
| `accountNumber` | Exactly 10 digits (NUBAN) |
| `accountName` | Required, ≤ 128 characters |

**Error codes → status** (`ProblemResults.StatusCodeFor`):

| Code | Status |
|---|---|
| `Money.InvalidAmount`, `Money.UnsupportedCurrency`, `Validation.*` | 400 |
| `Wallet.NotFound`, `Transaction.NotFound` | 404 |
| `Transaction.DuplicateReference`, `Concurrency.Exhausted`, `Ledger.*` | 409 |
| `Wallet.InsufficientFunds` | 422 |
| anything else | 500 |

---

## Flows

```
Top-up
  POST /top-ups ── provider.InitiateCharge ──► Transactions: Pending TopUp (+ providerReferenceId)
  charge.success webhook ──► debit ProviderSettlement, credit wallet, 2 entries, Succeeded     (one SaveChanges)

Withdrawal
  POST /withdrawals ──► wallet.PlaceHold + Pending Withdrawal + Withdrawals row
                        + OutboxMessages(wallet.payout-requested)                               (one SaveChanges)
  OutboxRelayService (every 1s, batch 50, oldest first)
        ──► publish with publisher confirms ──► queue wallet.payouts ──► mark processed / record failure
  RabbitMqPayoutConsumer ──► PayoutProcessor
        Accepted + reference ──► store providerReferenceId (still Pending), ack
        Rejected             ──► release hold (Failed), ack
        ambiguous / transient ──► wait 2s, nack(requeue)
        unreadable JSON       ──► nack(no requeue) ──► wallet.payouts.dead
  transfer.success ──► debit wallet from hold, credit ProviderSettlement, Succeeded
  transfer.failed  ──► release hold, Failed

Reconciliation (ReconciliationService, every SweeperInterval)
  PendingPayoutSweeper: Pending withdrawals older than PendingThreshold → ask the provider:
        Succeeded → settle · Failed → release · NotFound → clear stale providerReferenceId and
        enqueue a new outbox message (hold kept) · otherwise still pending
  LedgerInvariantChecker: logs I1/I3/I4/I6 violations
```

**RabbitMQ topology** (`PayoutQueueTopology`, declared by both publisher and consumer):

| Name | Kind |
|---|---|
| `wallet.payouts` | Durable quorum queue: `x-delivery-limit: 5`, `x-dead-letter-exchange: wallet.payouts.dlx` |
| `wallet.payouts.dlx` | Fanout exchange |
| `wallet.payouts.dead` | Dead-letter queue |

The outbox makes delivery at-least-once; the processor is idempotent (it skips final withdrawals and ones that
already have a provider reference).

---

## Project layout

```
Wallet.slnx
src/
  Wallet.AppHost/          Aspire app host: Postgres (db "appdb"), RabbitMQ ("messaging"), Redis, Kafka, the API
  Wallet.ServiceDefaults/  OpenTelemetry, health checks, service discovery, HTTP resilience
  Wallet.Api/              Program.cs + MVC controllers (one per route group), ProblemResults
  Wallet.Common/           Result<T>, Error, IClock / SystemClock / FakeClock
  Wallet.Ledger/           The wallet, as vertical slices:
    Domain/                Account, LedgerTransaction, LedgerEntry, LedgerPosting, WalletErrors, RequestHash, FieldLimits
    Persistence/           WalletDbContext, entity configurations, ConcurrencyRetry, EF migrations
    Wallets/ TopUps/ Withdrawals/ Webhooks/   command handlers + DTOs
    Outbox/                OutboxMessage, OutboxRelay, OutboxRelayService, RabbitMqMessagePublisher
    Payouts/               PayoutRequested, PayoutProcessor, RabbitMqPayoutConsumer, PayoutQueueTopology
    Providers/             IPaymentProvider, FakePaymentProvider, ProviderKeys
    Reconciliation/        PendingPayoutSweeper, LedgerInvariantChecker, ReconciliationService
    ServiceCollectionExtensions.cs   AddWallet / AddWalletBackgroundWorkers
tests/Wallet.Tests/        xUnit: domain, handlers, persistence, outbox, payouts, reconciliation, API
scripts/e2e.sh             end-to-end test against real Postgres + RabbitMQ
Dockerfile, compose.yaml   container image and a full local stack without Aspire
```

The payment provider is an in-memory fake (`FakePaymentProvider`). Its state is lost when the process restarts,
which the e2e script relies on to exercise the sweeper's resubmission path.

---

## Running

**Prerequisites:**
- Docker (enough for the compose stack)
- .NET 10 SDK, for building, testing or running on the host
- For orchestration, the Aspire CLI or the AppHost project

**With Docker Compose** (Postgres, RabbitMQ and the API built from `Dockerfile`; no Aspire or .NET SDK needed):

```bash
cd dotnet
docker compose up --build      # API on http://localhost:5568, RabbitMQ UI on http://localhost:15682
docker compose down -v         # stop and delete the data volumes
```

The compose stack runs in `Development`, so EF migrations apply on startup and `/dev/provider`, `/health`, `/alive`,
`/openapi/v1.json` and `/scalar/v1` are available. Its host ports (Postgres 5442, AMQP 5682) differ from the Go
stack's, so both can run side by side.

**With Aspire** (starts Postgres, RabbitMQ, Redis, Kafka and the API):

```bash
cd dotnet
aspire run            # or: dotnet run --project src/Wallet.AppHost
```

**API only**, against your own Postgres and RabbitMQ:

```bash
cd dotnet
ConnectionStrings__appdb="Host=localhost;Port=5432;Database=appdb;Username=postgres;Password=postgres" \
ConnectionStrings__messaging="amqp://guest:guest@localhost:5672" \
dotnet run --project src/Wallet.Api          # http://localhost:5568 (launchSettings "http" profile)
```

In `Development`, startup applies EF migrations and maps `/dev/provider`, `/health`, `/alive`, `/openapi/v1.json`
and `/scalar/v1`. In other environments, apply migrations explicitly, for example with
`dotnet ef database update --project src/Wallet.Ledger`.

### Configuration

| Key | Meaning | Default |
|---|---|---|
| `ConnectionStrings:appdb` | Npgsql connection string | required (Aspire injects it) |
| `ConnectionStrings:messaging` | AMQP URI | required (Aspire injects it) |
| `Wallet:OutboxPollInterval` | Outbox relay period | `00:00:01` |
| `Wallet:OutboxBatchSize` | Messages published per relay run | `50` |
| `Wallet:SweeperInterval` | Reconciliation period | `00:05:00` |
| `Wallet:PendingThreshold` | Age at which a Pending withdrawal is swept | `00:15:00` |

Environment variables use `__` as the separator, for example `Wallet__SweeperInterval=00:00:05`.

---

## Testing

```bash
cd dotnet
dotnet build
dotnet test                  # ~121 xUnit tests, no external services
scripts/e2e.sh               # needs docker, dotnet, curl, jq, uuidgen; KEEP=1 keeps the containers
```

- **Unit and handler tests** use EF Core InMemory. Two interceptors make it behave like Postgres:
  - `AtomicSaveInterceptor` enforces unique indexes and makes a conflicting save write nothing.
  - `ConcurrentWriteInterceptor` runs a competing writer just before a save, to force races deterministically.
- **API tests** use `WebApplicationFactory` with the background workers removed.
- **`WalletModelTests`** checks that the migration declares the constraints and indexes InMemory can't enforce.
- **`scripts/e2e.sh`**:
  - starts throwaway `postgres:17` and `rabbitmq:4-management` containers and runs the API in Development, with a 5s
    sweeper and a 20s threshold;
  - exercises every endpoint and background flow, including parallel overspend attempts, a broker outage, and an API
    restart that wipes the fake provider;
  - checks the ledger invariants directly in SQL.

---

## Known limitations

- **No authentication or authorization.** Any caller can act on any `{userId}`.
- **Unsigned webhooks.** `/webhooks/provider` does not verify a provider signature, so a forged `charge.success`
  credits a wallet. Add HMAC verification before using a real provider.
- **The delivery limit never triggers on RabbitMQ 4.** Quorum queues there don't count AMQP 0.9.1
  `nack(requeue: true)` towards `x-delivery-limit`, so a payout that keeps failing is retried every 2s forever
  instead of being dead-lettered after 5 attempts. The Go port counts attempts itself to avoid this.
- **The outbox relay isn't safe to run on several instances at once.** It doesn't use `FOR UPDATE SKIP LOCKED`, so
  two instances would publish the same messages (delivery stays at-least-once).
- **The AppHost starts Redis and Kafka**, but the API doesn't use them.
- **The payment provider is an in-memory fake.**
