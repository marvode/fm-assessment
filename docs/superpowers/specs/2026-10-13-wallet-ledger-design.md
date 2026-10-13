# Wallet Ledger MVP — Design

Date: 2026-10-13
Status: Approved in conversation, pending written-spec review

## 1. Goal

An MVP digital wallet backend. Users top up their wallet through a payment provider and withdraw to a bank account. Postgres stores a double-entry ledger. Correctness comes first and is driven test-first (TDD).

### Decisions made

| Topic | Decision |
|---|---|
| Project shape | One new class library `src/Live.Wallet` (vertical slices); `Live.Api` hosts controllers and hosted services |
| Payment provider | `IPaymentProvider` abstraction + `FakePaymentProvider`; no real provider in MVP |
| Bookkeeping | Full double-entry against a system `ProviderSettlement` account |
| Event queue | RabbitMQ (`Aspire.RabbitMQ.Client`, raw client, no MassTransit) |
| Withdrawal model | Hold (reduce `AvailableBalance`) → settle (ledger debit) or release (restore `AvailableBalance`) |
| Schema | User's columns kept; additions listed in §3 |
| Identity | No auth; `userId` in route; wallet created explicitly or lazily |
| Reconciliation | Pending-payout sweeper + ledger invariant checker |
| Currency | `NGN` only; amounts are `long` kobo |
| Concurrency | Optimistic concurrency (`Version` token) with bounded retry |
| Test DB | Unit tests + EF Core InMemory only (no Testcontainers) |

### Out of scope

Real provider integration, authentication/authorization, multi-currency, statement-level reconciliation, fees, transaction limits/KYC, and a UI.

## 2. Architecture

```
Client ──► Payment Provider (fake) ──webhook──► Wallet API (Live.Api controllers)
Client ──► Wallet API ──► Ledger System (Live.Wallet + Postgres, incl. OutboxMessages)
Ledger ──► OutboxRelay (hosted) ──► RabbitMQ `wallet.payouts` ──► PayoutWorker (hosted) ──► Payment Provider
ReconciliationSweeper (hosted) ──► Payment Provider (status) ──► Ledger
```

### Projects

- `src/Live.Wallet` (new class library, references `Practice.Common`)
  - `Ledger/`: `Account`, `AccountType`, `LedgerTransaction`, `TransactionType`, `TransactionStatus`, `LedgerEntry`, `EntryDirection`, `Money`, `LedgerPosting` (pure posting logic), `WalletErrors`
  - `Wallets/`: create and get wallet, list transactions
  - `TopUps/`: initiate top-up and complete top-up (from the webhook)
  - `Withdrawals/`: request withdrawal (hold + outbox), settle withdrawal, release withdrawal, `WithdrawalDetails`
  - `Webhooks/`: `ProviderWebhookHandler`, which dispatches by event type
  - `Outbox/`: `OutboxMessage`, `OutboxRelay`, `IMessagePublisher`, `RabbitMqMessagePublisher`
  - `Payouts/`: `PayoutRequested` message, `PayoutProcessor` (logic), `RabbitMqPayoutConsumer` (transport)
  - `Reconciliation/`: `PendingPayoutSweeper`, `LedgerInvariantChecker`
  - `Providers/`: `IPaymentProvider`, `FakePaymentProvider`, result records
  - `Persistence/`: `WalletDbContext`, entity configurations, migrations
  - `ServiceCollectionExtensions.AddWallet(...)`
- `src/Live.Api`: `WalletsController`, `TopUpsController`, `WithdrawalsController`, `ProviderWebhooksController`, `DevProviderController` (Development only), `ResultExtensions` (Error → ProblemDetails), and hosted-service registration.
- `tests/Live.Tests`: references `Live.Wallet` and holds the unit, handler, worker and API tests.

## 3. Data model

All amounts are `long` in kobo. `Currency` is the 3-letter code `NGN`.

### Accounts
| Column | Type | Notes |
|---|---|---|
| Id | uuid PK | |
| UserId | uuid NULL | NULL for system accounts |
| Type | text | `User` \| `System` |
| Name | text NULL | System account name, e.g. `ProviderSettlement` |
| Currency | char(3) | `NGN` |
| Balance | bigint | Settled balance; equals the sum of the account's ledger entries |
| AvailableBalance | bigint | `Balance` minus active holds |
| Version | int | Optimistic concurrency token |
| CreatedAt | timestamptz | |

Constraints: unique `(UserId, Currency)` where `UserId` is not null; unique `Name` where it is not null. For `Type = User`: `CHECK (Balance >= 0 AND AvailableBalance >= 0 AND AvailableBalance <= Balance)`.

The spec keeps `AvailableBalance` as an integer type, as the user specified. All balances use `bigint` for headroom.

### Transactions
| Column | Type | Notes |
|---|---|---|
| Id | uuid PK | |
| AccountId | uuid FK → Accounts | The user wallet involved |
| Type | text | `TopUp` \| `Withdrawal` |
| Status | text | `Pending` \| `Succeeded` \| `Failed` |
| Amount | bigint | > 0 |
| ReferenceId | text UNIQUE | Client idempotency key |
| ProviderReferenceId | text UNIQUE NULL | Set when the provider assigns one |
| RequestHash | text | Hash of the request body, used to detect a reused `ReferenceId` with a different payload |
| CreatedAt | timestamptz | |
| CompletedAt | timestamptz NULL | |

### LedgerEntries (append-only)
| Column | Type | Notes |
|---|---|---|
| Id | uuid PK | |
| TransactionId | uuid FK | |
| AccountId | uuid FK | |
| Direction | text | `Debit` \| `Credit` |
| Amount | bigint | > 0 |
| Currency | char(3) | |
| BalanceBefore | bigint | The account's `Balance` before this entry |
| BalanceAfter | bigint | The account's `Balance` after this entry |
| CreatedAt | timestamptz | |

Sign convention (wallet-liability view): a **Credit increases** an account's `Balance`, and a **Debit decreases** it. This applies to every account, including system ones. `ProviderSettlement` may go negative; its negated balance equals the total money held for users.

### Withdrawals
`TransactionId` (PK/FK), `BankCode`, `AccountNumber`, `AccountName`.

### OutboxMessages
`Id` uuid PK, `Type` text, `Payload` jsonb, `OccurredAt` timestamptz, `ProcessedAt` timestamptz NULL, `Attempts` int, `LastError` text NULL. There is a partial index on `OccurredAt` where `ProcessedAt IS NULL`.

## 4. Ledger invariants (each one is test-first)

1. **I1 Balanced:** a settled transaction has exactly two entries, one Debit and one Credit, with equal amounts and the same currency.
2. **I2 Running balance:** for each entry, `BalanceAfter = BalanceBefore ± Amount`, and `BalanceBefore` equals the account's previous `BalanceAfter` (0 for the first entry).
3. **I3 Account matches entries:** `Account.Balance` equals the account's last `BalanceAfter`, which equals the sum of its credits minus its debits.
4. **I4 Non-negative user wallet:** `0 <= AvailableBalance <= Balance` for user accounts.
5. **I5 Valid money:** amount > 0, and currency equals `NGN`.
6. **I6 Global zero-sum:** the sum of all account balances is 0.
7. **I7 Write-once entries:** ledger entries are never updated or deleted, and there is no code path that does so.

## 5. Money flows

### Create wallet
`POST /users/{userId}/wallet` creates the User account if it doesn't exist and returns it either way. A top-up initiated for a user with no wallet creates the wallet lazily.

### Top-up
1. `POST /users/{userId}/top-ups { referenceId, amount }`:
   - Validate the amount.
   - Check idempotency on `referenceId`.
   - Call `IPaymentProvider.InitiateChargeAsync`, which returns a `providerReferenceId`.
   - Store a Pending `TopUp` transaction.
   - Return 201 with the transaction.
2. Webhook `charge.success { providerReferenceId, amount }`:
   - Load the transaction by `ProviderReferenceId`.
   - If it is already Succeeded, do nothing and return 200.
   - If the webhook amount doesn't match the transaction, mark it Failed and log an error.
   - Otherwise post the ledger entries: Debit `ProviderSettlement`, Credit the wallet. The wallet's `Balance` and `AvailableBalance` both go up by the amount, and the status becomes Succeeded.
3. Webhook `charge.failed`: mark the transaction Failed, with no ledger entries.

### Withdrawal
1. `POST /users/{userId}/withdrawals { referenceId, amount, bankCode, accountNumber, accountName }`:
   - Validate the request.
   - Check idempotency on `referenceId`.
   - Require `AvailableBalance >= amount`; otherwise return `Wallet.InsufficientFunds`.
   - Reduce `AvailableBalance` by the amount (this is the hold).
   - Add a Pending `Withdrawal` transaction, a `Withdrawals` row and a `PayoutRequested { transactionId }` outbox message, all in **one `SaveChangesAsync`**.
   - Return 202.
2. **Settle**, triggered by the `transfer.success` webhook or the sweeper:
   - If the transaction is already final, do nothing.
   - Otherwise post Debit wallet and Credit `ProviderSettlement`. The wallet's `Balance` goes down; `AvailableBalance` doesn't change because it already went down at hold time.
   - Set the status to Succeeded.
3. **Release**, triggered by the `transfer.failed` webhook, the sweeper, or a definite provider rejection in the worker:
   - If the transaction is already final, do nothing.
   - Otherwise add the amount back to `AvailableBalance` and set the status to Failed.
   - No ledger entries are written.

### Idempotency
- **Same `ReferenceId`, same `RequestHash`:** return the original transaction (200/202), with no side effects.
- **Same `ReferenceId`, different `RequestHash`:** return `Transaction.DuplicateReference`, which maps to 409.
- **Duplicate webhooks:** they hit the "already final" check and do nothing.

### Concurrency
- `Account.Version` is an EF concurrency token, incremented on every change.
- Each handler wraps its load → mutate → save cycle in a retry loop (at most 3 attempts). On `DbUpdateConcurrencyException` it clears the change tracker, reloads and re-evaluates. When the retries run out, it returns `Concurrency.Exhausted`, which maps to 409.
- Because the balance check is re-evaluated after every reload, two racing withdrawals can't both pass on the same funds.

## 6. Async processing

- **OutboxRelay** (`BackgroundService`):
  - Every 1s it takes up to 50 unprocessed messages, ordered by `OccurredAt`.
  - For each one it calls `IMessagePublisher.PublishAsync` (publisher confirms on), then sets `ProcessedAt`.
  - On failure it increments `Attempts`, records `LastError` and moves on.
  - Delivery is at-least-once.
- **Queue topology:** the `wallet.payouts` durable queue, with dead-letter exchange `wallet.payouts.dlx` and queue `wallet.payouts.dead`.
- **PayoutProcessor** (transport-agnostic, invoked by `RabbitMqPayoutConsumer`):
  - It loads the transaction. If it is no longer Pending, it acks and does nothing.
  - It calls `IPaymentProvider.InitiatePayoutAsync(referenceId as idempotency key, amount, bank details)`. It returns one of:
    - `Accepted(providerRef)`: store `ProviderReferenceId`, ack. The transaction stays Pending until the webhook or the sweeper finalises it.
    - `Rejected(reason)`: release the hold, ack.
    - A transient error: nack and requeue. After 5 deliveries the message goes to the dead-letter queue; the hold stays, and the sweeper deals with it.
- **PendingPayoutSweeper** (`BackgroundService`, every 5 min, driven by `IClock`):
  - It takes Pending withdrawals older than 15 min and calls `IPaymentProvider.GetPayoutStatusAsync(referenceId)`.
  - The status decides the action:
    - `Succeeded`: settle.
    - `Failed` or `NotFound`: release.
    - `Pending`: leave it.
  - Settle and release use the same handlers as the webhooks.
- **LedgerInvariantChecker:** run by the sweeper. It verifies I1, I3, I4 and I6, and logs violations via `LoggerMessage` at Error level. It never auto-corrects anything.

## 7. API and errors

| Method | Route | Success |
|---|---|---|
| POST | `/users/{userId}/wallet` | 200/201 `WalletResponse` |
| GET | `/users/{userId}/wallet` | 200 `WalletResponse { accountId, balance, availableBalance, currency }` |
| GET | `/users/{userId}/wallet/transactions` | 200 list, newest first |
| POST | `/users/{userId}/top-ups` | 201 `TransactionResponse` (includes `providerReferenceId`) |
| POST | `/users/{userId}/withdrawals` | 202 `TransactionResponse` |
| POST | `/webhooks/provider` | 200 (also for duplicates); 400 for an unknown event; 404 for an unknown reference |
| POST | `/dev/provider/{providerReferenceId}/complete?outcome=success\|failed` | Development only; synthesises the webhook |

Handlers return `Result<T>`. Controllers map `Error.Code` to RFC 7807 ProblemDetails:

| Code | HTTP |
|---|---|
| `Money.InvalidAmount`, `Money.UnsupportedCurrency`, `Validation.*` | 400 |
| `Wallet.NotFound`, `Transaction.NotFound` | 404 |
| `Transaction.DuplicateReference`, `Concurrency.Exhausted` | 409 |
| `Wallet.InsufficientFunds` | 422 |

Exceptions are reserved for genuinely unexpected failures, such as infrastructure errors.

## 8. Testing strategy (TDD)

Each behaviour gets a failing test first. The first build is written by hand, red → green → refactor.

1. **Domain unit tests** (`Ledger/`): `Money` validation; `LedgerPosting` produces balanced entries with the right running balances (I1, I2, I3); a hold can't push `AvailableBalance` below 0; a release puts back exactly the held amount; posting rejects a currency mismatch.
2. **Handler tests** (EF Core InMemory, fake provider/publisher, `FakeClock`):
   - Top-up initiate and complete. A duplicate webhook does nothing. An amount mismatch fails the transaction.
   - Withdrawal: hold + outbox message in one save. Insufficient funds. Idempotent replay, and a 409 when the same reference comes with a different body.
   - Settle and release, both idempotent, plus the webhook and the sweeper racing on the same transaction.
   - Concurrency: two handlers on separate DbContexts load the same account version. One wins; the other retries and is re-evaluated, so a double-spend is rejected.
   - Invariant checker: it detects corrupted data that a test seeds on purpose.
3. **Worker tests:** the outbox relay publishes and marks messages, and records failures. The payout processor handles Accepted, Rejected and transient errors. The sweeper respects the 15-minute threshold using `FakeClock`.
4. **API tests** (`WebApplicationFactory<Program>`, DbContext replaced with InMemory, hosted services removed): routes, status codes and the ProblemDetails shape.

**Known gap (accepted):** EF InMemory doesn't enforce unique indexes, CHECK constraints or real transactions. The Postgres migration still declares all of them, so the database backs up the application-level checks. Real-Postgres and real-RabbitMQ tests (Testcontainers) are a follow-up.

## 9. Wiring and configuration

- **Packages** (central versions): `Aspire.Npgsql.EntityFrameworkCore.PostgreSQL`, `Microsoft.EntityFrameworkCore.InMemory` (tests), `Microsoft.EntityFrameworkCore.Design` (migrations), `Aspire.RabbitMQ.Client`.
- **Registration:** `builder.AddWallet()` in `Live.Api` registers the DbContext (`appdb`), the RabbitMQ client (`messaging`), handlers, the fake provider (singleton), `IClock` and the hosted services.
- **Migrations:** an initial EF migration; `Database.MigrateAsync()` runs at startup in Development only.
- **Seeding:** the `ProviderSettlement` system account is seeded through the migration.
- **Options:** `Wallet:Currency = NGN`, `Wallet:SweeperInterval`, `Wallet:PendingThreshold`, `Wallet:OutboxBatchSize`.
- **Logging:** all logging goes through `ILogger<T>` with `LoggerMessage`; there's no `Console.WriteLine`.
