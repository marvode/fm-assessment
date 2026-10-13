# Go Port of the Wallet: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `golang/`, a Go implementation of the wallet that is behaviour-compatible with `dotnet/`. It keeps the
same HTTP contract, ledger rules and outbox → RabbitMQ → payout semantics, and its own e2e script passes the same
checks.

**Architecture:** Vertical slices under `internal/ledger/` mirror `Wallet.Ledger`. A small unit of work
(`store.Tx` + `store.Run`/`store.WithRetry`) reproduces EF Core's change tracking and optimistic concurrency on top of
pgx and sqlc. The HTTP layer is the stdlib `ServeMux`, and background workers run under one `errgroup` in
`internal/app`.

**Tech Stack:**
- Go 1.25+
- `net/http`
- pgx/v5 + sqlc 1.29.0 (run via Docker)
- goose v3 (embedded SQL)
- amqp091-go
- google/uuid
- caarlos0/env v11
- log/slog
- errgroup
- testify + testcontainers-go (postgres, rabbitmq)

**Spec:** `docs/superpowers/specs/2026-10-13-golang-port-design.md`

## Global Constraints

- **Code location:** everything lives under `golang/` at the repo root. Module path `wallet`, `go 1.25` in `go.mod`.
- **Dependencies:** no web framework and no ORM.
  - Allowed: stdlib, `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/rabbitmq/amqp091-go`,
    `github.com/google/uuid`, `github.com/caarlos0/env/v11`, `golang.org/x/sync`.
  - Tests may also use `github.com/stretchr/testify` and `github.com/testcontainers/testcontainers-go` (plus its
    `modules/postgres` and `modules/rabbitmq`).
- **Money and currency:** money is `int64` kobo, and the currency is always `"NGN"`.
- **Error codes:** they match .NET exactly: `Money.InvalidAmount`, `Money.UnsupportedCurrency`, `Wallet.NotFound`,
  `Wallet.InsufficientFunds`, `Transaction.NotFound`, `Transaction.DuplicateReference`, `Concurrency.Exhausted`,
  `Ledger.HoldMissing`, `Ledger.SameAccount`, `Validation.<Field>`.
- **HTTP statuses:**

  | Error code | Status |
  |---|---|
  | `Money.*`, `Validation.*` | 400 |
  | `*.NotFound` | 404 |
  | `Transaction.DuplicateReference`, `Concurrency.Exhausted`, `Ledger.*` | 409 |
  | `Wallet.InsufficientFunds` | 422 |
  | anything else | 500 |
- **Endpoint success statuses:**
  - wallet create: 201, or 200 if it already exists;
  - top-up: 201, or 200 on replay;
  - withdrawal: **202**, or 200 on replay;
  - webhook: 200;
  - `/alive`: 200 `Healthy`;
  - `/health`: 200 `Healthy` or 503 `Unhealthy`, text/plain, in every environment;
  - there is no `/` route.
- **Dev route:** `/dev/provider/{providerReferenceId}/complete` exists only when `APP_ENV=development`.
- **Worker defaults:** outbox poll 1s, outbox batch 50, sweeper interval 5m, pending threshold 15m, read from
  `WALLET_OUTBOX_POLL_INTERVAL`, `WALLET_OUTBOX_BATCH_SIZE`, `WALLET_SWEEPER_INTERVAL`, `WALLET_PENDING_THRESHOLD`.
- **RabbitMQ names:**
  - queue `wallet.payouts`: quorum, `x-delivery-limit` 5, `x-dead-letter-exchange` `wallet.payouts.dlx`;
  - dead-letter exchange `wallet.payouts.dlx`: fanout;
  - dead-letter queue `wallet.payouts.dead`;
  - message type `wallet.payout-requested`.
- **Logging:** `log/slog` with the JSON handler in production. These messages are grepped by `scripts/e2e.sh` and
  must be exact:
  - `payout sweep`, with attrs in the order `settled`, `released`, `redriven`, `still_pending`, `errors`;
  - `provider lost accepted payout; resubmitting it`;
  - `ledger invariant violated`;
  - `unhandled error`.
- **Tests:**
  - All tests are external test packages (`package foo_test`), so test helpers in `internal/ledger/ledgertest` can
    import any slice without cycles.
  - Tests that need Postgres or RabbitMQ start containers through `ledgertest`, which requires Docker.
  - Setting `WALLET_TEST_DATABASE_URL` / `WALLET_TEST_RABBITMQ_URL` reuses existing servers instead; run with
    `go test -p 1 ./...` in that mode.
- **Commits:** one commit per task; each commit message ends with the attribution trailer this session requires. Work
  on a feature branch (for example `golang-port`), not `master`.
- **Formatting:** run `gofmt -w` on every Go file you write. `make lint` must pass at the end.

## Review Focus

These are the five failure modes the spec implies that no ported .NET test exercises, most likely first. Each one has
a test in the task named.

1. **Malformed, null, wrong-typed or missing JSON fields** (for example `{"amount":"lots"}` or `{"amount":5}` with no
   `referenceId`) must return a 400 problem with a `Validation.*` title, never a 500. Covered in Task 14:
   `TestBadBodiesAreValidationProblems`.
2. **A request body over 1 MiB** must return 400 `Validation.Body` and must not be read into memory. Covered in Task
   14: `TestAnOversizedBodyIsRejected`.
3. **RabbitMQ unreachable at startup or mid-run:**
   - the API still serves `/alive` and accepts withdrawals (202), and `/health` returns 503 `Unhealthy`;
   - publishing recovers after the connection drops.

   Covered in Task 13 (`TestRabbitPublisherRecoversAfterTheConnectionDrops`) and Task 15
   (`TestTheAPIServesWhileRabbitMQIsDown`).
4. **Shutdown (SIGTERM or context cancel) while running:** `App.Run` returns nil promptly, and a delivery whose
   processing was cut short is neither acked nor nacked, so RabbitMQ redelivers it. Covered in Task 13
   (`TestHandleLeavesADeliveryUnsettledWhenShuttingDown`) and Task 15 (`TestRunServesAndShutsDownCleanly`).
5. **A panic inside a handler** (for example the domain's "already final" panic) returns a 500 problem, is logged as
   `unhandled error`, and the server keeps serving. Covered in Task 14: `TestRecoverPanicsReturnsA500Problem`.

## File Map

```
golang/
  go.mod, go.sum, Makefile, compose.yaml, sqlc.yaml, .gitignore, AGENTS.md
  cmd/wallet-api/main.go                      Task 15  env config, signals, `migrate` subcommand, app.Run
  internal/common/
    error.go                                  Task 1   Error{Code,Message}, NewError, CodeOf
    clock.go                                  Task 1   Clock, SystemClock, FakeClock
    every.go                                  Task 1   RunEvery (PeriodicTimer loop)
  internal/config/config.go                   Task 14  Config + Load()
  internal/rabbit/conn.go                     Task 13  re-dialling connection
  internal/ledger/
    domain/                                   Task 2   types, errors, Account, Transaction, Entry+Post, RequestHash, limits
    store/
      migrations/00001_init.sql               Task 3   schema + seed
      queries/*.sql                           Task 3   every query sqlc generates
      db/ (generated)                         Task 3
      store.go, migrate.go                    Task 3   Store, New, Pool, Queries, Migrate
      tx.go, run.go, mapping.go, errors.go    Task 4   unit of work, Run, WithRetry, ErrConflict
      reads.go, outbox.go                     Task 4   read helpers, OutboxMessage + relay queries
    ledgertest/
      postgres.go                             Task 3   NewPool/NewStore (testcontainers or env URL)
      seed.go                                 Task 4   Start, Logger, SeedWallet, SeedTopUp, getters, Count, Times
      scenarios.go                            Task 8   WithdrawalCommand, PendingWithdrawal, AssignProviderReference
      rabbitmq.go                             Task 13  RabbitMQ (container or env URL), queue helpers
    providers/                                Task 5   PaymentProvider, keys, FakePaymentProvider
    wallets/                                  Task 6   responses, GetOrCreate, FindReplay, Create/Get/List handlers
    topups/                                   Task 7   InitiateHandler, CompleteHandler
    messages/payout_requested.go              Task 8   PayoutRequested + NewPayoutRequested
    withdrawals/                              Task 8   RequestHandler, SettleHandler, ReleaseHandler
    webhooks/handler.go                       Task 9   Webhook, Handler
    outbox/relay.go, publisher.go             Task 10  Publisher, Relay
    outbox/rabbit_publisher.go                Task 13  RabbitPublisher (confirms + returns)
    payouts/processor.go                      Task 11  Processor, Outcome
    payouts/topology.go, consumer.go          Task 13  DeclareTopology, Consumer
    reconciliation/                           Task 12  Sweeper, Checker, RunOnce
  internal/api/                               Task 14  router, problem, decode, handlers, health, dev, recover
  internal/app/app.go                         Task 15  composition root + Run
  scripts/e2e.sh                              Task 16  port of dotnet/scripts/e2e.sh
```

---

### Task 1: Module scaffold and `common`

**Files:**
- Create: `golang/go.mod`, `golang/.gitignore`, `golang/Makefile`, `golang/compose.yaml`
- Create: `golang/internal/common/error.go`, `golang/internal/common/clock.go`, `golang/internal/common/every.go`
- Test: `golang/internal/common/common_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Error struct{ Code, Message string }`, `func NewError(code, message string) *Error`.
    `(*Error).Is` matches by `Code`.
  - `func CodeOf(err error) string`
  - `type Clock interface{ Now() time.Time }`
  - `type SystemClock struct{}`: its `Now` returns UTC truncated to microseconds.
  - `type FakeClock`, `func NewFakeClock(start time.Time) *FakeClock`, `(*FakeClock).Now()`,
    `(*FakeClock).Advance(d time.Duration)`
  - `func RunEvery(ctx context.Context, interval time.Duration, run func(context.Context) error, onError func(error)) error`

- [ ] **Step 1: Check the toolchain and create the module**

Run: `go version`
Expected: `go version go1.25` or newer. If Go is missing or older, stop and ask the user to install Go 1.25+.

```bash
mkdir -p golang/internal/common
cd golang
go mod init wallet
go mod edit -go=1.25
go get github.com/stretchr/testify@latest
```

Create `golang/.gitignore`:

```gitignore
/bin/
```

Create `golang/compose.yaml`:

```yaml
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: wallet
    ports: ["5432:5432"]
  rabbitmq:
    image: rabbitmq:4-management
    ports: ["5672:5672", "15672:15672"]
```

Create `golang/Makefile`. Recipe lines **must start with a tab**:

```make
SQLC_IMAGE := sqlc/sqlc:1.29.0
DEV_ENV := APP_ENV=development \
	DATABASE_URL=postgres://postgres:postgres@localhost:5432/wallet?sslmode=disable \
	RABBITMQ_URL=amqp://guest:guest@localhost:5672/

.PHONY: generate test lint run up down e2e

generate:
	docker run --rm -u $$(id -u):$$(id -g) -v $(CURDIR):/src -w /src $(SQLC_IMAGE) generate

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	if command -v golangci-lint >/dev/null; then golangci-lint run; fi

run:
	$(DEV_ENV) go run ./cmd/wallet-api

up:
	docker compose up -d

down:
	docker compose down

e2e:
	scripts/e2e.sh
```

- [ ] **Step 2: Write the failing tests**

Create `golang/internal/common/common_test.go`:

```go
package common_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/common"
)

func TestFakeClockAdvanceMovesTimeForwardByExactlyTheGivenAmount(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := common.NewFakeClock(start)

	clock.Advance(5 * time.Minute)

	require.Equal(t, start.Add(5*time.Minute), clock.Now())
}

func TestSystemClockIsUTCAtMicrosecondPrecision(t *testing.T) {
	now := common.SystemClock{}.Now()

	require.Equal(t, time.UTC, now.Location())
	require.Zero(t, now.Nanosecond()%1000)
}

func TestErrorsMatchByCode(t *testing.T) {
	sentinel := common.NewError("Wallet.NotFound", "The user has no wallet.")
	wrapped := fmt.Errorf("loading: %w", common.NewError("Wallet.NotFound", "other text"))

	require.ErrorIs(t, wrapped, sentinel)
	require.NotErrorIs(t, wrapped, common.NewError("Wallet.InsufficientFunds", "x"))
	require.Equal(t, "Wallet.NotFound", common.CodeOf(wrapped))
	require.Equal(t, "", common.CodeOf(errors.New("plain")))
	require.Equal(t, "Wallet.NotFound: The user has no wallet.", sentinel.Error())
}

func TestRunEveryKeepsRunningAfterAFailureAndStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs, failures atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- common.RunEvery(ctx, time.Millisecond, func(context.Context) error {
			if runs.Add(1) == 1 {
				return errors.New("boom")
			}
			return nil
		}, func(error) { failures.Add(1) })
	}()

	require.Eventually(t, func() bool { return runs.Load() >= 3 }, time.Second, time.Millisecond)
	cancel()

	require.NoError(t, <-done)
	require.Equal(t, int32(1), failures.Load())
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/common/...`
Expected: FAIL to compile, with `undefined: common.NewFakeClock` (and similar).

- [ ] **Step 4: Implement `common`**

Create `golang/internal/common/error.go`:

```go
// Package common holds the small types every wallet package shares.
package common

import "errors"

// Error is a failure the caller is expected to handle: a stable code plus a human-readable message.
// The API maps codes to HTTP statuses. Two Errors match under errors.Is when their codes are equal.
type Error struct {
	Code    string
	Message string
}

func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// CodeOf returns the code of the first *Error in err's chain, or "" when there is none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
```

Create `golang/internal/common/clock.go`:

```go
package common

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock. Times are UTC and truncated to microseconds, the precision Postgres stores,
// so a value read back from the database equals the one written.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// FakeClock is a clock tests move by hand, so expiry is tested without sleeping.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start.UTC()} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
```

Create `golang/internal/common/every.go`:

```go
package common

import (
	"context"
	"time"
)

// RunEvery calls run once per interval until ctx ends, like .NET's PeriodicTimer loop: the first run is one interval
// after the start. A failed run is reported to onError and never stops the loop. It returns nil when ctx ends.
func RunEvery(ctx context.Context, interval time.Duration, run func(context.Context) error, onError func(error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := run(ctx); err != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go test ./internal/common/...`
Expected: `ok  	wallet/internal/common`

- [ ] **Step 6: Commit**

```bash
git add golang
git commit -m "feat(golang): scaffold Go module with shared error, clock and ticker"
```

---

### Task 2: Domain model

**Files:**
- Create in `golang/internal/ledger/domain/`: `types.go`, `errors.go`, `account.go`, `transaction.go`, `entry.go`,
  `withdrawal.go`, `requesthash.go`
- Test in `golang/internal/ledger/domain/`: `account_test.go`, `posting_test.go`, `transaction_test.go`,
  `requesthash_test.go`

**Interfaces:**
- Consumes: `common.Error`, `common.NewError`.
- Produces (package `domain`):
  - **Constants:** `Currency = "NGN"`.
  - **Typed string enums:**
    - `AccountType`: `AccountUser`, `AccountSystem`
    - `TransactionType`: `TopUp`, `Withdrawal`
    - `TransactionStatus`: `Pending`, `Succeeded`, `Failed`
    - `Direction`: `Debit`, `Credit`
  - **Limits:** `ReferenceIDMaxLength = 128`, `BankCodeMaxLength = 16`, `AccountNumberLength = 10`,
    `AccountNameMaxLength = 128`, `func BlankOrLonger(s string, max int) bool`.
  - **Settlement account:** `ProviderSettlementID uuid.UUID` (`00000000-0000-0000-0000-000000000001`) and
    `ProviderSettlementName = "ProviderSettlement"`.
  - **Errors** (all `*common.Error`): `ErrInvalidAmount`, `ErrUnsupportedCurrency`, `ErrWalletNotFound`,
    `ErrInsufficientFunds`, `ErrTransactionNotFound`, `ErrDuplicateReference`, `ErrConcurrencyExhausted`,
    `ErrHoldMissing`, `ErrSameAccount`, `ErrUnknownWebhookEvent`, and
    `func Validation(field, message string) *common.Error`.
  - **Account:**
    - `type Account struct{ ID uuid.UUID; UserID *uuid.UUID; Type AccountType; Name *string; Currency string; Balance, AvailableBalance, Version int64; CreatedAt time.Time }`
    - `OpenUserWallet(userID uuid.UUID, now time.Time) *Account`, `OpenSystem(id uuid.UUID, name string, now time.Time) *Account`
    - `(*Account).PlaceHold(amount int64) (int64, error)`, `ReleaseHold(amount int64) (int64, error)`,
      `CanDebit(amount int64, fromHold bool) error`, `Credit(transactionID uuid.UUID, amount int64, now time.Time) Entry`,
      `Debit(transactionID uuid.UUID, amount int64, fromHold bool, now time.Time) Entry`
  - **Transaction:**
    - `type Transaction struct{ ID, AccountID uuid.UUID; Type TransactionType; Status TransactionStatus; Amount int64; ReferenceID string; ProviderReferenceID *string; RequestHash string; CreatedAt time.Time; CompletedAt *time.Time }`
    - `StartTopUp(accountID uuid.UUID, amount int64, referenceID, requestHash, providerReferenceID string, now time.Time) *Transaction`
    - `StartWithdrawal(accountID uuid.UUID, amount int64, referenceID, requestHash string, now time.Time) *Transaction`
    - `IsFinal() bool`, `AssignProviderReference(ref string)`, `ClearProviderReference()`, `MarkSucceeded(now)`,
      `MarkFailed(now)`. The `Mark*` methods panic when the transaction is already final.
  - **Entry:** `type Entry struct{ ID, TransactionID, AccountID uuid.UUID; Direction Direction; Amount int64; Currency string; BalanceBefore, BalanceAfter int64; CreatedAt time.Time }`
  - **Posting:** `func Post(t *Transaction, debitAccount, creditAccount *Account, debitFromHold bool, now time.Time) ([]Entry, error)`
  - **WithdrawalDetails:** `type WithdrawalDetails struct{ TransactionID uuid.UUID; BankCode, AccountNumber, AccountName string }`
  - **RequestHash:** `func RequestHash(parts ...any) string` (SHA-256 of the parts joined by `|`, as uppercase hex)

- [ ] **Step 1: Add the uuid dependency**

```bash
cd golang && mkdir -p internal/ledger/domain && go get github.com/google/uuid@latest
```

- [ ] **Step 2: Write the failing tests**

Create `golang/internal/ledger/domain/account_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
)

var now = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func funded(t *testing.T, amount int64) *domain.Account {
	t.Helper()
	account := domain.OpenUserWallet(uuid.New(), now)
	account.Credit(uuid.New(), amount, now)
	return account
}

func TestOpenUserWalletStartsEmptyInNGN(t *testing.T) {
	userID := uuid.New()

	account := domain.OpenUserWallet(userID, now)

	require.Equal(t, userID, *account.UserID)
	require.Equal(t, domain.AccountUser, account.Type)
	require.Equal(t, "NGN", account.Currency)
	require.Zero(t, account.Balance)
	require.Zero(t, account.AvailableBalance)
	require.Equal(t, now, account.CreatedAt)
}

func TestCreditRaisesBothBalancesAndRecordsTheRunningBalance(t *testing.T) {
	account := domain.OpenUserWallet(uuid.New(), now)
	transactionID := uuid.New()

	entry := account.Credit(transactionID, 500, now)

	require.EqualValues(t, 500, account.Balance)
	require.EqualValues(t, 500, account.AvailableBalance)
	require.Equal(t, domain.Credit, entry.Direction)
	require.Equal(t, transactionID, entry.TransactionID)
	require.Equal(t, account.ID, entry.AccountID)
	require.EqualValues(t, 500, entry.Amount)
	require.Equal(t, "NGN", entry.Currency)
	require.Zero(t, entry.BalanceBefore)
	require.EqualValues(t, 500, entry.BalanceAfter)
}

func TestPlaceHoldReducesOnlyTheAvailableBalance(t *testing.T) {
	account := funded(t, 500)

	_, err := account.PlaceHold(200)

	require.NoError(t, err)
	require.EqualValues(t, 300, account.AvailableBalance)
	require.EqualValues(t, 500, account.Balance)
}

func TestPlaceHoldForTheEntireAvailableBalanceSucceeds(t *testing.T) {
	account := funded(t, 500)

	_, err := account.PlaceHold(500)

	require.NoError(t, err)
	require.Zero(t, account.AvailableBalance)
}

func TestPlaceHoldBeyondTheAvailableBalanceFailsAndChangesNothing(t *testing.T) {
	account := funded(t, 100)
	versionBefore := account.Version

	_, err := account.PlaceHold(101)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 100, account.AvailableBalance)
	require.Equal(t, versionBefore, account.Version)
}

func TestPlaceHoldRejectsNonPositiveAmounts(t *testing.T) {
	for _, amount := range []int64{0, -1} {
		_, err := funded(t, 100).PlaceHold(amount)
		require.Equal(t, "Money.InvalidAmount", common.CodeOf(err), "amount %d", amount)
	}
}

func TestReleaseHoldRestoresExactlyTheHeldAmount(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(200)
	require.NoError(t, err)

	_, err = account.ReleaseHold(200)

	require.NoError(t, err)
	require.EqualValues(t, 500, account.AvailableBalance)
	require.EqualValues(t, 500, account.Balance)
}

func TestReleaseHoldWithoutAMatchingHoldFails(t *testing.T) {
	account := funded(t, 500)

	_, err := account.ReleaseHold(1)

	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(err))
	require.EqualValues(t, 500, account.AvailableBalance)
}

func TestDebitFromAHoldLowersTheBalanceWithoutLoweringAvailableAgain(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(200)
	require.NoError(t, err)

	entry := account.Debit(uuid.New(), 200, true, now)

	require.EqualValues(t, 300, account.Balance)
	require.EqualValues(t, 300, account.AvailableBalance)
	require.Equal(t, domain.Debit, entry.Direction)
	require.EqualValues(t, 500, entry.BalanceBefore)
	require.EqualValues(t, 300, entry.BalanceAfter)
}

func TestCanDebitWithoutAHoldRequiresAvailableFunds(t *testing.T) {
	account := funded(t, 500)
	_, err := account.PlaceHold(400)
	require.NoError(t, err)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(account.CanDebit(200, false)))
}

func TestCanDebitFromAHoldRequiresAMatchingHold(t *testing.T) {
	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(funded(t, 500).CanDebit(200, true)))
}

func TestASystemAccountCanBeDebitedBelowZero(t *testing.T) {
	settlement := domain.OpenSystem(domain.ProviderSettlementID, domain.ProviderSettlementName, now)

	require.NoError(t, settlement.CanDebit(1_000, false))
	settlement.Debit(uuid.New(), 1_000, false, now)

	require.EqualValues(t, -1_000, settlement.Balance)
	require.Equal(t, domain.AccountSystem, settlement.Type)
	require.Nil(t, settlement.UserID)
}

func TestEveryChangeBumpsTheVersion(t *testing.T) {
	account := funded(t, 500)
	versionBefore := account.Version

	_, err := account.PlaceHold(100)

	require.NoError(t, err)
	require.Equal(t, versionBefore+1, account.Version)
}
```

Create `golang/internal/ledger/domain/posting_test.go`:

```go
package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
)

func settlement() *domain.Account {
	return domain.OpenSystem(domain.ProviderSettlementID, domain.ProviderSettlementName, now)
}

func wallet() *domain.Account { return domain.OpenUserWallet(uuid.New(), now) }

func fund(t *testing.T, w, s *domain.Account, amount int64) []domain.Entry {
	t.Helper()
	topUp := domain.StartTopUp(w.ID, amount, "ref-"+uuid.NewString(), "hash", "chg_"+uuid.NewString(), now)
	entries, err := domain.Post(topUp, s, w, false, now)
	require.NoError(t, err)
	return entries
}

func single(t *testing.T, entries []domain.Entry, direction domain.Direction) domain.Entry {
	t.Helper()
	var found []domain.Entry
	for _, e := range entries {
		if e.Direction == direction {
			found = append(found, e)
		}
	}
	require.Len(t, found, 1)
	return found[0]
}

func TestATopUpPostingIsBalancedAndMovesMoneyFromSettlementToTheWallet(t *testing.T) {
	s, w := settlement(), wallet()
	topUp := domain.StartTopUp(w.ID, 500, "ref-1", "hash", "chg_1", now)

	entries, err := domain.Post(topUp, s, w, false, now)

	require.NoError(t, err)
	require.Len(t, entries, 2)
	debit, credit := single(t, entries, domain.Debit), single(t, entries, domain.Credit)
	require.Equal(t, s.ID, debit.AccountID)
	require.Equal(t, w.ID, credit.AccountID)
	for _, e := range entries {
		require.EqualValues(t, 500, e.Amount)
		require.Equal(t, topUp.ID, e.TransactionID)
		require.Equal(t, "NGN", e.Currency)
	}
	require.EqualValues(t, 500, w.Balance)
	require.EqualValues(t, 500, w.AvailableBalance)
	require.EqualValues(t, -500, s.Balance)
	require.Zero(t, w.Balance+s.Balance)
}

func TestConsecutivePostingsChainRunningBalances(t *testing.T) {
	s, w := settlement(), wallet()

	first := fund(t, w, s, 500)
	second := fund(t, w, s, 300)

	require.Equal(t, single(t, first, domain.Credit).BalanceAfter, single(t, second, domain.Credit).BalanceBefore)
	require.EqualValues(t, 800, single(t, second, domain.Credit).BalanceAfter)
	require.EqualValues(t, -500, single(t, second, domain.Debit).BalanceBefore)
	require.EqualValues(t, -800, single(t, second, domain.Debit).BalanceAfter)
}

func TestSettlingAWithdrawalConsumesTheHold(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 1_000)
	_, err := w.PlaceHold(400)
	require.NoError(t, err)
	withdrawal := domain.StartWithdrawal(w.ID, 400, "wd-1", "hash", now)

	_, err = domain.Post(withdrawal, w, s, true, now)

	require.NoError(t, err)
	require.EqualValues(t, 600, w.Balance)
	require.EqualValues(t, 600, w.AvailableBalance)
	require.EqualValues(t, -600, s.Balance)
}

func TestADebitWithoutFundsFailsAndLeavesBothAccountsUntouched(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 100)
	walletVersion, settlementVersion := w.Version, s.Version
	withdrawal := domain.StartWithdrawal(w.ID, 150, "wd-1", "hash", now)

	_, err := domain.Post(withdrawal, w, s, false, now)

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 100, w.Balance)
	require.EqualValues(t, 100, w.AvailableBalance)
	require.EqualValues(t, -100, s.Balance)
	require.Equal(t, walletVersion, w.Version)
	require.Equal(t, settlementVersion, s.Version)
}

func TestSettlingWithoutAHoldFails(t *testing.T) {
	s, w := settlement(), wallet()
	fund(t, w, s, 1_000)
	withdrawal := domain.StartWithdrawal(w.ID, 400, "wd-1", "hash", now)

	_, err := domain.Post(withdrawal, w, s, true, now)

	require.Equal(t, "Ledger.HoldMissing", common.CodeOf(err))
}

func TestPostingToTheSameAccountIsRejected(t *testing.T) {
	w := wallet()
	topUp := domain.StartTopUp(w.ID, 10, "ref-1", "hash", "chg_1", now)

	_, err := domain.Post(topUp, w, w, false, now)

	require.Equal(t, "Ledger.SameAccount", common.CodeOf(err))
}
```

Create `golang/internal/ledger/domain/transaction_test.go`:

```go
package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
)

func TestANewWithdrawalIsPendingWithoutAProviderReference(t *testing.T) {
	accountID := uuid.New()

	withdrawal := domain.StartWithdrawal(accountID, 400, "wd-1", "hash", now)

	require.Equal(t, domain.Withdrawal, withdrawal.Type)
	require.Equal(t, domain.Pending, withdrawal.Status)
	require.False(t, withdrawal.IsFinal())
	require.Equal(t, accountID, withdrawal.AccountID)
	require.EqualValues(t, 400, withdrawal.Amount)
	require.Equal(t, "wd-1", withdrawal.ReferenceID)
	require.Equal(t, "hash", withdrawal.RequestHash)
	require.Nil(t, withdrawal.ProviderReferenceID)
	require.Equal(t, now, withdrawal.CreatedAt)
	require.Nil(t, withdrawal.CompletedAt)
}

func TestANewTopUpCarriesTheProviderReference(t *testing.T) {
	topUp := domain.StartTopUp(uuid.New(), 500, "ref-1", "hash", "chg_1", now)

	require.Equal(t, "chg_1", *topUp.ProviderReferenceID)
}

func TestATransactionCanBeFinalisedOnlyOnce(t *testing.T) {
	withdrawal := domain.StartWithdrawal(uuid.New(), 400, "wd-1", "hash", now)
	later := now.Add(time.Minute)

	withdrawal.MarkSucceeded(later)

	require.Equal(t, domain.Succeeded, withdrawal.Status)
	require.True(t, withdrawal.IsFinal())
	require.Equal(t, later, *withdrawal.CompletedAt)
	require.Panics(t, func() { withdrawal.MarkFailed(later) })
}
```

Create `golang/internal/ledger/domain/requesthash_test.go`:

```go
package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
)

func TestEqualRequestsHashEqually(t *testing.T) {
	userID := uuid.New()

	require.Equal(t, domain.RequestHash("Withdrawal", userID, int64(400)), domain.RequestHash("Withdrawal", userID, int64(400)))
}

func TestAnyDifferenceChangesTheHash(t *testing.T) {
	userID := uuid.New()

	require.NotEqual(t, domain.RequestHash("Withdrawal", userID, int64(400)), domain.RequestHash("Withdrawal", userID, int64(401)))
	require.NotEqual(t, domain.RequestHash("a", "b"), domain.RequestHash("b", "a"))
	require.Len(t, domain.RequestHash("a"), 64)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/domain/...`
Expected: FAIL to compile, with `undefined: domain.OpenUserWallet`.

- [ ] **Step 4: Implement the domain**

Create `golang/internal/ledger/domain/types.go`:

```go
// Package domain is the wallet's ledger model. It does no I/O: handlers load entities through store.Tx, call these
// methods, and store.Run persists the result.
package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Currency is the only currency the wallet handles. Amounts are integer kobo.
const Currency = "NGN"

type AccountType string

const (
	AccountUser   AccountType = "User"
	AccountSystem AccountType = "System"
)

type TransactionType string

const (
	TopUp      TransactionType = "TopUp"
	Withdrawal TransactionType = "Withdrawal"
)

type TransactionStatus string

const (
	Pending   TransactionStatus = "Pending"
	Succeeded TransactionStatus = "Succeeded"
	Failed    TransactionStatus = "Failed"
)

// Direction: Credit raises an account's balance, Debit lowers it, for every account type.
type Direction string

const (
	Debit  Direction = "Debit"
	Credit Direction = "Credit"
)

// Column sizes, shared by validation and the schema so oversized input is a 400, never a database error.
const (
	ReferenceIDMaxLength = 128
	BankCodeMaxLength    = 16
	AccountNumberLength  = 10
	AccountNameMaxLength = 128
)

// ProviderSettlementID is the seeded system account that mirrors money held at the payment provider.
var ProviderSettlementID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

const ProviderSettlementName = "ProviderSettlement"

// BlankOrLonger reports whether s is empty, whitespace only, or longer than max characters.
func BlankOrLonger(s string, max int) bool {
	return strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
```

Create `golang/internal/ledger/domain/errors.go`:

```go
package domain

import "wallet/internal/common"

// Every expected wallet failure. Codes are stable: the API maps them to HTTP statuses.
var (
	ErrInvalidAmount        = common.NewError("Money.InvalidAmount", "Amount must be a positive number of kobo.")
	ErrUnsupportedCurrency  = common.NewError("Money.UnsupportedCurrency", "Only NGN is supported.")
	ErrWalletNotFound       = common.NewError("Wallet.NotFound", "The user has no wallet.")
	ErrInsufficientFunds    = common.NewError("Wallet.InsufficientFunds", "The available balance is too low.")
	ErrTransactionNotFound  = common.NewError("Transaction.NotFound", "No matching transaction.")
	ErrDuplicateReference   = common.NewError("Transaction.DuplicateReference", "This referenceId was already used for a different request.")
	ErrConcurrencyExhausted = common.NewError("Concurrency.Exhausted", "The wallet kept changing concurrently; retry the request.")
	ErrHoldMissing          = common.NewError("Ledger.HoldMissing", "There is no matching hold for this amount.")
	ErrSameAccount          = common.NewError("Ledger.SameAccount", "A posting needs two different accounts.")
	ErrUnknownWebhookEvent  = common.NewError("Validation.Event", "Unknown webhook event.")
)

func Validation(field, message string) *common.Error {
	return common.NewError("Validation."+field, message)
}
```

Create `golang/internal/ledger/domain/account.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Account is a balance holder. Balance is settled money and always equals the sum of the account's ledger entries.
// AvailableBalance is Balance minus active withdrawal holds. Version is the optimistic-concurrency token: every
// change bumps it, and store.Run only saves an account whose stored Version is still the one it loaded.
type Account struct {
	ID               uuid.UUID
	UserID           *uuid.UUID
	Type             AccountType
	Name             *string
	Currency         string
	Balance          int64
	AvailableBalance int64
	Version          int64
	CreatedAt        time.Time
}

func OpenUserWallet(userID uuid.UUID, now time.Time) *Account {
	return &Account{ID: newID(), UserID: &userID, Type: AccountUser, Currency: Currency, CreatedAt: now}
}

func OpenSystem(id uuid.UUID, name string, now time.Time) *Account {
	return &Account{ID: id, Name: &name, Type: AccountSystem, Currency: Currency, CreatedAt: now}
}

func (a *Account) PlaceHold(amount int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidAmount
	}
	if a.AvailableBalance < amount {
		return 0, ErrInsufficientFunds
	}
	a.AvailableBalance -= amount
	a.Version++
	return a.AvailableBalance, nil
}

func (a *Account) ReleaseHold(amount int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidAmount
	}
	if a.AvailableBalance+amount > a.Balance {
		return 0, ErrHoldMissing
	}
	a.AvailableBalance += amount
	a.Version++
	return a.AvailableBalance, nil
}

// CanDebit: system accounts may go negative. A user wallet needs available funds, or, when fromHold is true, a hold
// covering the amount.
func (a *Account) CanDebit(amount int64, fromHold bool) error {
	if a.Type == AccountSystem {
		return nil
	}
	if fromHold {
		if a.Balance-amount >= a.AvailableBalance {
			return nil
		}
		return ErrHoldMissing
	}
	if a.AvailableBalance >= amount {
		return nil
	}
	return ErrInsufficientFunds
}

func (a *Account) Credit(transactionID uuid.UUID, amount int64, now time.Time) Entry {
	before := a.Balance
	a.Balance += amount
	a.AvailableBalance += amount
	a.Version++
	return newEntry(transactionID, a.ID, Credit, amount, a.Currency, before, a.Balance, now)
}

// Debit lowers the balance. When fromHold is true, AvailableBalance already dropped when the hold was placed, so
// only Balance moves.
func (a *Account) Debit(transactionID uuid.UUID, amount int64, fromHold bool, now time.Time) Entry {
	before := a.Balance
	a.Balance -= amount
	if !fromHold {
		a.AvailableBalance -= amount
	}
	a.Version++
	return newEntry(transactionID, a.ID, Debit, amount, a.Currency, before, a.Balance, now)
}
```

Create `golang/internal/ledger/domain/transaction.go`:

```go
package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Transaction is one money movement (a top-up or a withdrawal) and its lifecycle.
type Transaction struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Type      TransactionType
	Status    TransactionStatus
	Amount    int64
	// ReferenceID is the client's idempotency key, unique per wallet.
	ReferenceID         string
	ProviderReferenceID *string
	// RequestHash fingerprints the original request, to tell a safe retry from a reused ReferenceID.
	RequestHash string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

func StartTopUp(accountID uuid.UUID, amount int64, referenceID, requestHash, providerReferenceID string, now time.Time) *Transaction {
	t := start(TopUp, accountID, amount, referenceID, requestHash, now)
	t.ProviderReferenceID = &providerReferenceID
	return t
}

func StartWithdrawal(accountID uuid.UUID, amount int64, referenceID, requestHash string, now time.Time) *Transaction {
	return start(Withdrawal, accountID, amount, referenceID, requestHash, now)
}

func start(typ TransactionType, accountID uuid.UUID, amount int64, referenceID, requestHash string, now time.Time) *Transaction {
	return &Transaction{
		ID:          newID(),
		AccountID:   accountID,
		Type:        typ,
		Status:      Pending,
		Amount:      amount,
		ReferenceID: referenceID,
		RequestHash: requestHash,
		CreatedAt:   now,
	}
}

func (t *Transaction) IsFinal() bool { return t.Status != Pending }

func (t *Transaction) AssignProviderReference(providerReferenceID string) {
	t.ProviderReferenceID = &providerReferenceID
}

// ClearProviderReference forgets a provider reference the provider no longer recognises, so the payout can be
// submitted again.
func (t *Transaction) ClearProviderReference() { t.ProviderReferenceID = nil }

func (t *Transaction) MarkSucceeded(now time.Time) { t.complete(Succeeded, now) }

func (t *Transaction) MarkFailed(now time.Time) { t.complete(Failed, now) }

func (t *Transaction) complete(status TransactionStatus, now time.Time) {
	if t.IsFinal() {
		// Handlers check IsFinal first; reaching this is a bug, not an expected failure.
		panic(fmt.Sprintf("transaction %s is already %s", t.ID, t.Status))
	}
	t.Status = status
	t.CompletedAt = &now
}
```

Create `golang/internal/ledger/domain/entry.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Entry is one side of a posting. Append-only: entries are never updated or deleted.
type Entry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Direction     Direction
	Amount        int64
	Currency      string
	BalanceBefore int64
	BalanceAfter  int64
	CreatedAt     time.Time
}

func newEntry(transactionID, accountID uuid.UUID, direction Direction, amount int64, currency string, before, after int64, now time.Time) Entry {
	return Entry{
		ID:            newID(),
		TransactionID: transactionID,
		AccountID:     accountID,
		Direction:     direction,
		Amount:        amount,
		Currency:      currency,
		BalanceBefore: before,
		BalanceAfter:  after,
		CreatedAt:     now,
	}
}

// Post is the double-entry posting: it moves the transaction amount from debitAccount to creditAccount as exactly one
// Debit and one Credit. Every check runs before any account changes, so a failure leaves both accounts untouched.
// debitFromHold means the amount was already held on debitAccount.
func Post(t *Transaction, debitAccount, creditAccount *Account, debitFromHold bool, now time.Time) ([]Entry, error) {
	amount := t.Amount
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if debitAccount.Currency != Currency || creditAccount.Currency != Currency {
		return nil, ErrUnsupportedCurrency
	}
	if debitAccount.ID == creditAccount.ID {
		return nil, ErrSameAccount
	}
	if err := debitAccount.CanDebit(amount, debitFromHold); err != nil {
		return nil, err
	}
	return []Entry{
		debitAccount.Debit(t.ID, amount, debitFromHold, now),
		creditAccount.Credit(t.ID, amount, now),
	}, nil
}
```

Create `golang/internal/ledger/domain/withdrawal.go`:

```go
package domain

import "github.com/google/uuid"

// WithdrawalDetails is where a withdrawal pays out to. One row per withdrawal transaction.
type WithdrawalDetails struct {
	TransactionID uuid.UUID
	BankCode      string
	AccountNumber string
	AccountName   string
}
```

Create `golang/internal/ledger/domain/requesthash.go`:

```go
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// RequestHash is a stable fingerprint of a request's meaningful fields, stored with the transaction for idempotency
// checks.
func RequestHash(parts ...any) string {
	texts := make([]string, len(parts))
	for i, part := range parts {
		texts[i] = fmt.Sprint(part)
	}
	sum := sha256.Sum256([]byte(strings.Join(texts, "|")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go test ./internal/ledger/domain/...`
Expected: `ok  	wallet/internal/ledger/domain`

- [ ] **Step 6: Commit**

```bash
git add golang
git commit -m "feat(golang): port the ledger domain model"
```

---

### Task 3: Schema, sqlc queries and the Postgres test harness

**Files:**
- Create: `golang/sqlc.yaml`
- Create: `golang/internal/ledger/store/migrations/00001_init.sql`
- Create in `golang/internal/ledger/store/queries/`: `accounts.sql`, `transactions.sql`, `entries.sql`,
  `withdrawals.sql`, `outbox.sql`, `reconciliation.sql`
- Generate: `golang/internal/ledger/store/db/*.go` (committed)
- Create: `golang/internal/ledger/store/store.go`, `golang/internal/ledger/store/migrate.go`
- Create: `golang/internal/ledger/ledgertest/postgres.go`
- Test: `golang/internal/ledger/store/schema_test.go`

**Interfaces:**
- Consumes: `domain.ProviderSettlementID`.
- Produces:
  - **`store` package:** `type Store`, `func New(pool *pgxpool.Pool, opts ...Option) *Store`,
    `(*Store).Pool() *pgxpool.Pool`, `(*Store).Queries() *db.Queries`,
    `func Migrate(ctx context.Context, pool *pgxpool.Pool) error`. `Option` is defined here as `func(*Store)`; Task 4
    adds the options.
  - **`db` package (generated)**, with these models and methods:
    - Models: `Account`, `Transaction`, `LedgerEntry`, `Withdrawal`, `OutboxMessage`.
    - Accounts: `GetAccount`, `GetWalletByUser`, `InsertAccount`, `UpdateAccount` (returns rows affected).
    - Transactions: `GetTransaction`, `GetTransactionByProviderReference`, `GetTransactionByReference`,
      `ListTransactions`, `InsertTransaction`, `UpdateTransaction` (returns rows affected),
      `StalePendingWithdrawals`, `FindWithdrawalID`.
    - Entries and withdrawals: `InsertLedgerEntry`, `InsertWithdrawal`, `GetWithdrawal`.
    - Outbox: `InsertOutboxMessage`, `PendingOutboxMessages`, `MarkOutboxProcessed`, `RecordOutboxFailure`.
    - Reconciliation: `UnbalancedTransactions`, `DriftedAccounts`, `InvalidWallets`, `TotalBalance`.

    Step 4 lists every exact signature.
  - **`ledgertest` package:** `func NewPool(t *testing.T) *pgxpool.Pool` (a fresh migrated database per test),
    `func NewStore(t *testing.T) *store.Store`, `func RequirePgCode(t *testing.T, err error, code string)`.

- [ ] **Step 1: Add dependencies and write the migration**

```bash
cd golang
mkdir -p internal/ledger/store/migrations internal/ledger/store/queries internal/ledger/store/db internal/ledger/ledgertest
go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest \
  github.com/testcontainers/testcontainers-go@latest github.com/testcontainers/testcontainers-go/modules/postgres@latest
```

Create `golang/internal/ledger/store/migrations/00001_init.sql`:

```sql
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
```

- [ ] **Step 2: Write the queries and sqlc config**

Create `golang/sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: postgresql
    schema: internal/ledger/store/migrations
    queries: internal/ledger/store/queries
    gen:
      go:
        package: db
        out: internal/ledger/store/db
        sql_package: pgx/v5
        emit_pointers_for_null_types: true
        overrides:
          - db_type: uuid
            go_type: github.com/google/uuid.UUID
          - db_type: uuid
            nullable: true
            go_type:
              import: github.com/google/uuid
              type: UUID
              pointer: true
          - db_type: timestamptz
            go_type: time.Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

Create `golang/internal/ledger/store/queries/accounts.sql`:

```sql
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
```

Create `golang/internal/ledger/store/queries/transactions.sql`:

```sql
-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- name: GetTransactionByProviderReference :one
SELECT * FROM transactions
WHERE provider_reference_id = sqlc.arg(provider_reference_id)::text AND type = sqlc.arg(type);

-- name: GetTransactionByReference :one
SELECT * FROM transactions WHERE account_id = $1 AND reference_id = $2;

-- name: ListTransactions :many
SELECT * FROM transactions WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: InsertTransaction :exec
INSERT INTO transactions (id, account_id, type, status, amount, reference_id, provider_reference_id, request_hash,
                          created_at, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateTransaction :execrows
UPDATE transactions
SET status = sqlc.arg(status),
    provider_reference_id = sqlc.narg(provider_reference_id),
    completed_at = sqlc.narg(completed_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: StalePendingWithdrawals :many
SELECT id FROM transactions
WHERE type = 'Withdrawal' AND status = 'Pending' AND created_at <= $1
ORDER BY created_at;

-- name: FindWithdrawalID :one
SELECT id FROM transactions
WHERE type = 'Withdrawal'
  AND (provider_reference_id = sqlc.arg(provider_reference_id)::text OR id = sqlc.narg(keyed_id))
LIMIT 1;
```

Create `golang/internal/ledger/store/queries/entries.sql`:

```sql
-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (id, transaction_id, account_id, direction, amount, currency, balance_before,
                            balance_after, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);
```

Create `golang/internal/ledger/store/queries/withdrawals.sql`:

```sql
-- name: InsertWithdrawal :exec
INSERT INTO withdrawals (transaction_id, bank_code, account_number, account_name) VALUES ($1, $2, $3, $4);

-- name: GetWithdrawal :one
SELECT * FROM withdrawals WHERE transaction_id = $1;
```

Create `golang/internal/ledger/store/queries/outbox.sql`:

```sql
-- name: InsertOutboxMessage :exec
INSERT INTO outbox_messages (id, type, payload, occurred_at) VALUES ($1, $2, $3, $4);

-- name: PendingOutboxMessages :many
SELECT * FROM outbox_messages WHERE processed_at IS NULL ORDER BY occurred_at, id LIMIT $1;

-- name: MarkOutboxProcessed :exec
UPDATE outbox_messages
SET processed_at = sqlc.arg(processed_at)::timestamptz, attempts = attempts + 1
WHERE id = sqlc.arg(id);

-- name: RecordOutboxFailure :exec
UPDATE outbox_messages
SET last_error = sqlc.arg(last_error)::text, attempts = attempts + 1
WHERE id = sqlc.arg(id);
```

Create `golang/internal/ledger/store/queries/reconciliation.sql`:

```sql
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
```

- [ ] **Step 3: Generate the code**

Run: `cd golang && make generate`
Expected: no output, and `internal/ledger/store/db/` contains `db.go`, `models.go` and one `*.sql.go` per query
file.

- [ ] **Step 4: Verify the generated API matches what later tasks use**

Run: `cd golang && go mod tidy && go doc -all ./internal/ledger/store/db | grep -E '^func \(q \*Queries\)|^type .*(Params|Row) struct|^type (Account|Transaction|LedgerEntry|Withdrawal|OutboxMessage) struct'`

Expected: these method signatures (field names inside the Params/Row structs follow the column or `sqlc.arg` names
in CamelCase, for example `ProviderReferenceID`, `ExpectedVersion`, `KeyedID`):

```
func (q *Queries) DriftedAccounts(ctx context.Context) ([]DriftedAccountsRow, error)
func (q *Queries) FindWithdrawalID(ctx context.Context, arg FindWithdrawalIDParams) (uuid.UUID, error)
func (q *Queries) GetAccount(ctx context.Context, id uuid.UUID) (Account, error)
func (q *Queries) GetTransaction(ctx context.Context, id uuid.UUID) (Transaction, error)
func (q *Queries) GetTransactionByProviderReference(ctx context.Context, arg GetTransactionByProviderReferenceParams) (Transaction, error)
func (q *Queries) GetTransactionByReference(ctx context.Context, arg GetTransactionByReferenceParams) (Transaction, error)
func (q *Queries) GetWalletByUser(ctx context.Context, userID uuid.UUID) (Account, error)
func (q *Queries) GetWithdrawal(ctx context.Context, transactionID uuid.UUID) (Withdrawal, error)
func (q *Queries) InsertAccount(ctx context.Context, arg InsertAccountParams) error
func (q *Queries) InsertLedgerEntry(ctx context.Context, arg InsertLedgerEntryParams) error
func (q *Queries) InsertOutboxMessage(ctx context.Context, arg InsertOutboxMessageParams) error
func (q *Queries) InsertTransaction(ctx context.Context, arg InsertTransactionParams) error
func (q *Queries) InsertWithdrawal(ctx context.Context, arg InsertWithdrawalParams) error
func (q *Queries) InvalidWallets(ctx context.Context) ([]InvalidWalletsRow, error)
func (q *Queries) ListTransactions(ctx context.Context, arg ListTransactionsParams) ([]Transaction, error)
func (q *Queries) MarkOutboxProcessed(ctx context.Context, arg MarkOutboxProcessedParams) error
func (q *Queries) PendingOutboxMessages(ctx context.Context, limit int32) ([]OutboxMessage, error)
func (q *Queries) RecordOutboxFailure(ctx context.Context, arg RecordOutboxFailureParams) error
func (q *Queries) StalePendingWithdrawals(ctx context.Context, createdAt time.Time) ([]uuid.UUID, error)
func (q *Queries) TotalBalance(ctx context.Context) (int64, error)
func (q *Queries) UnbalancedTransactions(ctx context.Context) ([]UnbalancedTransactionsRow, error)
func (q *Queries) UpdateAccount(ctx context.Context, arg UpdateAccountParams) (int64, error)
func (q *Queries) UpdateTransaction(ctx context.Context, arg UpdateTransactionParams) (int64, error)
```

Model field types:
- `Account`: `UserID *uuid.UUID`, `Name *string`, `Version int64`
- `Transaction`: `ProviderReferenceID *string`, `CompletedAt *time.Time`
- `OutboxMessage`: `Payload []byte`, `ProcessedAt *time.Time`, `Attempts int32`, `LastError *string`

If a name or type differs, fix the query annotation or `sqlc.yaml` rather than adapting later tasks. Then re-run
`make generate`.

- [ ] **Step 5: Write the failing schema tests**

Create `golang/internal/ledger/store/schema_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
)

func TestTheProviderSettlementAccountIsSeeded(t *testing.T) {
	s := ledgertest.NewStore(t)

	settlement, err := s.Queries().GetAccount(context.Background(), domain.ProviderSettlementID)

	require.NoError(t, err)
	require.Equal(t, "System", settlement.Type)
	require.Equal(t, domain.ProviderSettlementName, *settlement.Name)
	require.Nil(t, settlement.UserID)
	require.Zero(t, settlement.Balance)
	require.Equal(t, "NGN", settlement.Currency)
}

const insertWallet = `INSERT INTO accounts (id, user_id, type, currency, balance, available_balance, version, created_at)
VALUES ($1, $2, 'User', $3, $4, $5, 0, now())`

const insertTransaction = `INSERT INTO transactions (id, account_id, type, status, amount, reference_id,
provider_reference_id, request_hash, created_at) VALUES ($1, $2, 'TopUp', 'Pending', $3, $4, $5, 'h', now())`

func TestAUserHasOneWalletPerCurrency(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, userID := context.Background(), uuid.New()
	_, err := pool.Exec(ctx, insertWallet, uuid.New(), userID, "NGN", 0, 0)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insertWallet, uuid.New(), userID, "NGN", 0, 0)

	ledgertest.RequirePgCode(t, err, "23505")
}

func TestReferenceIDsAreUniquePerWalletAndProviderReferencesAreUnique(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, walletID := context.Background(), uuid.New()
	_, err := pool.Exec(ctx, insertWallet, walletID, uuid.New(), "NGN", 0, 0)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-1", "chg_1")
	require.NoError(t, err)

	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-1", "chg_2")
	ledgertest.RequirePgCode(t, err, "23505")

	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 10, "ref-2", "chg_1")
	ledgertest.RequirePgCode(t, err, "23505")
}

func TestBalanceAndAmountRulesAreCheckConstraints(t *testing.T) {
	pool := ledgertest.NewPool(t)
	ctx, walletID := context.Background(), uuid.New()

	_, err := pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "NGN", -1, -1)
	ledgertest.RequirePgCode(t, err, "23514")
	_, err = pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "NGN", 10, 11)
	ledgertest.RequirePgCode(t, err, "23514")
	_, err = pool.Exec(ctx, insertWallet, uuid.New(), uuid.New(), "USD", 0, 0)
	ledgertest.RequirePgCode(t, err, "23514")

	_, err = pool.Exec(ctx, insertWallet, walletID, uuid.New(), "NGN", 0, 0)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, insertTransaction, uuid.New(), walletID, 0, "ref-0", nil)
	ledgertest.RequirePgCode(t, err, "23514")
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/store/...`
Expected: FAIL to compile, with `undefined: ledgertest.NewStore`.

- [ ] **Step 7: Implement `store.New`, `Migrate` and the test harness**

Create `golang/internal/ledger/store/store.go`:

```go
// Package store persists the ledger in Postgres. Store wraps the pool; Run and WithRetry (run.go) execute a unit of
// work with EF-style change tracking and optimistic concurrency.
package store

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/ledger/store/db"
)

type Store struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	beforeWrite Hook
}

type Option func(*Store)

func New(pool *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{pool: pool, queries: db.New(pool)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Queries runs generated queries directly on the pool, outside any unit of work. Use it for reads.
func (s *Store) Queries() *db.Queries { return s.queries }
```

`Hook` is defined in Task 4. So that this task compiles on its own, add `golang/internal/ledger/store/hook.go` now:

```go
package store

import "context"

// Hook runs inside Run after the operation succeeded and just before its changes are written. Tests use it to commit
// a competing write from another connection, which forces a concurrency conflict deterministically.
type Hook func(ctx context.Context) error
```

Create `golang/internal/ledger/store/migrate.go`:

```go
package store

import (
	"context"
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies every pending goose migration. It is idempotent.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}
```

Create `golang/internal/ledger/ledgertest/postgres.go`:

```go
// Package ledgertest holds test helpers shared by the wallet's test packages. Only _test.go files import it.
package ledgertest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"wallet/internal/ledger/store"
)

var (
	pgOnce     sync.Once
	pgAdminURL string
	pgErr      error
	// One template per test binary: go test runs packages in parallel processes that may share one server.
	templateDB = fmt.Sprintf("wallet_template_%d", os.Getpid())
)

// NewPool returns a pool on a fresh, migrated database that is dropped when the test ends. The server is
// WALLET_TEST_DATABASE_URL when set, otherwise a postgres:17 container started once per test binary.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pgOnce.Do(func() { pgAdminURL, pgErr = setupPostgres(ctx) })
	require.NoError(t, pgErr)

	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	exec(t, pgAdminURL, "CREATE DATABASE "+name+" TEMPLATE "+templateDB)
	pool, err := pgxpool.New(ctx, withDatabase(pgAdminURL, name))
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		exec(t, pgAdminURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool
}

func NewStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(NewPool(t))
}

// RequirePgCode asserts err is a Postgres error with the given SQLSTATE (23505 unique, 23514 check).
func RequirePgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a Postgres error, got %v", err)
	require.Equal(t, code, pgErr.Code)
}

func setupPostgres(ctx context.Context) (string, error) {
	serverURL := os.Getenv("WALLET_TEST_DATABASE_URL")
	if serverURL == "" {
		container, err := postgres.Run(ctx, "postgres:17",
			postgres.WithDatabase("postgres"),
			postgres.WithUsername("postgres"),
			postgres.WithPassword("postgres"),
			postgres.BasicWaitStrategies())
		if err != nil {
			return "", err
		}
		if serverURL, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return "", err
		}
	}
	admin := withDatabase(serverURL, "postgres")
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDB+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return "", err
	}
	// Close the migration pool before cloning: CREATE DATABASE ... TEMPLATE fails while the template has sessions.
	pool, err := pgxpool.New(ctx, withDatabase(serverURL, templateDB))
	if err != nil {
		return "", err
	}
	defer pool.Close()
	return admin, store.Migrate(ctx, pool)
}

func exec(t *testing.T, connString, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString)
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	require.NoError(t, err)
}

func withDatabase(connString, database string) string {
	u, err := url.Parse(connString)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + database
	return u.String()
}
```

There's a subtle bug to avoid: `setupPostgres` returns `admin, store.Migrate(ctx, pool)` while `defer pool.Close()`
is still pending. Go evaluates the return values **before** running deferred calls, so the migration finishes first
and the pool closes before any test clones the template. That is the order we need. Keep it as written.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go test ./internal/ledger/store/...`
Expected: `ok  	wallet/internal/ledger/store`. The first run pulls `postgres:17`.

- [ ] **Step 9: Commit**

```bash
git add golang
git commit -m "feat(golang): add Postgres schema, sqlc queries and test database harness"
```

---

### Task 4: Unit of work (`store.Tx`, `Run`, `WithRetry`) and test seeding

**Files:**
- Create in `golang/internal/ledger/store/`: `errors.go`, `mapping.go`, `tx.go`, `run.go`, `reads.go`, `outbox.go`
- Create: `golang/internal/ledger/ledgertest/seed.go`
- Test: `golang/internal/ledger/store/unit_of_work_test.go`

**Interfaces:**
- Consumes: the Task 3 `db` queries, the `domain` types, and `Hook`/`Option` from Task 3.
- Produces (package `store`):
  - **Errors and options:**
    - `var ErrConflict`: a concurrent write won; retrying may succeed.
    - `const MaxAttempts = 3`
    - `func WithBeforeWrite(h Hook) Option`
  - **Running a unit of work:**
    - `func Run[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error)` makes one
      attempt and returns `ErrConflict` on a lost race.
    - `func WithRetry[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error)`
      retries on `ErrConflict` and returns `domain.ErrConcurrencyExhausted` after `MaxAttempts`.
  - **`*Tx` reads.** Every loaded entity is tracked, and loading the same id twice returns the same pointer.
    - `Account(ctx, id uuid.UUID) (*domain.Account, error)`: errors if missing.
    - `WalletByUser(ctx, userID uuid.UUID) (*domain.Account, error)`: `nil, nil` if none.
    - `Transaction(ctx, id uuid.UUID) (*domain.Transaction, error)`: `nil, nil` if none.
    - `TransactionByProviderReference(ctx, ref string, typ domain.TransactionType) (*domain.Transaction, error)`:
      `nil, nil` if none.
    - `TransactionByReference(ctx, accountID uuid.UUID, referenceID string) (*domain.Transaction, error)`:
      `nil, nil` if none.
  - **`*Tx` staged inserts:** `AddAccount(*domain.Account)`, `AddTransaction(*domain.Transaction)`,
    `AddEntries(...domain.Entry)`, `AddWithdrawal(domain.WithdrawalDetails)`, `AddOutbox(OutboxMessage)`.
  - **Store reads:**
    - `(*Store).WalletByUser(ctx, userID) (*domain.Account, error)`: `nil, nil` if none.
    - `(*Store).Account(ctx, id) (*domain.Account, error)`
    - `(*Store).Transaction(ctx, id) (*domain.Transaction, error)`: `nil, nil` if none.
    - `(*Store).Transactions(ctx, accountID uuid.UUID, limit int32) ([]*domain.Transaction, error)`
    - `(*Store).Withdrawal(ctx, transactionID uuid.UUID) (domain.WithdrawalDetails, error)`
    - `(*Store).StalePendingWithdrawals(ctx, cutoff time.Time) ([]uuid.UUID, error)`
    - `(*Store).FindWithdrawalID(ctx, providerReferenceID string, keyedID *uuid.UUID) (uuid.UUID, bool, error)`
  - **Outbox:**
    - `type OutboxMessage struct{ ID uuid.UUID; Type string; Payload []byte; OccurredAt time.Time; ProcessedAt *time.Time; Attempts int32; LastError *string }`
    - `func NewOutboxMessage(messageType string, payload []byte, now time.Time) OutboxMessage`
    - `(*Store).PendingOutbox(ctx, limit int) ([]OutboxMessage, error)`
    - `(*Store).MarkOutboxProcessed(ctx, id uuid.UUID, now time.Time) error`
    - `(*Store).RecordOutboxFailure(ctx, id uuid.UUID, message string) error`: truncates to 2000 characters.
- Produces (package `ledgertest`):
  - **Fixtures:** `var Start` (2026-10-13 09:00 UTC), `func Logger() *slog.Logger` (discards output).
  - **Seeding:**
    - `func SeedWallet(t, s *store.Store, userID uuid.UUID, balance int64, at time.Time) uuid.UUID`
    - `func SeedTopUp(t, s *store.Store, accountID uuid.UUID, amount int64, at time.Time) uuid.UUID`: returns the
      top-up transaction id.
  - **Getters:** `func Wallet(t, s, userID) *domain.Account`, `func Settlement(t, s) *domain.Account`,
    `func Transaction(t, s, id) *domain.Transaction`.
  - **Assertions and hooks:**
    - `func Count(t, s *store.Store, sql string, args ...any) int64`
    - `func Times(n int, write func(ctx context.Context) error) store.Hook`
    - `func Hooked(s *store.Store, n int, write func(ctx context.Context) error) *store.Store`

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/store/unit_of_work_test.go`:

```go
package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/store"
)

func placeHold(walletID uuid.UUID, amount int64, attempts *int) func(context.Context, *store.Tx) (int64, error) {
	return func(ctx context.Context, tx *store.Tx) (int64, error) {
		if attempts != nil {
			*attempts++
		}
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return 0, err
		}
		return wallet.PlaceHold(amount)
	}
}

// holdElsewhere commits a hold through a separate unit of work, as a concurrent request would.
func holdElsewhere(s *store.Store, walletID uuid.UUID, amount int64) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := store.Run(ctx, s, placeHold(walletID, amount, nil))
		return err
	}
}

func TestWithRetrySavesTheOperationOnTheFirstAttempt(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	attempts := 0

	available, err := store.WithRetry(context.Background(), s, placeHold(walletID, 100, &attempts))

	require.NoError(t, err)
	require.EqualValues(t, 900, available)
	require.Equal(t, 1, attempts)
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestWithRetryRetriesFromFreshStateAfterAConcurrentWrite(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(1, holdElsewhere(s, walletID, 300))))
	attempts := 0

	available, err := store.WithRetry(context.Background(), hooked, placeHold(walletID, 100, &attempts))

	require.NoError(t, err)
	require.EqualValues(t, 600, available)
	require.Equal(t, 2, attempts)
	require.EqualValues(t, 600, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestWithRetryGivesUpAfterTheMaximumAttempts(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(store.MaxAttempts, holdElsewhere(s, walletID, 10))))
	attempts := 0

	_, err := store.WithRetry(context.Background(), hooked, placeHold(walletID, 100, &attempts))

	require.Equal(t, "Concurrency.Exhausted", common.CodeOf(err))
	require.Equal(t, store.MaxAttempts, attempts)
	require.EqualValues(t, 1_000-10*store.MaxAttempts, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestAFailedOperationIsNotSaved(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)

	_, err := store.WithRetry(context.Background(), s, func(ctx context.Context, tx *store.Tx) (int64, error) {
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return 0, err
		}
		if _, err := wallet.PlaceHold(100); err != nil {
			return 0, err
		}
		return 0, domain.ErrInsufficientFunds
	})

	require.ErrorIs(t, err, domain.ErrInsufficientFunds)
	require.EqualValues(t, 1_000, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestSavingAStaleAccountWritesNothing(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(1, holdElsewhere(s, walletID, 100))))

	_, err := store.Run(context.Background(), hooked, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return struct{}{}, err
		}
		if _, err := wallet.PlaceHold(200); err != nil {
			return struct{}{}, err
		}
		tx.AddOutbox(store.NewOutboxMessage("test.message", []byte("{}"), ledgertest.Start))
		return struct{}{}, nil
	})

	require.True(t, errors.Is(err, store.ErrConflict))
	require.Zero(t, ledgertest.Count(t, s, "SELECT count(*) FROM outbox_messages"))
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestAPostedTopUpRoundTrips(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()

	walletID := ledgertest.SeedWallet(t, s, userID, 5_000, ledgertest.Start)

	wallet := ledgertest.Wallet(t, s, userID)
	require.Equal(t, walletID, wallet.ID)
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, -5_000, ledgertest.Settlement(t, s).Balance)
	transactions, err := s.Transactions(context.Background(), walletID, 10)
	require.NoError(t, err)
	require.Len(t, transactions, 1)
	require.Equal(t, domain.Succeeded, transactions[0].Status)
	require.Equal(t, ledgertest.Start, transactions[0].CreatedAt)
	require.EqualValues(t, 2, ledgertest.Count(t, s, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactions[0].ID))
}

func TestADuplicateInsertIsAConflict(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 0, ledgertest.Start)

	_, err := store.Run(context.Background(), s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddAccount(domain.OpenUserWallet(userID, ledgertest.Start))
		return struct{}{}, nil
	})

	require.ErrorIs(t, err, store.ErrConflict)
}

func TestLoadingTheSameAccountTwiceReturnsOneTrackedCopy(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)

	_, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		first, err := tx.Account(ctx, walletID)
		require.NoError(t, err)
		second, err := tx.WalletByUser(ctx, userID)
		require.NoError(t, err)
		require.Same(t, first, second)
		_, err = first.PlaceHold(100)
		return struct{}{}, err
	})

	require.NoError(t, err)
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/store/...`
Expected: FAIL to compile, with `undefined: store.WithRetry`, `undefined: ledgertest.SeedWallet`.

- [ ] **Step 3: Implement the unit of work**

Create `golang/internal/ledger/store/errors.go`:

```go
package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConflict means another writer changed a tracked row first (a stale version or status) or committed the same
// unique key. Nothing was written; running the operation again sees the latest state.
var ErrConflict = errors.New("store: concurrent write conflict")

const uniqueViolation = "23505"

// writeErr turns a unique violation into ErrConflict (keeping the cause) and passes other errors through.
func writeErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return err
}
```

Create `golang/internal/ledger/store/mapping.go`:

```go
package store

import (
	"time"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// pgx returns timestamptz in the local zone; the wallet works in UTC.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toAccount(r db.Account) *domain.Account {
	return &domain.Account{
		ID:               r.ID,
		UserID:           r.UserID,
		Type:             domain.AccountType(r.Type),
		Name:             r.Name,
		Currency:         r.Currency,
		Balance:          r.Balance,
		AvailableBalance: r.AvailableBalance,
		Version:          r.Version,
		CreatedAt:        utc(r.CreatedAt),
	}
}

func toTransaction(r db.Transaction) *domain.Transaction {
	return &domain.Transaction{
		ID:                  r.ID,
		AccountID:           r.AccountID,
		Type:                domain.TransactionType(r.Type),
		Status:              domain.TransactionStatus(r.Status),
		Amount:              r.Amount,
		ReferenceID:         r.ReferenceID,
		ProviderReferenceID: r.ProviderReferenceID,
		RequestHash:         r.RequestHash,
		CreatedAt:           utc(r.CreatedAt),
		CompletedAt:         utcPtr(r.CompletedAt),
	}
}

func toOutboxMessage(r db.OutboxMessage) OutboxMessage {
	return OutboxMessage{
		ID:          r.ID,
		Type:        r.Type,
		Payload:     r.Payload,
		OccurredAt:  utc(r.OccurredAt),
		ProcessedAt: utcPtr(r.ProcessedAt),
		Attempts:    r.Attempts,
		LastError:   r.LastError,
	}
}
```

Create `golang/internal/ledger/store/tx.go`:

```go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// Tx is one unit of work, like an EF DbContext. Entities it loads are tracked (loading the same id twice returns
// the same pointer), Add* stages inserts, and Run writes every change in one database transaction once the operation
// returns. A changed account is saved only if its stored version is still the loaded one; a changed transaction only
// if its stored status is. Losing either race fails the save with ErrConflict.
type Tx struct {
	q               *db.Queries
	accounts        map[uuid.UUID]*trackedAccount
	accountList     []*trackedAccount
	transactions    map[uuid.UUID]*trackedTransaction
	transactionList []*trackedTransaction
	newAccounts     []*domain.Account
	newTransactions []*domain.Transaction
	entries         []domain.Entry
	withdrawals     []domain.WithdrawalDetails
	outbox          []OutboxMessage
}

type trackedAccount struct {
	current  *domain.Account
	original domain.Account
}

type trackedTransaction struct {
	current  *domain.Transaction
	original domain.Transaction
}

func newTx(q *db.Queries) *Tx {
	return &Tx{q: q, accounts: map[uuid.UUID]*trackedAccount{}, transactions: map[uuid.UUID]*trackedTransaction{}}
}

func (t *Tx) Account(ctx context.Context, id uuid.UUID) (*domain.Account, error) {
	if tracked, ok := t.accounts[id]; ok {
		return tracked.current, nil
	}
	row, err := t.q.GetAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load account %s: %w", id, err)
	}
	return t.trackAccount(row), nil
}

func (t *Tx) WalletByUser(ctx context.Context, userID uuid.UUID) (*domain.Account, error) {
	row, err := t.q.GetWalletByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t.trackAccount(row), nil
}

func (t *Tx) Transaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	if tracked, ok := t.transactions[id]; ok {
		return tracked.current, nil
	}
	return t.loadTransaction(t.q.GetTransaction(ctx, id))
}

func (t *Tx) TransactionByProviderReference(ctx context.Context, providerReferenceID string, typ domain.TransactionType) (*domain.Transaction, error) {
	return t.loadTransaction(t.q.GetTransactionByProviderReference(ctx, db.GetTransactionByProviderReferenceParams{
		ProviderReferenceID: providerReferenceID,
		Type:                string(typ),
	}))
}

func (t *Tx) TransactionByReference(ctx context.Context, accountID uuid.UUID, referenceID string) (*domain.Transaction, error) {
	return t.loadTransaction(t.q.GetTransactionByReference(ctx, db.GetTransactionByReferenceParams{
		AccountID:   accountID,
		ReferenceID: referenceID,
	}))
}

func (t *Tx) AddAccount(a *domain.Account)             { t.newAccounts = append(t.newAccounts, a) }
func (t *Tx) AddTransaction(tr *domain.Transaction)    { t.newTransactions = append(t.newTransactions, tr) }
func (t *Tx) AddEntries(entries ...domain.Entry)       { t.entries = append(t.entries, entries...) }
func (t *Tx) AddWithdrawal(w domain.WithdrawalDetails) { t.withdrawals = append(t.withdrawals, w) }
func (t *Tx) AddOutbox(m OutboxMessage)                { t.outbox = append(t.outbox, m) }

func (t *Tx) trackAccount(row db.Account) *domain.Account {
	if tracked, ok := t.accounts[row.ID]; ok {
		return tracked.current
	}
	account := toAccount(row)
	tracked := &trackedAccount{current: account, original: *account}
	t.accounts[row.ID] = tracked
	t.accountList = append(t.accountList, tracked)
	return account
}

func (t *Tx) loadTransaction(row db.Transaction, err error) (*domain.Transaction, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tracked, ok := t.transactions[row.ID]; ok {
		return tracked.current, nil
	}
	transaction := toTransaction(row)
	tracked := &trackedTransaction{current: transaction, original: *transaction}
	t.transactions[row.ID] = tracked
	t.transactionList = append(t.transactionList, tracked)
	return transaction, nil
}

func (t *Tx) hasWrites() bool {
	if len(t.newAccounts)+len(t.newTransactions)+len(t.entries)+len(t.withdrawals)+len(t.outbox) > 0 {
		return true
	}
	for _, a := range t.accountList {
		if *a.current != a.original {
			return true
		}
	}
	for _, tr := range t.transactionList {
		if *tr.current != tr.original {
			return true
		}
	}
	return false
}

// flush writes inserts and conditional updates in dependency order (foreign keys point at accounts and transactions).
func (t *Tx) flush(ctx context.Context) error {
	for _, a := range t.newAccounts {
		err := t.q.InsertAccount(ctx, db.InsertAccountParams{
			ID: a.ID, UserID: a.UserID, Type: string(a.Type), Name: a.Name, Currency: a.Currency,
			Balance: a.Balance, AvailableBalance: a.AvailableBalance, Version: a.Version, CreatedAt: a.CreatedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, tracked := range t.accountList {
		a := tracked.current
		if *a == tracked.original {
			continue
		}
		rows, err := t.q.UpdateAccount(ctx, db.UpdateAccountParams{
			Balance: a.Balance, AvailableBalance: a.AvailableBalance, Version: a.Version,
			ID: a.ID, ExpectedVersion: tracked.original.Version,
		})
		if err != nil {
			return writeErr(err)
		}
		if rows == 0 {
			return ErrConflict
		}
	}
	for _, tr := range t.newTransactions {
		err := t.q.InsertTransaction(ctx, db.InsertTransactionParams{
			ID: tr.ID, AccountID: tr.AccountID, Type: string(tr.Type), Status: string(tr.Status), Amount: tr.Amount,
			ReferenceID: tr.ReferenceID, ProviderReferenceID: tr.ProviderReferenceID, RequestHash: tr.RequestHash,
			CreatedAt: tr.CreatedAt, CompletedAt: tr.CompletedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, tracked := range t.transactionList {
		tr := tracked.current
		if *tr == tracked.original {
			continue
		}
		rows, err := t.q.UpdateTransaction(ctx, db.UpdateTransactionParams{
			Status: string(tr.Status), ProviderReferenceID: tr.ProviderReferenceID, CompletedAt: tr.CompletedAt,
			ID: tr.ID, ExpectedStatus: string(tracked.original.Status),
		})
		if err != nil {
			return writeErr(err)
		}
		if rows == 0 {
			return ErrConflict
		}
	}
	for _, w := range t.withdrawals {
		err := t.q.InsertWithdrawal(ctx, db.InsertWithdrawalParams{
			TransactionID: w.TransactionID, BankCode: w.BankCode, AccountNumber: w.AccountNumber, AccountName: w.AccountName,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, e := range t.entries {
		err := t.q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{
			ID: e.ID, TransactionID: e.TransactionID, AccountID: e.AccountID, Direction: string(e.Direction),
			Amount: e.Amount, Currency: e.Currency, BalanceBefore: e.BalanceBefore, BalanceAfter: e.BalanceAfter,
			CreatedAt: e.CreatedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, m := range t.outbox {
		err := t.q.InsertOutboxMessage(ctx, db.InsertOutboxMessageParams{
			ID: m.ID, Type: m.Type, Payload: m.Payload, OccurredAt: m.OccurredAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	return nil
}
```

Create `golang/internal/ledger/store/run.go`:

```go
package store

import (
	"context"
	"errors"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// MaxAttempts bounds WithRetry.
const MaxAttempts = 3

// WithBeforeWrite installs a hook that runs after a successful operation and before its changes are written.
func WithBeforeWrite(h Hook) Option { return func(s *Store) { s.beforeWrite = h } }

// Run executes fn as one unit of work: load → change → save. If fn fails nothing is written. If another writer
// changed a tracked row first, or committed the same unique key, nothing is written and Run returns ErrConflict.
func Run[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error) {
	var zero T
	pgTx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = pgTx.Rollback(context.WithoutCancel(ctx)) }() // no-op after Commit

	tx := newTx(db.New(pgTx))
	result, err := fn(ctx, tx)
	if err != nil {
		return zero, err
	}
	if !tx.hasWrites() {
		return result, pgTx.Commit(ctx)
	}
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx); err != nil {
			return zero, err
		}
	}
	if err := tx.flush(ctx); err != nil {
		return zero, err
	}
	if err := pgTx.Commit(ctx); err != nil {
		return zero, writeErr(err)
	}
	return result, nil
}

// WithRetry is Run, rerun from fresh state after a conflict (the port of .NET's ConcurrencyRetry). After MaxAttempts
// conflicts it returns domain.ErrConcurrencyExhausted.
func WithRetry[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		result, err := Run(ctx, s, fn)
		if !errors.Is(err, ErrConflict) {
			return result, err
		}
		if attempt == MaxAttempts {
			var zero T
			return zero, domain.ErrConcurrencyExhausted
		}
	}
}
```

Create `golang/internal/ledger/store/reads.go`:

```go
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// Reads outside a unit of work: nothing they return is tracked or saved.

func (s *Store) WalletByUser(ctx context.Context, userID uuid.UUID) (*domain.Account, error) {
	row, err := s.queries.GetWalletByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toAccount(row), nil
}

func (s *Store) Account(ctx context.Context, id uuid.UUID) (*domain.Account, error) {
	row, err := s.queries.GetAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load account %s: %w", id, err)
	}
	return toAccount(row), nil
}

func (s *Store) Transaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	row, err := s.queries.GetTransaction(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toTransaction(row), nil
}

// Transactions lists an account's transactions newest first.
func (s *Store) Transactions(ctx context.Context, accountID uuid.UUID, limit int32) ([]*domain.Transaction, error) {
	rows, err := s.queries.ListTransactions(ctx, db.ListTransactionsParams{AccountID: accountID, Limit: limit})
	if err != nil {
		return nil, err
	}
	transactions := make([]*domain.Transaction, len(rows))
	for i, row := range rows {
		transactions[i] = toTransaction(row)
	}
	return transactions, nil
}

func (s *Store) Withdrawal(ctx context.Context, transactionID uuid.UUID) (domain.WithdrawalDetails, error) {
	row, err := s.queries.GetWithdrawal(ctx, transactionID)
	if err != nil {
		return domain.WithdrawalDetails{}, fmt.Errorf("load withdrawal details %s: %w", transactionID, err)
	}
	return domain.WithdrawalDetails{
		TransactionID: row.TransactionID, BankCode: row.BankCode, AccountNumber: row.AccountNumber, AccountName: row.AccountName,
	}, nil
}

// StalePendingWithdrawals returns Pending withdrawals created at or before cutoff, oldest first.
func (s *Store) StalePendingWithdrawals(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	return s.queries.StalePendingWithdrawals(ctx, cutoff)
}

// FindWithdrawalID matches a withdrawal by the provider's reference or, when keyedID is set, by its own id.
func (s *Store) FindWithdrawalID(ctx context.Context, providerReferenceID string, keyedID *uuid.UUID) (uuid.UUID, bool, error) {
	id, err := s.queries.FindWithdrawalID(ctx, db.FindWithdrawalIDParams{ProviderReferenceID: providerReferenceID, KeyedID: keyedID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}
```

Create `golang/internal/ledger/store/outbox.go`:

```go
package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/store/db"
)

const maxOutboxErrorLength = 2_000

// OutboxMessage is an event saved in the same database transaction as the ledger change it describes, then relayed
// to the queue.
type OutboxMessage struct {
	ID          uuid.UUID
	Type        string
	Payload     []byte
	OccurredAt  time.Time
	ProcessedAt *time.Time
	Attempts    int32
	LastError   *string
}

func NewOutboxMessage(messageType string, payload []byte, now time.Time) OutboxMessage {
	return OutboxMessage{ID: uuid.Must(uuid.NewV7()), Type: messageType, Payload: payload, OccurredAt: now}
}

// PendingOutbox returns up to limit unprocessed messages, oldest first.
func (s *Store) PendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error) {
	rows, err := s.queries.PendingOutboxMessages(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	messages := make([]OutboxMessage, len(rows))
	for i, row := range rows {
		messages[i] = toOutboxMessage(row)
	}
	return messages, nil
}

func (s *Store) MarkOutboxProcessed(ctx context.Context, id uuid.UUID, now time.Time) error {
	return s.queries.MarkOutboxProcessed(ctx, db.MarkOutboxProcessedParams{ProcessedAt: now, ID: id})
}

func (s *Store) RecordOutboxFailure(ctx context.Context, id uuid.UUID, message string) error {
	if runes := []rune(message); len(runes) > maxOutboxErrorLength {
		message = string(runes[:maxOutboxErrorLength])
	}
	return s.queries.RecordOutboxFailure(ctx, db.RecordOutboxFailureParams{LastError: message, ID: id})
}
```

Create `golang/internal/ledger/ledgertest/seed.go`:

```go
package ledgertest

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// Start is the fixed time every test clock starts at.
var Start = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// SeedWallet opens a wallet and, when balance is positive, funds it with a settled top-up. Returns the wallet id.
func SeedWallet(t *testing.T, s *store.Store, userID uuid.UUID, balance int64, at time.Time) uuid.UUID {
	t.Helper()
	wallet := domain.OpenUserWallet(userID, at)
	_, err := store.Run(context.Background(), s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddAccount(wallet)
		return struct{}{}, nil
	})
	require.NoError(t, err)
	if balance > 0 {
		SeedTopUp(t, s, wallet.ID, balance, at)
	}
	return wallet.ID
}

// SeedTopUp posts a settled top-up to the wallet and returns its transaction id.
func SeedTopUp(t *testing.T, s *store.Store, accountID uuid.UUID, amount int64, at time.Time) uuid.UUID {
	t.Helper()
	id, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (uuid.UUID, error) {
		wallet, err := tx.Account(ctx, accountID)
		if err != nil {
			return uuid.Nil, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return uuid.Nil, err
		}
		topUp := domain.StartTopUp(accountID, amount, "seed-"+uuid.NewString(), "seed", "chg_seed_"+uuid.NewString(), at)
		entries, err := domain.Post(topUp, settlement, wallet, false, at)
		if err != nil {
			return uuid.Nil, err
		}
		topUp.MarkSucceeded(at)
		tx.AddTransaction(topUp)
		tx.AddEntries(entries...)
		return topUp.ID, nil
	})
	require.NoError(t, err)
	return id
}

func Wallet(t *testing.T, s *store.Store, userID uuid.UUID) *domain.Account {
	t.Helper()
	wallet, err := s.WalletByUser(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, wallet, "user %s has no wallet", userID)
	return wallet
}

func Settlement(t *testing.T, s *store.Store) *domain.Account {
	t.Helper()
	settlement, err := s.Account(context.Background(), domain.ProviderSettlementID)
	require.NoError(t, err)
	return settlement
}

func Transaction(t *testing.T, s *store.Store, id uuid.UUID) *domain.Transaction {
	t.Helper()
	transaction, err := s.Transaction(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, transaction, "transaction %s not found", id)
	return transaction
}

// Count runs a single-value count query.
func Count(t *testing.T, s *store.Store, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.Pool().QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// Times returns a before-write hook that runs write before the first n saves and does nothing afterwards: the
// equivalent of .NET's ConcurrentWriteInterceptor. write must use a store without this hook.
func Times(n int, write func(ctx context.Context) error) store.Hook {
	var mu sync.Mutex
	remaining := n
	return func(ctx context.Context) error {
		mu.Lock()
		if remaining == 0 {
			mu.Unlock()
			return nil
		}
		remaining--
		mu.Unlock()
		return write(ctx)
	}
}

// Hooked returns a store on the same pool whose first n saves each run write first (see Times). The handler under
// test uses the hooked store; write uses s, so the competing write is not itself hooked.
func Hooked(s *store.Store, n int, write func(ctx context.Context) error) *store.Store {
	return store.New(s.Pool(), store.WithBeforeWrite(Times(n, write)))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/store/...`
Expected: `ok  	wallet/internal/ledger/store`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add change-tracking unit of work with optimistic concurrency retry"
```

---

### Task 5: Payment provider interface and fake

**Files:**
- Create in `golang/internal/ledger/providers/`: `provider.go`, `keys.go`, `fake.go`
- Test in `golang/internal/ledger/providers/`: `fake_test.go`, `keys_test.go`

**Interfaces:**
- Consumes: nothing beyond uuid.
- Produces (package `providers`):
  - **Enums:**
    - `PayoutOutcome`: `Accepted`, `Rejected`
    - `PayoutStatus`: `StatusPending`, `StatusSucceeded`, `StatusFailed`, `StatusNotFound`
  - **Charge:** `type ChargeInitiation struct{ ProviderReferenceID string }`
  - **Payout initiation:**
    - `type PayoutInitiation struct{ Outcome PayoutOutcome; ProviderReferenceID *string; FailureReason *string }`
    - `func AcceptedPayout(providerReferenceID string) PayoutInitiation`
    - `func RejectedPayout(reason string) PayoutInitiation`
  - **Payout instruction:** `type PayoutInstruction struct{ ReferenceID string; Amount int64; BankCode, AccountNumber, AccountName string }`
  - **Provider interface:**
    `type PaymentProvider interface{ InitiateCharge(ctx, referenceID string, amount int64) (ChargeInitiation, error); InitiatePayout(ctx, PayoutInstruction) (PayoutInitiation, error); GetPayoutStatus(ctx, referenceID string) (PayoutStatus, error) }`
  - **Keys:**
    - `func ChargeKey(accountID uuid.UUID, referenceID string) string`: `<32 hex>-<referenceID>`
    - `func PayoutKey(transactionID uuid.UUID) string`: 32 hex characters, no hyphens
    - `func ParsePayoutKey(key string) (uuid.UUID, bool)`: accepts only the 32-hex form
  - **Fake provider:**
    - `type Record struct{ IsPayout bool; ReferenceID, ProviderReferenceID string; Amount int64; Status PayoutStatus }`
    - `type FakePaymentProvider`, `func NewFakePaymentProvider() *FakePaymentProvider`, which implements `PaymentProvider`
    - Methods: `SetPayoutBehaviour(func(PayoutInstruction) (PayoutInitiation, error))`, `PayoutAttempts() int`,
      `SetPayoutStatus(referenceID string, status PayoutStatus)`,
      `FindByProviderReference(providerReferenceID string) (Record, bool)`

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/providers/fake_test.go`:

```go
package providers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/providers"
)

var ctx = context.Background()

func instruction(referenceID string) providers.PayoutInstruction {
	return providers.PayoutInstruction{ReferenceID: referenceID, Amount: 4_000, BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace"}
}

func TestAChargeIsIdempotentPerReference(t *testing.T) {
	p := providers.NewFakePaymentProvider()

	first, err := p.InitiateCharge(ctx, "topup-1", 5_000)
	require.NoError(t, err)
	second, err := p.InitiateCharge(ctx, "topup-1", 5_000)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(first.ProviderReferenceID, "chg_"))
	require.Equal(t, first.ProviderReferenceID, second.ProviderReferenceID)
	record, ok := p.FindByProviderReference(first.ProviderReferenceID)
	require.True(t, ok)
	require.False(t, record.IsPayout)
	require.EqualValues(t, 5_000, record.Amount)
	require.Equal(t, "topup-1", record.ReferenceID)
}

func TestAPayoutIsAcceptedByDefaultAndIdempotentPerReference(t *testing.T) {
	p := providers.NewFakePaymentProvider()

	first, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)
	second, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)

	require.Equal(t, providers.Accepted, first.Outcome)
	require.True(t, strings.HasPrefix(*first.ProviderReferenceID, "trf_"))
	require.Equal(t, *first.ProviderReferenceID, *second.ProviderReferenceID)
	require.Equal(t, 2, p.PayoutAttempts())
	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusPending, status)
}

func TestAScriptedRejectionIsReturnedAndNotRecorded(t *testing.T) {
	p := providers.NewFakePaymentProvider()
	p.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.RejectedPayout("Invalid account"), nil
	})

	result, err := p.InitiatePayout(ctx, instruction("wd-1"))

	require.NoError(t, err)
	require.Equal(t, providers.Rejected, result.Outcome)
	require.Equal(t, "Invalid account", *result.FailureReason)
	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusNotFound, status)
}

func TestSetPayoutStatusChangesTheReportedStatus(t *testing.T) {
	p := providers.NewFakePaymentProvider()
	_, err := p.InitiatePayout(ctx, instruction("wd-1"))
	require.NoError(t, err)

	p.SetPayoutStatus("wd-1", providers.StatusSucceeded)

	status, err := p.GetPayoutStatus(ctx, "wd-1")
	require.NoError(t, err)
	require.Equal(t, providers.StatusSucceeded, status)
}

func TestAnUnknownPayoutIsNotFound(t *testing.T) {
	status, err := providers.NewFakePaymentProvider().GetPayoutStatus(ctx, "missing")

	require.NoError(t, err)
	require.Equal(t, providers.StatusNotFound, status)
}
```

Create `golang/internal/ledger/providers/keys_test.go`:

```go
package providers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/providers"
)

func TestPayoutKeysRoundTripOnlyInTheCompactForm(t *testing.T) {
	id := uuid.New()
	key := providers.PayoutKey(id)

	parsed, ok := providers.ParsePayoutKey(key)

	require.Len(t, key, 32)
	require.True(t, ok)
	require.Equal(t, id, parsed)
	_, ok = providers.ParsePayoutKey(id.String())
	require.False(t, ok, "hyphenated ids are not payout keys")
	_, ok = providers.ParsePayoutKey("not-a-guid")
	require.False(t, ok)
}

func TestChargeKeysAreScopedToTheWallet(t *testing.T) {
	first, second := uuid.New(), uuid.New()

	require.NotEqual(t, providers.ChargeKey(first, "ref-1"), providers.ChargeKey(second, "ref-1"))
	require.Equal(t, providers.PayoutKey(first)+"-ref-1", providers.ChargeKey(first, "ref-1"))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/providers/...`
Expected: FAIL to compile, with `undefined: providers.NewFakePaymentProvider`.

- [ ] **Step 3: Implement the providers package**

Create `golang/internal/ledger/providers/provider.go`:

```go
// Package providers is the payment provider as the wallet sees it, plus an in-memory fake.
package providers

import "context"

type PayoutOutcome string

const (
	Accepted PayoutOutcome = "Accepted"
	Rejected PayoutOutcome = "Rejected"
)

type PayoutStatus string

const (
	StatusPending   PayoutStatus = "Pending"
	StatusSucceeded PayoutStatus = "Succeeded"
	StatusFailed    PayoutStatus = "Failed"
	StatusNotFound  PayoutStatus = "NotFound"
)

type ChargeInitiation struct {
	ProviderReferenceID string
}

type PayoutInitiation struct {
	Outcome             PayoutOutcome
	ProviderReferenceID *string
	FailureReason       *string
}

func AcceptedPayout(providerReferenceID string) PayoutInitiation {
	return PayoutInitiation{Outcome: Accepted, ProviderReferenceID: &providerReferenceID}
}

func RejectedPayout(reason string) PayoutInitiation {
	return PayoutInitiation{Outcome: Rejected, FailureReason: &reason}
}

type PayoutInstruction struct {
	// ReferenceID is our key for the payout: the provider's idempotency key.
	ReferenceID   string
	Amount        int64
	BankCode      string
	AccountNumber string
	AccountName   string
}

// PaymentProvider: transient faults (network, 5xx) come back as errors; a definite refusal is a Rejected
// PayoutInitiation. Payouts are idempotent per ReferenceID.
type PaymentProvider interface {
	InitiateCharge(ctx context.Context, referenceID string, amount int64) (ChargeInitiation, error)
	InitiatePayout(ctx context.Context, instruction PayoutInstruction) (PayoutInitiation, error)
	GetPayoutStatus(ctx context.Context, referenceID string) (PayoutStatus, error)
}
```

Create `golang/internal/ledger/providers/keys.go`:

```go
package providers

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// The idempotency keys we send the provider. Client referenceIds are only unique per wallet, so they are never sent
// raw: a charge key adds the wallet id, a payout key is the withdrawal's transaction id. Both are stable across retries.

func ChargeKey(accountID uuid.UUID, referenceID string) string {
	return compact(accountID) + "-" + referenceID
}

func PayoutKey(transactionID uuid.UUID) string { return compact(transactionID) }

// ParsePayoutKey reads a payout key back into the transaction id. Only the 32-hex form is a payout key.
func ParsePayoutKey(key string) (uuid.UUID, bool) {
	if len(key) != 32 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(key)
	return id, err == nil
}

func compact(id uuid.UUID) string { return hex.EncodeToString(id[:]) }
```

Create `golang/internal/ledger/providers/fake.go`:

```go
package providers

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Record is what the fake provider remembers about a charge or payout.
type Record struct {
	IsPayout            bool
	ReferenceID         string
	ProviderReferenceID string
	Amount              int64
	Status              PayoutStatus
}

// FakePaymentProvider is an in-memory stand-in for a real provider. Tests script payout behaviour; the dev endpoint
// reads it to fake webhooks. Its memory is lost on restart, which the e2e script relies on.
type FakePaymentProvider struct {
	mu             sync.Mutex
	charges        map[string]*Record
	payouts        map[string]*Record
	payoutAttempts int
	behaviour      func(PayoutInstruction) (PayoutInitiation, error)
}

func NewFakePaymentProvider() *FakePaymentProvider {
	return &FakePaymentProvider{charges: map[string]*Record{}, payouts: map[string]*Record{}}
}

// SetPayoutBehaviour decides new payouts. Nil accepts them; returning an error simulates a transient provider fault.
func (p *FakePaymentProvider) SetPayoutBehaviour(behaviour func(PayoutInstruction) (PayoutInitiation, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.behaviour = behaviour
}

func (p *FakePaymentProvider) PayoutAttempts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.payoutAttempts
}

func (p *FakePaymentProvider) InitiateCharge(_ context.Context, referenceID string, amount int64) (ChargeInitiation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, ok := p.charges[referenceID]
	if !ok {
		record = &Record{ReferenceID: referenceID, ProviderReferenceID: "chg_" + compact(uuid.New()), Amount: amount, Status: StatusPending}
		p.charges[referenceID] = record
	}
	return ChargeInitiation{ProviderReferenceID: record.ProviderReferenceID}, nil
}

func (p *FakePaymentProvider) InitiatePayout(_ context.Context, instruction PayoutInstruction) (PayoutInitiation, error) {
	p.mu.Lock()
	p.payoutAttempts++
	if existing, ok := p.payouts[instruction.ReferenceID]; ok {
		defer p.mu.Unlock()
		if existing.Status == StatusFailed {
			return RejectedPayout("The payout already failed."), nil
		}
		return AcceptedPayout(existing.ProviderReferenceID), nil
	}
	behaviour := p.behaviour
	p.mu.Unlock()

	initiation := AcceptedPayout("trf_" + compact(uuid.New()))
	if behaviour != nil {
		var err error
		if initiation, err = behaviour(instruction); err != nil {
			return PayoutInitiation{}, err
		}
	}
	if initiation.Outcome == Accepted && initiation.ProviderReferenceID != nil {
		p.mu.Lock()
		p.payouts[instruction.ReferenceID] = &Record{
			IsPayout: true, ReferenceID: instruction.ReferenceID, ProviderReferenceID: *initiation.ProviderReferenceID,
			Amount: instruction.Amount, Status: StatusPending,
		}
		p.mu.Unlock()
	}
	return initiation, nil
}

func (p *FakePaymentProvider) GetPayoutStatus(_ context.Context, referenceID string) (PayoutStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record, ok := p.payouts[referenceID]; ok {
		return record.Status, nil
	}
	return StatusNotFound, nil
}

func (p *FakePaymentProvider) SetPayoutStatus(referenceID string, status PayoutStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record, ok := p.payouts[referenceID]; ok {
		record.Status = status
	}
}

func (p *FakePaymentProvider) FindByProviderReference(providerReferenceID string) (Record, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, records := range []map[string]*Record{p.charges, p.payouts} {
		for _, record := range records {
			if record.ProviderReferenceID == providerReferenceID {
				return *record, true
			}
		}
	}
	return Record{}, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/providers/...`
Expected: `ok  	wallet/internal/ledger/providers`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add payment provider interface and in-memory fake"
```

---

### Task 6: Wallets slice (create, get, list, idempotency helpers)

**Files:**
- Create in `golang/internal/ledger/wallets/`: `responses.go`, `accounts.go`, `idempotency.go`, `create.go`, `get.go`,
  `list.go`
- Test: `golang/internal/ledger/wallets/wallets_test.go`

**Interfaces:**
- Consumes: `store.Run`, `store.ErrConflict`, `(*store.Store).WalletByUser`, `(*store.Store).Transactions`,
  `(*store.Tx).WalletByUser`, `(*store.Tx).AddAccount`, `(*store.Tx).TransactionByReference`, and the `domain` types
  and errors.
- Produces (package `wallets`):
  - **Wallet response:**
    - `type WalletResponse struct{ AccountID uuid.UUID "accountId"; UserID uuid.UUID "userId"; Balance int64 "balance"; AvailableBalance int64 "availableBalance"; Currency string "currency" }`
      (the quoted strings are the JSON tags)
    - `func FromAccount(*domain.Account) WalletResponse`
  - **Transaction response:**
    - `type TransactionResponse struct{ ID "id"; Type "type"; Status "status"; Amount "amount"; Currency "currency"; ReferenceID "referenceId"; ProviderReferenceID *string "providerReferenceId"; CreatedAt time.Time "createdAt"; CompletedAt *time.Time "completedAt" }`
    - `func FromTransaction(*domain.Transaction) TransactionResponse`
  - **Result types:** `type IdempotentResponse struct{ Transaction TransactionResponse; Replayed bool }`,
    `type Creation struct{ Wallet WalletResponse; Created bool }`
  - **Helpers:**
    - `func GetOrCreate(ctx, tx *store.Tx, userID uuid.UUID, now time.Time) (*domain.Account, bool, error)`
    - `func FindReplay(ctx, tx *store.Tx, accountID uuid.UUID, referenceID, requestHash string) (*IdempotentResponse, error)`
  - **Handlers:**
    - `NewCreateHandler(s *store.Store, clock common.Clock) *CreateHandler`, `(*CreateHandler).Handle(ctx, userID uuid.UUID) (Creation, error)`
    - `NewGetHandler(s *store.Store) *GetHandler`, `(*GetHandler).Handle(ctx, userID) (WalletResponse, error)`
    - `NewListHandler(s *store.Store) *ListHandler`, `(*ListHandler).Handle(ctx, userID) ([]TransactionResponse, error)`:
      newest first, at most 100, never nil.

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/wallets/wallets_test.go`:

```go
package wallets_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/wallets"
)

var ctx = context.Background()

func TestCreateOpensExactlyOneEmptyWalletPerUser(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	handler := wallets.NewCreateHandler(s, common.NewFakeClock(ledgertest.Start))

	first, err := handler.Handle(ctx, userID)
	require.NoError(t, err)
	second, err := handler.Handle(ctx, userID)
	require.NoError(t, err)

	require.True(t, first.Created)
	require.Equal(t, userID, first.Wallet.UserID)
	require.Zero(t, first.Wallet.Balance)
	require.Equal(t, "NGN", first.Wallet.Currency)
	require.False(t, second.Created)
	require.Equal(t, first.Wallet.AccountID, second.Wallet.AccountID)
	require.EqualValues(t, 1, ledgertest.Count(t, s, "SELECT count(*) FROM accounts WHERE user_id = $1", userID))
}

func TestCreateReturnsTheWinnerWhenAConcurrentCreateCommitsFirst(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	clock := common.NewFakeClock(ledgertest.Start)
	var winner wallets.Creation
	hooked := ledgertest.Hooked(s, 1, func(ctx context.Context) error {
		var err error
		winner, err = wallets.NewCreateHandler(s, clock).Handle(ctx, userID)
		return err
	})

	result, err := wallets.NewCreateHandler(hooked, clock).Handle(ctx, userID)

	require.NoError(t, err)
	require.False(t, result.Created)
	require.Equal(t, winner.Wallet.AccountID, result.Wallet.AccountID)
	require.EqualValues(t, 1, ledgertest.Count(t, s, "SELECT count(*) FROM accounts WHERE user_id = $1", userID))
}

func TestGetForAnUnknownUserReturnsNotFound(t *testing.T) {
	_, err := wallets.NewGetHandler(ledgertest.NewStore(t)).Handle(ctx, uuid.New())

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestGetReturnsTheBalances(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 2_500, ledgertest.Start)

	wallet, err := wallets.NewGetHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.EqualValues(t, 2_500, wallet.Balance)
	require.EqualValues(t, 2_500, wallet.AvailableBalance)
}

func TestListReturnsTransactionsNewestFirst(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	ledgertest.SeedTopUp(t, s, walletID, 200, ledgertest.Start.Add(5*time.Minute))

	transactions, err := wallets.NewListHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.Len(t, transactions, 2)
	require.EqualValues(t, 200, transactions[0].Amount)
	require.EqualValues(t, 1_000, transactions[1].Amount)
	for _, tr := range transactions {
		require.Equal(t, "TopUp", tr.Type)
		require.Equal(t, "Succeeded", tr.Status)
		require.Equal(t, "NGN", tr.Currency)
	}
}

func TestListForAnUnknownUserReturnsNotFound(t *testing.T) {
	_, err := wallets.NewListHandler(ledgertest.NewStore(t)).Handle(ctx, uuid.New())

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestListOfAnEmptyWalletIsAnEmptySlice(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 0, ledgertest.Start)

	transactions, err := wallets.NewListHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.NotNil(t, transactions)
	require.Empty(t, transactions)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/wallets/...`
Expected: FAIL to compile, with `undefined: wallets.NewCreateHandler`.

- [ ] **Step 3: Implement the wallets slice**

Create `golang/internal/ledger/wallets/responses.go`:

```go
// Package wallets opens wallets, reads balances and history, and holds the response and idempotency helpers the
// top-up and withdrawal slices share.
package wallets

import (
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
)

type WalletResponse struct {
	AccountID        uuid.UUID `json:"accountId"`
	UserID           uuid.UUID `json:"userId"`
	Balance          int64     `json:"balance"`
	AvailableBalance int64     `json:"availableBalance"`
	Currency         string    `json:"currency"`
}

func FromAccount(a *domain.Account) WalletResponse {
	response := WalletResponse{AccountID: a.ID, Balance: a.Balance, AvailableBalance: a.AvailableBalance, Currency: a.Currency}
	if a.UserID != nil {
		response.UserID = *a.UserID
	}
	return response
}

type TransactionResponse struct {
	ID                  uuid.UUID  `json:"id"`
	Type                string     `json:"type"`
	Status              string     `json:"status"`
	Amount              int64      `json:"amount"`
	Currency            string     `json:"currency"`
	ReferenceID         string     `json:"referenceId"`
	ProviderReferenceID *string    `json:"providerReferenceId"`
	CreatedAt           time.Time  `json:"createdAt"`
	CompletedAt         *time.Time `json:"completedAt"`
}

func FromTransaction(t *domain.Transaction) TransactionResponse {
	return TransactionResponse{
		ID:                  t.ID,
		Type:                string(t.Type),
		Status:              string(t.Status),
		Amount:              t.Amount,
		Currency:            domain.Currency,
		ReferenceID:         t.ReferenceID,
		ProviderReferenceID: t.ProviderReferenceID,
		CreatedAt:           t.CreatedAt,
		CompletedAt:         t.CompletedAt,
	}
}

// IdempotentResponse is the transaction a command produced. Replayed is true when an earlier identical request
// already produced it.
type IdempotentResponse struct {
	Transaction TransactionResponse
	Replayed    bool
}

type Creation struct {
	Wallet  WalletResponse
	Created bool
}
```

Create `golang/internal/ledger/wallets/accounts.go`:

```go
package wallets

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// GetOrCreate returns the user's wallet, staging a new one in tx when none exists. created reports which.
func GetOrCreate(ctx context.Context, tx *store.Tx, userID uuid.UUID, now time.Time) (*domain.Account, bool, error) {
	existing, err := tx.WalletByUser(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}
	account := domain.OpenUserWallet(userID, now)
	tx.AddAccount(account)
	return account, true, nil
}
```

Create `golang/internal/ledger/wallets/idempotency.go`:

```go
package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// FindReplay returns nil when referenceID is new for this wallet. Otherwise it returns the original transaction when
// the request matches, or ErrDuplicateReference when the same referenceID arrives with a different request.
func FindReplay(ctx context.Context, tx *store.Tx, accountID uuid.UUID, referenceID, requestHash string) (*IdempotentResponse, error) {
	existing, err := tx.TransactionByReference(ctx, accountID, referenceID)
	if err != nil || existing == nil {
		return nil, err
	}
	if existing.RequestHash != requestHash {
		return nil, domain.ErrDuplicateReference
	}
	return &IdempotentResponse{Transaction: FromTransaction(existing), Replayed: true}, nil
}
```

Create `golang/internal/ledger/wallets/create.go`:

```go
package wallets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
)

type CreateHandler struct {
	store *store.Store
	clock common.Clock
}

func NewCreateHandler(s *store.Store, clock common.Clock) *CreateHandler {
	return &CreateHandler{store: s, clock: clock}
}

func (h *CreateHandler) Handle(ctx context.Context, userID uuid.UUID) (Creation, error) {
	creation, err := store.Run(ctx, h.store, func(ctx context.Context, tx *store.Tx) (Creation, error) {
		wallet, created, err := GetOrCreate(ctx, tx, userID, h.clock.Now())
		if err != nil {
			return Creation{}, err
		}
		return Creation{Wallet: FromAccount(wallet), Created: created}, nil
	})
	if !errors.Is(err, store.ErrConflict) {
		return creation, err
	}
	// A concurrent create won the unique (user_id, currency) index; return the winner's wallet.
	winner, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return Creation{}, err
	}
	if winner == nil {
		return Creation{}, fmt.Errorf("wallet for user %s missing after a conflicting create", userID)
	}
	return Creation{Wallet: FromAccount(winner)}, nil
}
```

Create `golang/internal/ledger/wallets/get.go`:

```go
package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

type GetHandler struct {
	store *store.Store
}

func NewGetHandler(s *store.Store) *GetHandler { return &GetHandler{store: s} }

func (h *GetHandler) Handle(ctx context.Context, userID uuid.UUID) (WalletResponse, error) {
	account, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return WalletResponse{}, err
	}
	if account == nil {
		return WalletResponse{}, domain.ErrWalletNotFound
	}
	return FromAccount(account), nil
}
```

Create `golang/internal/ledger/wallets/list.go`:

```go
package wallets

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

const pageSize = 100

type ListHandler struct {
	store *store.Store
}

func NewListHandler(s *store.Store) *ListHandler { return &ListHandler{store: s} }

// Handle returns the wallet's most recent transactions, newest first.
func (h *ListHandler) Handle(ctx context.Context, userID uuid.UUID) ([]TransactionResponse, error) {
	account, err := h.store.WalletByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, domain.ErrWalletNotFound
	}
	transactions, err := h.store.Transactions(ctx, account.ID, pageSize)
	if err != nil {
		return nil, err
	}
	responses := make([]TransactionResponse, len(transactions))
	for i, t := range transactions {
		responses[i] = FromTransaction(t)
	}
	return responses, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/wallets/...`
Expected: `ok  	wallet/internal/ledger/wallets`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add wallet create, get and list handlers"
```

---

### Task 7: Top-ups slice

**Files:**
- Create: `golang/internal/ledger/topups/initiate.go`, `golang/internal/ledger/topups/complete.go`
- Test: `golang/internal/ledger/topups/topups_test.go`

**Interfaces:**
- Consumes:
  - `store.Run`, `store.WithRetry`, `store.ErrConflict`
  - `wallets.GetOrCreate`, `wallets.FindReplay`, `wallets.FromTransaction`, `wallets.IdempotentResponse`,
    `wallets.TransactionResponse`
  - `providers.PaymentProvider`, `providers.ChargeKey`
  - `domain.*`
- Produces (package `topups`):
  - `type InitiateCommand struct{ UserID uuid.UUID; ReferenceID string; Amount int64 }`
  - `NewInitiateHandler(s *store.Store, provider providers.PaymentProvider, clock common.Clock) *InitiateHandler`,
    `(*InitiateHandler).Handle(ctx, InitiateCommand) (wallets.IdempotentResponse, error)`
  - `NewCompleteHandler(s *store.Store, clock common.Clock, log *slog.Logger) *CompleteHandler`, with
    `(*CompleteHandler).Succeed(ctx, providerReferenceID string, amount int64) (wallets.TransactionResponse, error)` and
    `(*CompleteHandler).Fail(ctx, providerReferenceID string) (wallets.TransactionResponse, error)`

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/topups/topups_test.go`:

```go
package topups_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
)

var ctx = context.Background()

type fixture struct {
	s        *store.Store
	clock    *common.FakeClock
	provider *providers.FakePaymentProvider
	userID   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		s:        ledgertest.NewStore(t),
		clock:    common.NewFakeClock(ledgertest.Start),
		provider: providers.NewFakePaymentProvider(),
		userID:   uuid.New(),
	}
}

func (f *fixture) initiate(s *store.Store) *topups.InitiateHandler {
	return topups.NewInitiateHandler(s, f.provider, f.clock)
}

func (f *fixture) complete(s *store.Store) *topups.CompleteHandler {
	return topups.NewCompleteHandler(s, f.clock, ledgertest.Logger())
}

func (f *fixture) command(amount int64) topups.InitiateCommand {
	return topups.InitiateCommand{UserID: f.userID, Amount: amount, ReferenceID: "topup-1"}
}

func (f *fixture) startTopUp(t *testing.T, amount int64) string {
	t.Helper()
	started, err := f.initiate(f.s).Handle(ctx, f.command(amount))
	require.NoError(t, err)
	return *started.Transaction.ProviderReferenceID
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return ledgertest.Count(t, f.s, sql, args...)
}

func TestInitiateOpensTheWalletLazilyAndRecordsAPendingTopUp(t *testing.T) {
	f := newFixture(t)

	result, err := f.initiate(f.s).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, "TopUp", result.Transaction.Type)
	require.Equal(t, "Pending", result.Transaction.Status)
	require.EqualValues(t, 5_000, result.Transaction.Amount)
	require.True(t, strings.HasPrefix(*result.Transaction.ProviderReferenceID, "chg_"))
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestInitiateRejectsNonPositiveAmounts(t *testing.T) {
	for _, amount := range []int64{0, -5} {
		f := newFixture(t)

		_, err := f.initiate(f.s).Handle(ctx, f.command(amount))

		require.Equal(t, "Money.InvalidAmount", common.CodeOf(err), "amount %d", amount)
		require.Zero(t, f.count(t, "SELECT count(*) FROM transactions"))
	}
}

func TestInitiateRejectsABlankReference(t *testing.T) {
	f := newFixture(t)
	command := f.command(5_000)
	command.ReferenceID = " "

	_, err := f.initiate(f.s).Handle(ctx, command)

	require.Equal(t, "Validation.ReferenceId", common.CodeOf(err))
}

func TestInitiateRejectsAnOversizedReferenceBeforeCharging(t *testing.T) {
	f := newFixture(t)
	command := f.command(5_000)
	command.ReferenceID = strings.Repeat("r", 129)

	_, err := f.initiate(f.s).Handle(ctx, command)

	require.Equal(t, "Validation.ReferenceId", common.CodeOf(err))
	require.Zero(t, f.count(t, "SELECT count(*) FROM transactions"))
}

func TestInitiateReplaysAnIdenticalRequest(t *testing.T) {
	f := newFixture(t)

	first, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)
	second, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)

	require.True(t, second.Replayed)
	require.Equal(t, first.Transaction.ID, second.Transaction.ID)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
}

func TestInitiateRejectsAReusedReferenceWithADifferentAmount(t *testing.T) {
	f := newFixture(t)
	_, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)

	_, err = f.initiate(f.s).Handle(ctx, f.command(6_000))

	require.Equal(t, "Transaction.DuplicateReference", common.CodeOf(err))
}

func TestSucceedCreditsTheWalletWithABalancedPosting(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, -5_000, ledgertest.Settlement(t, f.s).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestSucceedIsIdempotentForDuplicateWebhooks(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	_, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)
	require.NoError(t, err)

	again, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", again.Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", again.ID))
}

func TestSucceedWithAMismatchedAmountFailsTheTopUpWithoutCrediting(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 9_000)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestSucceedForAnUnknownReferenceReturnsNotFound(t *testing.T) {
	f := newFixture(t)

	_, err := f.complete(f.s).Succeed(ctx, "chg_missing", 5_000)

	require.Equal(t, "Transaction.NotFound", common.CodeOf(err))
}

func TestFailMarksTheTopUpFailedWithoutEntries(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Fail(ctx, providerReferenceID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestAFailureWebhookRacingBehindASuccessDoesNotOverwriteIt(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	successFirst := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)
		return err
	})

	result, err := f.complete(successFirst).Fail(ctx, providerReferenceID)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.Equal(t, domain.Succeeded, ledgertest.Transaction(t, f.s, result.ID).Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestConcurrentIdenticalInitiationsCreateOneTopUp(t *testing.T) {
	f := newFixture(t)
	twin := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
		return err
	})

	result, err := f.initiate(twin).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM accounts WHERE user_id = $1", f.userID))
}

func TestConcurrentFirstTopUpsShareOneWallet(t *testing.T) {
	f := newFixture(t)
	other := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		command := f.command(1_000)
		command.ReferenceID = "topup-other"
		_, err := f.initiate(f.s).Handle(ctx, command)
		return err
	})

	result, err := f.initiate(other).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM accounts WHERE user_id = $1", f.userID))
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
}

func TestReferenceIDsAreScopedToEachUsersWallet(t *testing.T) {
	f := newFixture(t)
	mine, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)
	command := f.command(5_000)
	command.UserID = uuid.New()

	theirs, err := f.initiate(f.s).Handle(ctx, command)

	require.NoError(t, err)
	require.False(t, theirs.Replayed)
	require.NotEqual(t, mine.Transaction.ID, theirs.Transaction.ID)
	require.NotEqual(t, *mine.Transaction.ProviderReferenceID, *theirs.Transaction.ProviderReferenceID)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/topups/...`
Expected: FAIL to compile, with `undefined: topups.NewInitiateHandler`.

- [ ] **Step 3: Implement the top-up handlers**

Create `golang/internal/ledger/topups/initiate.go`:

```go
// Package topups starts charges with the provider and applies the provider's verdict.
package topups

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

const maxInitiateAttempts = 2

type InitiateCommand struct {
	UserID      uuid.UUID
	ReferenceID string
	Amount      int64
}

// InitiateHandler starts a charge with the provider and records a Pending top-up. Money moves only when the
// provider's webhook confirms it. The user's wallet is opened on the first top-up.
type InitiateHandler struct {
	store    *store.Store
	provider providers.PaymentProvider
	clock    common.Clock
}

func NewInitiateHandler(s *store.Store, provider providers.PaymentProvider, clock common.Clock) *InitiateHandler {
	return &InitiateHandler{store: s, provider: provider, clock: clock}
}

func (h *InitiateHandler) Handle(ctx context.Context, command InitiateCommand) (wallets.IdempotentResponse, error) {
	if command.Amount <= 0 {
		return wallets.IdempotentResponse{}, domain.ErrInvalidAmount
	}
	if domain.BlankOrLonger(command.ReferenceID, domain.ReferenceIDMaxLength) {
		return wallets.IdempotentResponse{}, domain.Validation("ReferenceId",
			fmt.Sprintf("ReferenceId is required and at most %d characters.", domain.ReferenceIDMaxLength))
	}
	requestHash := domain.RequestHash(string(domain.TopUp), command.UserID, command.Amount)

	for attempt := 1; ; attempt++ {
		response, err := store.Run(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.IdempotentResponse, error) {
			now := h.clock.Now()
			wallet, created, err := wallets.GetOrCreate(ctx, tx, command.UserID, now)
			if err != nil {
				return wallets.IdempotentResponse{}, err
			}
			if !created {
				replay, err := wallets.FindReplay(ctx, tx, wallet.ID, command.ReferenceID, requestHash)
				if err != nil {
					return wallets.IdempotentResponse{}, err
				}
				if replay != nil {
					return *replay, nil
				}
			}
			charge, err := h.provider.InitiateCharge(ctx, providers.ChargeKey(wallet.ID, command.ReferenceID), command.Amount)
			if err != nil {
				return wallets.IdempotentResponse{}, err
			}
			topUp := domain.StartTopUp(wallet.ID, command.Amount, command.ReferenceID, requestHash, charge.ProviderReferenceID, now)
			tx.AddTransaction(topUp)
			return wallets.IdempotentResponse{Transaction: wallets.FromTransaction(topUp)}, nil
		})
		if !errors.Is(err, store.ErrConflict) {
			return response, err
		}
		// A concurrent request committed this referenceId or opened this user's wallet first (unique index).
		// Retry against what it committed: the replay check or the existing wallet now finds it.
		if attempt == maxInitiateAttempts {
			return wallets.IdempotentResponse{}, domain.ErrConcurrencyExhausted
		}
	}
}
```

Create `golang/internal/ledger/topups/complete.go`:

```go
package topups

import (
	"context"
	"log/slog"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// CompleteHandler applies the provider's verdict on a charge. Repeat deliveries for a final top-up change nothing.
type CompleteHandler struct {
	store *store.Store
	clock common.Clock
	log   *slog.Logger
}

func NewCompleteHandler(s *store.Store, clock common.Clock, log *slog.Logger) *CompleteHandler {
	return &CompleteHandler{store: s, clock: clock, log: log}
}

func (h *CompleteHandler) Succeed(ctx context.Context, providerReferenceID string, amount int64) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		topUp, err := find(ctx, tx, providerReferenceID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if topUp.IsFinal() {
			return wallets.FromTransaction(topUp), nil
		}
		now := h.clock.Now()
		if topUp.Amount != amount {
			h.log.Error("top-up amount mismatch; marking it failed",
				"transaction_id", topUp.ID, "expected_amount", topUp.Amount, "reported_amount", amount)
			topUp.MarkFailed(now)
			return wallets.FromTransaction(topUp), nil
		}
		wallet, err := tx.Account(ctx, topUp.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		entries, err := domain.Post(topUp, settlement, wallet, false, now)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		tx.AddEntries(entries...)
		topUp.MarkSucceeded(now)
		return wallets.FromTransaction(topUp), nil
	})
}

func (h *CompleteHandler) Fail(ctx context.Context, providerReferenceID string) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		topUp, err := find(ctx, tx, providerReferenceID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if !topUp.IsFinal() {
			topUp.MarkFailed(h.clock.Now())
		}
		return wallets.FromTransaction(topUp), nil
	})
}

func find(ctx context.Context, tx *store.Tx, providerReferenceID string) (*domain.Transaction, error) {
	topUp, err := tx.TransactionByProviderReference(ctx, providerReferenceID, domain.TopUp)
	if err != nil {
		return nil, err
	}
	if topUp == nil {
		return nil, domain.ErrTransactionNotFound
	}
	return topUp, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/topups/...`
Expected: `ok  	wallet/internal/ledger/topups`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add top-up initiation and completion handlers"
```

---

### Task 8: Withdrawals slice and the `PayoutRequested` message

**Files:**
- Create: `golang/internal/ledger/messages/payout_requested.go`
- Create in `golang/internal/ledger/withdrawals/`: `request.go`, `settle.go`, `release.go`
- Create: `golang/internal/ledger/ledgertest/scenarios.go`
- Test in `golang/internal/ledger/withdrawals/`: `request_test.go`, `settle_release_test.go`

**Interfaces:**
- Consumes: `store.*`, `wallets.FindReplay`, `wallets.FromTransaction`, `wallets.IdempotentResponse`,
  `wallets.TransactionResponse`, `domain.*`.
- Produces:
  - **`messages` package:**
    - `const PayoutRequestedType = "wallet.payout-requested"`
    - `type PayoutRequested struct{ TransactionID uuid.UUID "transactionId" }` (the quoted string is the JSON tag)
    - `func NewPayoutRequested(transactionID uuid.UUID, now time.Time) store.OutboxMessage`
  - **`withdrawals` package:**
    - `type RequestCommand struct{ UserID uuid.UUID; ReferenceID string; Amount int64; BankCode, AccountNumber, AccountName string }`
    - `NewRequestHandler(s *store.Store, clock common.Clock) *RequestHandler`,
      `(*RequestHandler).Handle(ctx, RequestCommand) (wallets.IdempotentResponse, error)`
    - `NewSettleHandler(s *store.Store, clock common.Clock) *SettleHandler`,
      `(*SettleHandler).Handle(ctx, transactionID uuid.UUID) (wallets.TransactionResponse, error)`
    - `NewReleaseHandler(s *store.Store, clock common.Clock, log *slog.Logger) *ReleaseHandler`,
      `(*ReleaseHandler).Handle(ctx, transactionID uuid.UUID, reason string) (wallets.TransactionResponse, error)`
  - **`ledgertest` package:**
    - `func WithdrawalCommand(userID uuid.UUID, amount int64, referenceID string) withdrawals.RequestCommand`
      (bank `058`, account `0123456789`, name `Ada Lovelace`)
    - `func PendingWithdrawal(t, s *store.Store, clock common.Clock, userID uuid.UUID, balance, amount int64) uuid.UUID`
    - `func AssignProviderReference(t, s *store.Store, transactionID uuid.UUID, providerReferenceID string)`

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/withdrawals/request_test.go`:

```go
package withdrawals_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s      *store.Store
	clock  *common.FakeClock
	userID uuid.UUID
}

func newFixture(t *testing.T, balance int64) *fixture {
	f := &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
	ledgertest.SeedWallet(t, f.s, f.userID, balance, ledgertest.Start)
	return f
}

func (f *fixture) request(s *store.Store) *withdrawals.RequestHandler { return withdrawals.NewRequestHandler(s, f.clock) }

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return ledgertest.Count(t, f.s, sql, args...)
}

func TestPlacesAHoldAndRecordsTheDetailsAndPayoutEventTogether(t *testing.T) {
	f := newFixture(t, 10_000)

	result, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, "Withdrawal", result.Transaction.Type)
	require.Equal(t, "Pending", result.Transaction.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	details, err := f.s.Withdrawal(ctx, result.Transaction.ID)
	require.NoError(t, err)
	require.Equal(t, "0123456789", details.AccountNumber)
	require.Equal(t, "058", details.BankCode)
	pending, err := f.s.PendingOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, messages.PayoutRequestedType, pending[0].Type)
	require.Nil(t, pending[0].ProcessedAt)
	var payload messages.PayoutRequested
	require.NoError(t, json.Unmarshal(pending[0].Payload, &payload))
	require.Equal(t, result.Transaction.ID, payload.TransactionID)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.Transaction.ID))
}

func TestWithdrawingTheEntireAvailableBalanceSucceeds(t *testing.T) {
	f := newFixture(t, 10_000)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 10_000, "wd-all"))
	require.NoError(t, err)
	_, err = f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 1, "wd-one-more"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestInsufficientFundsChangesNothing(t *testing.T) {
	f := newFixture(t, 10_000)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 20_000, "wd-1"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
	require.Zero(t, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestAUserWithoutAWalletGetsNotFound(t *testing.T) {
	f := newFixture(t, 0)

	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(uuid.New(), 100, "wd-1"))

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestInvalidRequestsAreRejected(t *testing.T) {
	cases := []struct {
		amount                  int64
		accountNumber, bankCode string
		code                    string
	}{
		{0, "0123456789", "058", "Money.InvalidAmount"},
		{100, "12345", "058", "Validation.AccountNumber"},
		{100, "01234567AB", "058", "Validation.AccountNumber"},
		{100, "0123456789", "", "Validation.BankCode"},
	}
	f := newFixture(t, 10_000)
	for _, c := range cases {
		command := ledgertest.WithdrawalCommand(f.userID, c.amount, "wd-1")
		command.AccountNumber, command.BankCode = c.accountNumber, c.bankCode

		_, err := f.request(f.s).Handle(ctx, command)

		require.Equal(t, c.code, common.CodeOf(err), "%+v", c)
	}
}

func TestOversizedFieldsAreRejected(t *testing.T) {
	f := newFixture(t, 10_000)
	for _, field := range []string{"ReferenceId", "BankCode", "AccountName"} {
		command := ledgertest.WithdrawalCommand(f.userID, 100, "wd-1")
		switch field {
		case "ReferenceId":
			command.ReferenceID = strings.Repeat("r", 129)
		case "BankCode":
			command.BankCode = strings.Repeat("1", 17)
		default:
			command.AccountName = strings.Repeat("a", 129)
		}

		_, err := f.request(f.s).Handle(ctx, command)

		require.Equal(t, "Validation."+field, common.CodeOf(err))
	}
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnIdenticalRetryReturnsTheOriginalWithoutASecondHold(t *testing.T) {
	f := newFixture(t, 10_000)

	first, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)
	retry, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)

	require.True(t, retry.Replayed)
	require.Equal(t, first.Transaction.ID, retry.Transaction.ID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestAReusedReferenceWithADifferentAmountIsRejected(t *testing.T) {
	f := newFixture(t, 10_000)
	_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)

	_, err = f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 5_000, "wd-1"))

	require.Equal(t, "Transaction.DuplicateReference", common.CodeOf(err))
}

func TestConcurrentWithdrawalsCannotSpendTheSameMoney(t *testing.T) {
	f := newFixture(t, 10_000)
	competitor := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 5_000, "wd-competing"))
		return err
	})

	_, err := f.request(competitor).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 8_000, "wd-mine"))

	require.Equal(t, "Wallet.InsufficientFunds", common.CodeOf(err))
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM outbox_messages"))
}

func TestConcurrentIdenticalRequestsCreateOneWithdrawal(t *testing.T) {
	f := newFixture(t, 10_000)
	twin := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
		return err
	})

	result, err := f.request(twin).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))

	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'Withdrawal'"))
}

func TestWithdrawalReferenceIDsAreScopedToEachUsersWallet(t *testing.T) {
	f := newFixture(t, 10_000)
	otherUserID := uuid.New()
	ledgertest.SeedWallet(t, f.s, otherUserID, 10_000, ledgertest.Start)

	mine, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 4_000, "wd-1"))
	require.NoError(t, err)
	theirs, err := f.request(f.s).Handle(ctx, ledgertest.WithdrawalCommand(otherUserID, 4_000, "wd-1"))
	require.NoError(t, err)

	require.False(t, theirs.Replayed)
	require.NotEqual(t, mine.Transaction.ID, theirs.Transaction.ID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, otherUserID).AvailableBalance)
}
```

Create `golang/internal/ledger/withdrawals/settle_release_test.go`:

```go
package withdrawals_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

func (f *fixture) settle(s *store.Store) *withdrawals.SettleHandler {
	return withdrawals.NewSettleHandler(s, f.clock)
}

func (f *fixture) release(s *store.Store) *withdrawals.ReleaseHandler {
	return withdrawals.NewReleaseHandler(s, f.clock, ledgertest.Logger())
}

func pendingWithdrawal(t *testing.T) (*fixture, uuid.UUID) {
	f := &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
	return f, ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
}

func TestSettleDebitsTheWalletFromTheHoldAndCreditsSettlement(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	result, err := f.settle(f.s).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.Balance)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
	require.EqualValues(t, -6_000, ledgertest.Settlement(t, f.s).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
	require.EqualValues(t, 1, f.count(t, `SELECT count(*) FROM ledger_entries
		WHERE transaction_id = $1 AND direction = 'Debit' AND account_id = $2 AND balance_after = 6000`, transactionID, wallet.ID))
	require.Zero(t, f.count(t, "SELECT COALESCE(sum(balance), 0)::bigint FROM accounts"))
}

func TestSettlingTwicePostsOnce(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	_, err := f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)
	_, err = f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)

	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}

func TestReleaseRestoresTheAvailableBalanceWithoutEntries(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)

	result, err := f.release(f.s).Handle(ctx, transactionID, "test")

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 10_000, wallet.AvailableBalance)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}

func TestReleaseAfterSettleChangesNothing(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	_, err := f.settle(f.s).Handle(ctx, transactionID)
	require.NoError(t, err)

	result, err := f.release(f.s).Handle(ctx, transactionID, "late failure")

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 6_000, wallet.Balance)
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
}

func TestSettleAfterReleaseChangesNothing(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	_, err := f.release(f.s).Handle(ctx, transactionID, "test")
	require.NoError(t, err)

	result, err := f.settle(f.s).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestSettlingOrReleasingAnUnknownWithdrawalReturnsNotFound(t *testing.T) {
	f, _ := pendingWithdrawal(t)

	_, settleErr := f.settle(f.s).Handle(ctx, uuid.New())
	_, releaseErr := f.release(f.s).Handle(ctx, uuid.New(), "test")

	require.Equal(t, "Transaction.NotFound", common.CodeOf(settleErr))
	require.Equal(t, "Transaction.NotFound", common.CodeOf(releaseErr))
}

func TestSettleAndReleaseRacingApplyExactlyOne(t *testing.T) {
	f, transactionID := pendingWithdrawal(t)
	releaseFirst := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.release(f.s).Handle(ctx, transactionID, "webhook said failed")
		return err
	})

	result, err := f.settle(releaseFirst).Handle(ctx, transactionID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 10_000, wallet.Balance)
	require.EqualValues(t, 10_000, wallet.AvailableBalance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactionID))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/withdrawals/...`
Expected: FAIL to compile, with `undefined: withdrawals.NewRequestHandler`, `undefined: ledgertest.WithdrawalCommand`.

- [ ] **Step 3: Implement the message, the handlers and the scenarios**

Create `golang/internal/ledger/messages/payout_requested.go`:

```go
// Package messages defines the events the wallet puts on its outbox. It is a leaf package so every slice that
// enqueues or consumes an event can import it.
package messages

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/store"
)

// PayoutRequestedType asks the payout worker to send a held withdrawal to the provider.
const PayoutRequestedType = "wallet.payout-requested"

type PayoutRequested struct {
	TransactionID uuid.UUID `json:"transactionId"`
}

func NewPayoutRequested(transactionID uuid.UUID, now time.Time) store.OutboxMessage {
	payload, err := json.Marshal(PayoutRequested{TransactionID: transactionID})
	if err != nil {
		panic(err) // a struct holding one UUID always marshals
	}
	return store.NewOutboxMessage(PayoutRequestedType, payload, now)
}
```

Create `golang/internal/ledger/withdrawals/request.go`:

```go
// Package withdrawals holds funds for a payout and settles or releases the hold once the provider decides.
package withdrawals

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

type RequestCommand struct {
	UserID        uuid.UUID
	ReferenceID   string
	Amount        int64
	BankCode      string
	AccountNumber string
	AccountName   string
}

// RequestHandler holds the funds and records the withdrawal, its bank details and a PayoutRequested outbox event in
// one save, so the payout is queued exactly when the hold commits.
type RequestHandler struct {
	store *store.Store
	clock common.Clock
}

func NewRequestHandler(s *store.Store, clock common.Clock) *RequestHandler {
	return &RequestHandler{store: s, clock: clock}
}

func (h *RequestHandler) Handle(ctx context.Context, command RequestCommand) (wallets.IdempotentResponse, error) {
	if err := validate(command); err != nil {
		return wallets.IdempotentResponse{}, err
	}
	requestHash := domain.RequestHash(string(domain.Withdrawal), command.UserID, command.Amount,
		command.BankCode, command.AccountNumber, command.AccountName)

	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.IdempotentResponse, error) {
		wallet, err := tx.WalletByUser(ctx, command.UserID)
		if err != nil {
			return wallets.IdempotentResponse{}, err
		}
		if wallet == nil {
			return wallets.IdempotentResponse{}, domain.ErrWalletNotFound
		}
		// Checked inside the retry: a racing identical request may have committed since the last attempt.
		replay, err := wallets.FindReplay(ctx, tx, wallet.ID, command.ReferenceID, requestHash)
		if err != nil {
			return wallets.IdempotentResponse{}, err
		}
		if replay != nil {
			return *replay, nil
		}
		if _, err := wallet.PlaceHold(command.Amount); err != nil {
			return wallets.IdempotentResponse{}, err
		}
		now := h.clock.Now()
		withdrawal := domain.StartWithdrawal(wallet.ID, command.Amount, command.ReferenceID, requestHash, now)
		tx.AddTransaction(withdrawal)
		tx.AddWithdrawal(domain.WithdrawalDetails{
			TransactionID: withdrawal.ID, BankCode: command.BankCode,
			AccountNumber: command.AccountNumber, AccountName: command.AccountName,
		})
		tx.AddOutbox(messages.NewPayoutRequested(withdrawal.ID, now))
		return wallets.IdempotentResponse{Transaction: wallets.FromTransaction(withdrawal)}, nil
	})
}

func validate(command RequestCommand) error {
	if command.Amount <= 0 {
		return domain.ErrInvalidAmount
	}
	if domain.BlankOrLonger(command.ReferenceID, domain.ReferenceIDMaxLength) {
		return domain.Validation("ReferenceId", fmt.Sprintf("ReferenceId is required and at most %d characters.", domain.ReferenceIDMaxLength))
	}
	if domain.BlankOrLonger(command.BankCode, domain.BankCodeMaxLength) {
		return domain.Validation("BankCode", fmt.Sprintf("BankCode is required and at most %d characters.", domain.BankCodeMaxLength))
	}
	if !isNUBAN(command.AccountNumber) {
		return domain.Validation("AccountNumber", "AccountNumber must be a 10-digit NUBAN.")
	}
	if domain.BlankOrLonger(command.AccountName, domain.AccountNameMaxLength) {
		return domain.Validation("AccountName", fmt.Sprintf("AccountName is required and at most %d characters.", domain.AccountNameMaxLength))
	}
	return nil
}

func isNUBAN(accountNumber string) bool {
	if len(accountNumber) != domain.AccountNumberLength {
		return false
	}
	for _, c := range accountNumber {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
```

Create `golang/internal/ledger/withdrawals/settle.go`:

```go
package withdrawals

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// SettleHandler: the provider paid out, so post the held amount from the wallet to settlement. Called by the
// webhook and the sweeper.
type SettleHandler struct {
	store *store.Store
	clock common.Clock
}

func NewSettleHandler(s *store.Store, clock common.Clock) *SettleHandler {
	return &SettleHandler{store: s, clock: clock}
}

func (h *SettleHandler) Handle(ctx context.Context, transactionID uuid.UUID) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		withdrawal, err := findWithdrawal(ctx, tx, transactionID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if withdrawal.IsFinal() {
			return wallets.FromTransaction(withdrawal), nil
		}
		now := h.clock.Now()
		wallet, err := tx.Account(ctx, withdrawal.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		entries, err := domain.Post(withdrawal, wallet, settlement, true, now)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		tx.AddEntries(entries...)
		withdrawal.MarkSucceeded(now)
		return wallets.FromTransaction(withdrawal), nil
	})
}

func findWithdrawal(ctx context.Context, tx *store.Tx, transactionID uuid.UUID) (*domain.Transaction, error) {
	withdrawal, err := tx.Transaction(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	if withdrawal == nil || withdrawal.Type != domain.Withdrawal {
		return nil, domain.ErrTransactionNotFound
	}
	return withdrawal, nil
}
```

Create `golang/internal/ledger/withdrawals/release.go`:

```go
package withdrawals

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

// ReleaseHandler: the payout definitely failed, so give the held amount back to AvailableBalance. There are no
// ledger entries, because no money moved.
type ReleaseHandler struct {
	store *store.Store
	clock common.Clock
	log   *slog.Logger
}

func NewReleaseHandler(s *store.Store, clock common.Clock, log *slog.Logger) *ReleaseHandler {
	return &ReleaseHandler{store: s, clock: clock, log: log}
}

func (h *ReleaseHandler) Handle(ctx context.Context, transactionID uuid.UUID, reason string) (wallets.TransactionResponse, error) {
	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.TransactionResponse, error) {
		withdrawal, err := findWithdrawal(ctx, tx, transactionID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if withdrawal.IsFinal() {
			return wallets.FromTransaction(withdrawal), nil
		}
		wallet, err := tx.Account(ctx, withdrawal.AccountID)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if _, err := wallet.ReleaseHold(withdrawal.Amount); err != nil {
			return wallets.TransactionResponse{}, err
		}
		withdrawal.MarkFailed(h.clock.Now())
		h.log.Warn("released withdrawal hold", "transaction_id", withdrawal.ID, "amount", withdrawal.Amount, "reason", reason)
		return wallets.FromTransaction(withdrawal), nil
	})
}
```

Create `golang/internal/ledger/ledgertest/scenarios.go`:

```go
package ledgertest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

func WithdrawalCommand(userID uuid.UUID, amount int64, referenceID string) withdrawals.RequestCommand {
	return withdrawals.RequestCommand{
		UserID: userID, Amount: amount, ReferenceID: referenceID,
		BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace",
	}
}

// PendingWithdrawal seeds a funded wallet with one held, Pending withdrawal ("wd-1") and returns its transaction id.
func PendingWithdrawal(t *testing.T, s *store.Store, clock common.Clock, userID uuid.UUID, balance, amount int64) uuid.UUID {
	t.Helper()
	SeedWallet(t, s, userID, balance, clock.Now())
	result, err := withdrawals.NewRequestHandler(s, clock).Handle(context.Background(), WithdrawalCommand(userID, amount, "wd-1"))
	require.NoError(t, err)
	return result.Transaction.ID
}

func AssignProviderReference(t *testing.T, s *store.Store, transactionID uuid.UUID, providerReferenceID string) {
	t.Helper()
	_, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		transaction, err := tx.Transaction(ctx, transactionID)
		if err != nil {
			return struct{}{}, err
		}
		transaction.AssignProviderReference(providerReferenceID)
		return struct{}{}, nil
	})
	require.NoError(t, err)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/withdrawals/... ./internal/ledger/store/...`
Expected: both `ok`. The store tests import `ledgertest`, which now imports `withdrawals`; this confirms there is no
import cycle.

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add withdrawal request, settle and release handlers"
```

---

### Task 9: Provider webhooks

**Files:**
- Create: `golang/internal/ledger/webhooks/handler.go`
- Test: `golang/internal/ledger/webhooks/handler_test.go`

**Interfaces:**
- Consumes: `topups.CompleteHandler`, `withdrawals.SettleHandler`, `withdrawals.ReleaseHandler`,
  `(*store.Store).FindWithdrawalID`, `providers.ParsePayoutKey`, `domain.ErrUnknownWebhookEvent`,
  `domain.ErrTransactionNotFound`.
- Produces (package `webhooks`):
  - Event constants: `ChargeSuccess = "charge.success"`, `ChargeFailed = "charge.failed"`,
    `TransferSuccess = "transfer.success"`, `TransferFailed = "transfer.failed"`
  - `type Webhook struct{ Event string; ProviderReferenceID string; Reference *string; Amount int64 }`
  - `NewHandler(s *store.Store, topUps *topups.CompleteHandler, settle *withdrawals.SettleHandler, release *withdrawals.ReleaseHandler) *Handler`,
    `(*Handler).Handle(ctx, Webhook) (wallets.TransactionResponse, error)`

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/webhooks/handler_test.go`:

```go
package webhooks_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s      *store.Store
	clock  *common.FakeClock
	userID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), userID: uuid.New()}
}

func (f *fixture) handler() *webhooks.Handler {
	log := ledgertest.Logger()
	return webhooks.NewHandler(f.s,
		topups.NewCompleteHandler(f.s, f.clock, log),
		withdrawals.NewSettleHandler(f.s, f.clock),
		withdrawals.NewReleaseHandler(f.s, f.clock, log))
}

func (f *fixture) startTopUp(t *testing.T, amount int64) string {
	t.Helper()
	started, err := topups.NewInitiateHandler(f.s, providers.NewFakePaymentProvider(), f.clock).
		Handle(ctx, topups.InitiateCommand{UserID: f.userID, ReferenceID: "topup-1", Amount: amount})
	require.NoError(t, err)
	return *started.Transaction.ProviderReferenceID
}

func webhook(event, providerReferenceID string, amount int64) webhooks.Webhook {
	return webhooks.Webhook{Event: event, ProviderReferenceID: providerReferenceID, Amount: amount}
}

func TestChargeSuccessCreditsTheWallet(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.handler().Handle(ctx, webhook(webhooks.ChargeSuccess, providerReferenceID, 5_000))

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestADuplicateChargeSuccessCreditsOnce(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	w := webhook(webhooks.ChargeSuccess, providerReferenceID, 5_000)

	_, err := f.handler().Handle(ctx, w)
	require.NoError(t, err)
	_, err = f.handler().Handle(ctx, w)
	require.NoError(t, err)

	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestChargeFailedFailsTheTopUp(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.handler().Handle(ctx, webhook(webhooks.ChargeFailed, providerReferenceID, 5_000))

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferSuccessSettlesTheWithdrawalFoundByProviderReference(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	ledgertest.AssignProviderReference(t, f.s, transactionID, "trf_abc")

	result, err := f.handler().Handle(ctx, webhook(webhooks.TransferSuccess, "trf_abc", 4_000))

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferSuccessBeforeTheProviderReferenceIsStoredMatchesOurReference(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	w := webhook(webhooks.TransferSuccess, "trf_not_yet_stored", 4_000)
	reference := providers.PayoutKey(transactionID)
	w.Reference = &reference

	result, err := f.handler().Handle(ctx, w)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestTransferFailedReleasesTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	ledgertest.AssignProviderReference(t, f.s, transactionID, "trf_abc")

	result, err := f.handler().Handle(ctx, webhook(webhooks.TransferFailed, "trf_abc", 4_000))

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnUnknownEventIsRejected(t *testing.T) {
	f := newFixture(t)

	_, err := f.handler().Handle(ctx, webhook("refund.created", "x", 1))

	require.Equal(t, "Validation.Event", common.CodeOf(err))
}

func TestAnUnknownTransferReferenceReturnsNotFound(t *testing.T) {
	f := newFixture(t)
	w := webhook(webhooks.TransferSuccess, "trf_missing", 1)
	reference := "not-a-guid"
	w.Reference = &reference

	_, err := f.handler().Handle(ctx, w)

	require.Equal(t, "Transaction.NotFound", common.CodeOf(err))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/webhooks/...`
Expected: FAIL to compile, with `undefined: webhooks.NewHandler`.

- [ ] **Step 3: Implement the handler**

Create `golang/internal/ledger/webhooks/handler.go`:

```go
// Package webhooks routes provider callbacks to the top-up and withdrawal handlers. Every route is idempotent.
package webhooks

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/withdrawals"
)

const (
	ChargeSuccess   = "charge.success"
	ChargeFailed    = "charge.failed"
	TransferSuccess = "transfer.success"
	TransferFailed  = "transfer.failed"
)

type Webhook struct {
	Event               string
	ProviderReferenceID string
	// Reference is the key we gave the provider, echoed back (for a payout, providers.PayoutKey: the transaction id).
	// It lets a transfer webhook match before the payout worker has stored the provider reference.
	Reference *string
	Amount    int64
}

type Handler struct {
	store   *store.Store
	topUps  *topups.CompleteHandler
	settle  *withdrawals.SettleHandler
	release *withdrawals.ReleaseHandler
}

func NewHandler(s *store.Store, topUps *topups.CompleteHandler, settle *withdrawals.SettleHandler, release *withdrawals.ReleaseHandler) *Handler {
	return &Handler{store: s, topUps: topUps, settle: settle, release: release}
}

func (h *Handler) Handle(ctx context.Context, webhook Webhook) (wallets.TransactionResponse, error) {
	switch webhook.Event {
	case ChargeSuccess:
		return h.topUps.Succeed(ctx, webhook.ProviderReferenceID, webhook.Amount)
	case ChargeFailed:
		return h.topUps.Fail(ctx, webhook.ProviderReferenceID)
	case TransferSuccess, TransferFailed:
		transactionID, found, err := h.findWithdrawalID(ctx, webhook)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if !found {
			return wallets.TransactionResponse{}, domain.ErrTransactionNotFound
		}
		if webhook.Event == TransferSuccess {
			return h.settle.Handle(ctx, transactionID)
		}
		return h.release.Handle(ctx, transactionID, "Provider reported transfer.failed.")
	default:
		return wallets.TransactionResponse{}, domain.ErrUnknownWebhookEvent
	}
}

func (h *Handler) findWithdrawalID(ctx context.Context, webhook Webhook) (uuid.UUID, bool, error) {
	// The payout key is the transaction id, so a well-formed reference identifies the withdrawal directly.
	var keyedID *uuid.UUID
	if webhook.Reference != nil {
		if id, ok := providers.ParsePayoutKey(*webhook.Reference); ok {
			keyedID = &id
		}
	}
	return h.store.FindWithdrawalID(ctx, webhook.ProviderReferenceID, keyedID)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/webhooks/...`
Expected: `ok  	wallet/internal/ledger/webhooks`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add provider webhook routing"
```

---

### Task 10: Outbox relay

**Files:**
- Create: `golang/internal/ledger/outbox/publisher.go`, `golang/internal/ledger/outbox/relay.go`
- Test: `golang/internal/ledger/outbox/relay_test.go`

**Interfaces:**
- Consumes: `(*store.Store).PendingOutbox`, `MarkOutboxProcessed`, `RecordOutboxFailure`; `store.NewOutboxMessage`;
  `messages.PayoutRequested`.
- Produces (package `outbox`):
  - `type Publisher interface{ Publish(ctx context.Context, messageType string, payload []byte, messageID uuid.UUID) error }`
  - `NewRelay(s *store.Store, publisher Publisher, clock common.Clock, batchSize int, log *slog.Logger) *Relay`
  - `(*Relay).RelayPending(ctx) (int, error)`. A publish error is recorded and the relay moves on. It stops and
    returns `ctx.Err()` only when ctx itself has ended.

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/outbox/relay_test.go`:

```go
package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/store"
)

var ctx = context.Background()

type published struct {
	messageType string
	payload     []byte
	messageID   uuid.UUID
}

type recordingPublisher struct {
	mu          sync.Mutex
	published   []published
	failWhen    func(uuid.UUID) bool
	timeOutWhen func(uuid.UUID) bool
}

func (p *recordingPublisher) Publish(_ context.Context, messageType string, payload []byte, messageID uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWhen != nil && p.failWhen(messageID) {
		return errors.New("broker unavailable")
	}
	if p.timeOutWhen != nil && p.timeOutWhen(messageID) {
		// What broker clients return on their own timeout: a deadline the caller's context did not set.
		return fmt.Errorf("publish timed out: %w", context.DeadlineExceeded)
	}
	p.published = append(p.published, published{messageType, payload, messageID})
	return nil
}

type fixture struct {
	s         *store.Store
	clock     *common.FakeClock
	publisher *recordingPublisher
}

func newFixture(t *testing.T) *fixture {
	return &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), publisher: &recordingPublisher{}}
}

func (f *fixture) relay(batchSize int) *outbox.Relay {
	return outbox.NewRelay(f.s, f.publisher, f.clock, batchSize, ledgertest.Logger())
}

func (f *fixture) addMessage(t *testing.T) uuid.UUID {
	t.Helper()
	message := store.NewOutboxMessage("test.message", []byte("{}"), f.clock.Now())
	_, err := store.Run(ctx, f.s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddOutbox(message)
		return struct{}{}, nil
	})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	return message.ID
}

type row struct {
	attempts    int32
	lastError   *string
	processedAt *time.Time
}

func (f *fixture) row(t *testing.T, id uuid.UUID) row {
	t.Helper()
	var r row
	err := f.s.Pool().QueryRow(ctx, "SELECT attempts, last_error, processed_at FROM outbox_messages WHERE id = $1", id).
		Scan(&r.attempts, &r.lastError, &r.processedAt)
	require.NoError(t, err)
	return r
}

func TestPublishesPendingMessagesOldestFirstAndMarksThemProcessed(t *testing.T) {
	f := newFixture(t)
	first, second := f.addMessage(t), f.addMessage(t)

	published, err := f.relay(50).RelayPending(ctx)
	require.NoError(t, err)
	publishedAgain, err := f.relay(50).RelayPending(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, published)
	require.Zero(t, publishedAgain)
	require.Equal(t, []uuid.UUID{first, second}, []uuid.UUID{f.publisher.published[0].messageID, f.publisher.published[1].messageID})
	require.Zero(t, ledgertest.Count(t, f.s, "SELECT count(*) FROM outbox_messages WHERE processed_at IS NULL"))
}

func TestAFailedPublishIsRecordedAndRetriedOnTheNextRun(t *testing.T) {
	f := newFixture(t)
	failing := f.addMessage(t)
	f.addMessage(t)
	f.publisher.failWhen = func(id uuid.UUID) bool { return id == failing }

	published, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	failed := f.row(t, failing)
	require.Nil(t, failed.processedAt)
	require.EqualValues(t, 1, failed.attempts)
	require.Equal(t, "broker unavailable", *failed.lastError)

	f.publisher.failWhen = nil
	published, err = f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	require.NotNil(t, f.row(t, failing).processedAt)
}

func TestAPublishTimeoutIsRecordedRatherThanTreatedAsShutdown(t *testing.T) {
	f := newFixture(t)
	timingOut := f.addMessage(t)
	f.addMessage(t)
	f.publisher.timeOutWhen = func(id uuid.UUID) bool { return id == timingOut }

	published, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	failed := f.row(t, timingOut)
	require.Nil(t, failed.processedAt)
	require.EqualValues(t, 1, failed.attempts)
}

func TestShutdownStopsTheRelayWithoutRecordingAFailure(t *testing.T) {
	f := newFixture(t)
	message := f.addMessage(t)
	runCtx, cancel := context.WithCancel(ctx)
	f.publisher.failWhen = func(uuid.UUID) bool { cancel(); return true }

	_, err := f.relay(50).RelayPending(runCtx)

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, f.row(t, message).attempts)
}

func TestPublishesAtMostOneBatchPerRun(t *testing.T) {
	f := newFixture(t)
	f.addMessage(t)
	f.addMessage(t)
	f.addMessage(t)

	first, err := f.relay(2).RelayPending(ctx)
	require.NoError(t, err)
	second, err := f.relay(2).RelayPending(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, first)
	require.Equal(t, 1, second)
}

func TestAWithdrawalPublishesAPayoutRequestForItsTransaction(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, uuid.New(), 10_000, 4_000)

	_, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Len(t, f.publisher.published, 1)
	require.Equal(t, messages.PayoutRequestedType, f.publisher.published[0].messageType)
	var payload messages.PayoutRequested
	require.NoError(t, json.Unmarshal(f.publisher.published[0].payload, &payload))
	require.Equal(t, transactionID, payload.TransactionID)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/outbox/...`
Expected: FAIL to compile, with `undefined: outbox.NewRelay`.

- [ ] **Step 3: Implement the relay**

Create `golang/internal/ledger/outbox/publisher.go`:

```go
// Package outbox relays events saved with ledger changes to RabbitMQ.
package outbox

import (
	"context"

	"github.com/google/uuid"
)

// Publisher sends one outbox message. It returns only after the broker has the message.
type Publisher interface {
	Publish(ctx context.Context, messageType string, payload []byte, messageID uuid.UUID) error
}
```

Create `golang/internal/ledger/outbox/relay.go`:

```go
package outbox

import (
	"context"
	"log/slog"

	"wallet/internal/common"
	"wallet/internal/ledger/store"
)

// Relay publishes unprocessed outbox messages oldest first and marks each processed after the broker accepts it.
// A crash between publish and save re-publishes the message, so delivery is at-least-once and consumers must be
// idempotent.
type Relay struct {
	store     *store.Store
	publisher Publisher
	clock     common.Clock
	batchSize int
	log       *slog.Logger
}

func NewRelay(s *store.Store, publisher Publisher, clock common.Clock, batchSize int, log *slog.Logger) *Relay {
	return &Relay{store: s, publisher: publisher, clock: clock, batchSize: batchSize, log: log}
}

func (r *Relay) RelayPending(ctx context.Context) (int, error) {
	batch, err := r.store.PendingOutbox(ctx, r.batchSize)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, message := range batch {
		err := r.publisher.Publish(ctx, message.Type, message.Payload, message.ID)
		if err != nil && ctx.Err() != nil {
			return published, ctx.Err()
		}
		if err != nil {
			r.log.Warn("publishing outbox message failed", "message_id", message.ID, "attempts", message.Attempts+1, "error", err)
			if err := r.store.RecordOutboxFailure(ctx, message.ID, err.Error()); err != nil {
				return published, err
			}
			continue
		}
		if err := r.store.MarkOutboxProcessed(ctx, message.ID, r.clock.Now()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/outbox/...`
Expected: `ok  	wallet/internal/ledger/outbox`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add outbox relay"
```

---

### Task 11: Payout processor

**Files:**
- Create: `golang/internal/ledger/payouts/processor.go`
- Test: `golang/internal/ledger/payouts/processor_test.go`

**Interfaces:**
- Consumes: `(*store.Store).Transaction`, `(*store.Store).Withdrawal`, `store.Run`, `store.ErrConflict`,
  `providers.PaymentProvider`, `providers.PayoutKey`, `withdrawals.ReleaseHandler`, `messages.PayoutRequested`.
- Produces (package `payouts`):
  - `type Outcome string`: `Initiated`, `Rejected`, `Skipped`
  - `NewProcessor(s *store.Store, provider providers.PaymentProvider, release *withdrawals.ReleaseHandler, log *slog.Logger) *Processor`
  - `(*Processor).Process(ctx, messages.PayoutRequested) (Outcome, error)`. A non-nil error means the delivery
    should be retried.

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/payouts/processor_test.go`:

```go
package payouts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s        *store.Store
	clock    *common.FakeClock
	provider *providers.FakePaymentProvider
	userID   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start),
		provider: providers.NewFakePaymentProvider(), userID: uuid.New(),
	}
}

func (f *fixture) release() *withdrawals.ReleaseHandler {
	return withdrawals.NewReleaseHandler(f.s, f.clock, ledgertest.Logger())
}

func (f *fixture) processor() *payouts.Processor {
	return payouts.NewProcessor(f.s, f.provider, f.release(), ledgertest.Logger())
}

func (f *fixture) pendingWithdrawal(t *testing.T) uuid.UUID {
	return ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
}

func TestAnAcceptedPayoutStoresTheProviderReferenceAndStaysPending(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	var sent providers.PayoutInstruction
	f.provider.SetPayoutBehaviour(func(instruction providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		sent = instruction
		return providers.AcceptedPayout("trf_accepted"), nil
	})

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Initiated, outcome)
	require.Equal(t, providers.PayoutInstruction{
		ReferenceID: providers.PayoutKey(transactionID), Amount: 4_000,
		BankCode: "058", AccountNumber: "0123456789", AccountName: "Ada Lovelace",
	}, sent)
	transaction := ledgertest.Transaction(t, f.s, transactionID)
	require.Equal(t, "trf_accepted", *transaction.ProviderReferenceID)
	require.False(t, transaction.IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestARejectedPayoutReleasesTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.RejectedPayout("Invalid account"), nil
	})

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Rejected, outcome)
	require.True(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestATransientProviderErrorPropagatesAndKeepsTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	timeout := errors.New("provider timeout")
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.PayoutInitiation{}, timeout
	})

	_, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.ErrorIs(t, err, timeout)
	transaction := ledgertest.Transaction(t, f.s, transactionID)
	require.False(t, transaction.IsFinal())
	require.Nil(t, transaction.ProviderReferenceID)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestAnAcceptanceWithoutAProviderReferenceKeepsTheHold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	f.provider.SetPayoutBehaviour(func(providers.PayoutInstruction) (providers.PayoutInitiation, error) {
		return providers.PayoutInitiation{Outcome: providers.Accepted}, nil
	})

	_, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.Error(t, err)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestARedeliveredMessageAfterAcceptanceDoesNotPayTwice(t *testing.T) {
	f := newFixture(t)
	message := messages.PayoutRequested{TransactionID: f.pendingWithdrawal(t)}
	_, err := f.processor().Process(ctx, message)
	require.NoError(t, err)

	redelivery, err := f.processor().Process(ctx, message)

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, redelivery)
	require.Equal(t, 1, f.provider.PayoutAttempts())
}

func TestAMessageForAFinalWithdrawalIsSkipped(t *testing.T) {
	f := newFixture(t)
	transactionID := f.pendingWithdrawal(t)
	_, err := f.release().Handle(ctx, transactionID, "test")
	require.NoError(t, err)

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: transactionID})

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, outcome)
	require.Zero(t, f.provider.PayoutAttempts())
}

func TestAMessageForAnUnknownTransactionIsSkipped(t *testing.T) {
	f := newFixture(t)

	outcome, err := f.processor().Process(ctx, messages.PayoutRequested{TransactionID: uuid.New()})

	require.NoError(t, err)
	require.Equal(t, payouts.Skipped, outcome)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/payouts/...`
Expected: FAIL to compile, with `undefined: payouts.NewProcessor`.

- [ ] **Step 3: Implement the processor**

Create `golang/internal/ledger/payouts/processor.go`:

```go
// Package payouts sends held withdrawals to the payment provider: the RabbitMQ consumer feeds Processor.
package payouts

import (
	"context"
	"fmt"
	"log/slog"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

type Outcome string

const (
	Initiated Outcome = "Initiated"
	Rejected  Outcome = "Rejected"
	Skipped   Outcome = "Skipped"
)

// Processor sends one held withdrawal to the provider. Acceptance only records the provider reference: the webhook
// or the sweeper settles it later. A definite rejection releases the hold. Transient faults and ambiguous answers
// return an error so the message is redelivered.
type Processor struct {
	store    *store.Store
	provider providers.PaymentProvider
	release  *withdrawals.ReleaseHandler
	log      *slog.Logger
}

func NewProcessor(s *store.Store, provider providers.PaymentProvider, release *withdrawals.ReleaseHandler, log *slog.Logger) *Processor {
	return &Processor{store: s, provider: provider, release: release, log: log}
}

func (p *Processor) Process(ctx context.Context, message messages.PayoutRequested) (Outcome, error) {
	withdrawal, err := p.store.Transaction(ctx, message.TransactionID)
	if err != nil {
		return "", err
	}
	if withdrawal == nil || withdrawal.Type != domain.Withdrawal {
		p.log.Warn("payout requested for unknown withdrawal; skipping", "transaction_id", message.TransactionID)
		return Skipped, nil
	}
	// A provider reference means an earlier delivery already got this payout accepted.
	if withdrawal.IsFinal() || withdrawal.ProviderReferenceID != nil {
		return Skipped, nil
	}
	details, err := p.store.Withdrawal(ctx, withdrawal.ID)
	if err != nil {
		return "", err
	}
	initiation, err := p.provider.InitiatePayout(ctx, providers.PayoutInstruction{
		ReferenceID: providers.PayoutKey(withdrawal.ID), Amount: withdrawal.Amount,
		BankCode: details.BankCode, AccountNumber: details.AccountNumber, AccountName: details.AccountName,
	})
	if err != nil {
		return "", fmt.Errorf("initiate payout for withdrawal %s: %w", withdrawal.ID, err)
	}

	switch {
	case initiation.Outcome == providers.Accepted && initiation.ProviderReferenceID != nil:
		if err := p.assignProviderReference(ctx, withdrawal, *initiation.ProviderReferenceID); err != nil {
			return "", err
		}
		return Initiated, nil
	case initiation.Outcome == providers.Rejected:
		reason := "Provider rejected the payout."
		if initiation.FailureReason != nil {
			reason = *initiation.FailureReason
		}
		if _, err := p.release.Handle(ctx, withdrawal.ID, reason); err != nil {
			return "", err
		}
		return Rejected, nil
	default:
		// Ambiguous: the provider may have paid. Releasing could double-spend, so fail the delivery and let it retry.
		return "", fmt.Errorf("provider returned an unusable payout result (%s, no reference) for withdrawal %s",
			initiation.Outcome, withdrawal.ID)
	}
}

// assignProviderReference saves the reference only if the withdrawal's status has not changed since it was read, as
// the EF status concurrency token did. A changed status fails the delivery; the redelivery then skips the final
// withdrawal.
func (p *Processor) assignProviderReference(ctx context.Context, read *domain.Transaction, providerReferenceID string) error {
	_, err := store.Run(ctx, p.store, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		current, err := tx.Transaction(ctx, read.ID)
		if err != nil {
			return struct{}{}, err
		}
		if current == nil || current.Status != read.Status {
			return struct{}{}, store.ErrConflict
		}
		current.AssignProviderReference(providerReferenceID)
		return struct{}{}, nil
	})
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/payouts/...`
Expected: `ok  	wallet/internal/ledger/payouts`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add payout processor"
```

---

### Task 12: Reconciliation (pending-payout sweeper and invariant checker)

**Files:**
- Create in `golang/internal/ledger/reconciliation/`: `sweeper.go`, `checker.go`, `run.go`
- Test in `golang/internal/ledger/reconciliation/`: `sweeper_test.go`, `checker_test.go`, `run_test.go`

**Interfaces:**
- Consumes:
  - `(*store.Store).StalePendingWithdrawals`, `store.Run`, `(*store.Store).Queries()`, and the `UnbalancedTransactions`,
    `DriftedAccounts`, `InvalidWallets` and `TotalBalance` queries
  - `providers.PaymentProvider`, `providers.PayoutKey`
  - `withdrawals.SettleHandler`, `withdrawals.ReleaseHandler`
  - `messages.NewPayoutRequested`
  - for tests: `payouts.NewProcessor`
- Produces (package `reconciliation`):
  - `type Summary struct{ Settled, Released, Redriven, StillPending, Errors int }`
  - `NewSweeper(s *store.Store, provider providers.PaymentProvider, settle *withdrawals.SettleHandler, release *withdrawals.ReleaseHandler, clock common.Clock, pendingThreshold time.Duration, log *slog.Logger) *Sweeper`,
    `(*Sweeper).Sweep(ctx) (Summary, error)`
  - `type Violation struct{ Invariant, Detail string }`
  - `NewChecker(s *store.Store, log *slog.Logger) *Checker`, `(*Checker).Check(ctx) ([]Violation, error)`
  - `func RunOnce(ctx context.Context, sweeper *Sweeper, checker *Checker, log *slog.Logger) error`. It logs
    `payout sweep` with attrs `settled, released, redriven, still_pending, errors`, in that order.

- [ ] **Step 1: Write the failing tests**

Create `golang/internal/ledger/reconciliation/sweeper_test.go`:

```go
package reconciliation_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/reconciliation"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

var ctx = context.Background()

type fixture struct {
	s        *store.Store
	clock    *common.FakeClock
	provider *providers.FakePaymentProvider
	userID   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start),
		provider: providers.NewFakePaymentProvider(), userID: uuid.New(),
	}
}

func (f *fixture) release() *withdrawals.ReleaseHandler {
	return withdrawals.NewReleaseHandler(f.s, f.clock, ledgertest.Logger())
}

func (f *fixture) sweeper(provider providers.PaymentProvider) *reconciliation.Sweeper {
	return reconciliation.NewSweeper(f.s, provider, withdrawals.NewSettleHandler(f.s, f.clock), f.release(),
		f.clock, 15*time.Minute, ledgertest.Logger())
}

func (f *fixture) process(t *testing.T, provider providers.PaymentProvider, transactionID uuid.UUID) payouts.Outcome {
	t.Helper()
	outcome, err := payouts.NewProcessor(f.s, provider, f.release(), ledgertest.Logger()).
		Process(ctx, messages.PayoutRequested{TransactionID: transactionID})
	require.NoError(t, err)
	return outcome
}

// submittedWithdrawal is a Pending withdrawal the provider has accepted, so the provider knows the payout.
func (f *fixture) submittedWithdrawal(t *testing.T) uuid.UUID {
	t.Helper()
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	f.process(t, f.provider, transactionID)
	return transactionID
}

func (f *fixture) sweep(t *testing.T, provider providers.PaymentProvider) reconciliation.Summary {
	t.Helper()
	summary, err := f.sweeper(provider).Sweep(ctx)
	require.NoError(t, err)
	return summary
}

func TestSettlesAStaleWithdrawalTheProviderReportsSucceeded(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusSucceeded)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Settled)
	require.True(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestReleasesAStaleWithdrawalTheProviderReportsFailed(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusFailed)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Released)
	require.EqualValues(t, 10_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestIgnoresWithdrawalsYoungerThanTheThreshold(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	f.provider.SetPayoutStatus(providers.PayoutKey(transactionID), providers.StatusSucceeded)
	f.clock.Advance(10 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, reconciliation.Summary{}, summary)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
}

func TestKeepsAPayoutTheProviderHasNeverSeenOnHoldAndRequeuesIt(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.Redriven)
	require.False(t, ledgertest.Transaction(t, f.s, transactionID).IsFinal())
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
	// the original request plus the sweeper's re-drive
	require.EqualValues(t, 2, ledgertest.Count(t, f.s,
		"SELECT count(*) FROM outbox_messages WHERE type = $1 AND payload->>'transactionId' = $2",
		messages.PayoutRequestedType, transactionID.String()))
}

func TestResubmitsAnAcceptedPayoutTheProviderHasLostInsteadOfRequeueingForever(t *testing.T) {
	f := newFixture(t)
	transactionID := f.submittedWithdrawal(t)
	restartedProvider := providers.NewFakePaymentProvider()
	f.clock.Advance(16 * time.Minute)

	require.Equal(t, 1, f.sweep(t, restartedProvider).Redriven)
	require.Nil(t, ledgertest.Transaction(t, f.s, transactionID).ProviderReferenceID, "the provider no longer knows that reference")

	require.Equal(t, payouts.Initiated, f.process(t, restartedProvider, transactionID))
	require.Equal(t, 1, restartedProvider.PayoutAttempts())

	require.Equal(t, reconciliation.Summary{StillPending: 1}, f.sweep(t, restartedProvider))
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}

func TestLeavesPayoutsTheProviderStillHasPending(t *testing.T) {
	f := newFixture(t)
	f.submittedWithdrawal(t)
	f.clock.Advance(16 * time.Minute)

	summary := f.sweep(t, f.provider)

	require.Equal(t, 1, summary.StillPending)
	require.EqualValues(t, 6_000, ledgertest.Wallet(t, f.s, f.userID).AvailableBalance)
}
```

Create `golang/internal/ledger/reconciliation/checker_test.go`:

```go
package reconciliation_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/reconciliation"
	"wallet/internal/ledger/withdrawals"
)

func (f *fixture) invariants(t *testing.T) []string {
	t.Helper()
	violations, err := reconciliation.NewChecker(f.s, ledgertest.Logger()).Check(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, v := range violations {
		names = append(names, v.Invariant)
	}
	return names
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.s.Pool().Exec(ctx, sql, args...)
	require.NoError(t, err)
}

func TestAHealthyLedgerHasNoViolations(t *testing.T) {
	f := newFixture(t)
	settledID := ledgertest.PendingWithdrawal(t, f.s, f.clock, f.userID, 10_000, 4_000)
	_, err := withdrawals.NewSettleHandler(f.s, f.clock).Handle(ctx, settledID)
	require.NoError(t, err)
	_, err = withdrawals.NewRequestHandler(f.s, f.clock).Handle(ctx, ledgertest.WithdrawalCommand(f.userID, 1_000, "wd-2"))
	require.NoError(t, err)

	require.Empty(t, f.invariants(t))
}

// Postgres enforces the foreign key InMemory ignored, so the stray entry joins the wallet's seed top-up.
func TestDetectsAnUnbalancedTransactionAndTheAccountDriftItCauses(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	var topUpID uuid.UUID
	require.NoError(t, f.s.Pool().QueryRow(ctx, "SELECT id FROM transactions WHERE account_id = $1", walletID).Scan(&topUpID))
	f.exec(t, `INSERT INTO ledger_entries (id, transaction_id, account_id, direction, amount, currency, balance_before,
		balance_after, created_at) VALUES ($1, $2, $3, 'Credit', 50, 'NGN', 10000, 10050, $4)`,
		uuid.New(), topUpID, walletID, ledgertest.Start)

	invariants := f.invariants(t)

	require.Contains(t, invariants, "I1")
	require.Contains(t, invariants, "I3")
}

func TestDetectsABalanceEditedOutsideTheLedger(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "UPDATE accounts SET balance = 20000 WHERE id = $1", walletID)

	invariants := f.invariants(t)

	require.Contains(t, invariants, "I3")
	require.Contains(t, invariants, "I6")
}

// The check constraint forbids this state, so the test drops it in its throwaway database to prove the checker
// would still catch it.
func TestDetectsAvailableBalanceAboveBalance(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "ALTER TABLE accounts DROP CONSTRAINT ck_accounts_user_wallet_balances")
	f.exec(t, "UPDATE accounts SET available_balance = 10001 WHERE id = $1", walletID)

	require.Equal(t, []string{"I4"}, f.invariants(t))
}
```

Create `golang/internal/ledger/reconciliation/run_test.go`:

```go
package reconciliation_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/reconciliation"
)

// scripts/e2e.sh greps these exact log lines.
func TestRunOnceLogsTheSweepSummaryAndEveryViolation(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "UPDATE accounts SET balance = 20000 WHERE id = $1", walletID)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	err := reconciliation.RunOnce(ctx, f.sweeper(f.provider), reconciliation.NewChecker(f.s, log), log)

	require.NoError(t, err)
	require.Contains(t, logs.String(), `"msg":"payout sweep","settled":0,"released":0,"redriven":0,"still_pending":0,"errors":0`)
	require.Contains(t, logs.String(), `"msg":"ledger invariant violated","invariant":"I3"`)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/ledger/reconciliation/...`
Expected: FAIL to compile, with `undefined: reconciliation.NewSweeper`.

- [ ] **Step 3: Implement reconciliation**

Create `golang/internal/ledger/reconciliation/sweeper.go`:

```go
// Package reconciliation repairs payouts that went quiet and checks the ledger invariants.
package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/withdrawals"
)

type Summary struct {
	Settled      int
	Released     int
	Redriven     int
	StillPending int
	Errors       int
}

// Sweeper asks the provider about withdrawals still Pending after the threshold, and settles or releases them
// through the same handlers as the webhooks. A payout the provider has never seen stays held and is re-queued:
// releasing could both pay out and refund if a delayed message still submits it.
type Sweeper struct {
	store            *store.Store
	provider         providers.PaymentProvider
	settle           *withdrawals.SettleHandler
	release          *withdrawals.ReleaseHandler
	clock            common.Clock
	pendingThreshold time.Duration
	log              *slog.Logger
}

func NewSweeper(s *store.Store, provider providers.PaymentProvider, settle *withdrawals.SettleHandler,
	release *withdrawals.ReleaseHandler, clock common.Clock, pendingThreshold time.Duration, log *slog.Logger) *Sweeper {
	return &Sweeper{store: s, provider: provider, settle: settle, release: release, clock: clock, pendingThreshold: pendingThreshold, log: log}
}

func (s *Sweeper) Sweep(ctx context.Context) (Summary, error) {
	stale, err := s.store.StalePendingWithdrawals(ctx, s.clock.Now().Add(-s.pendingThreshold))
	if err != nil {
		return Summary{}, err
	}
	var summary Summary
	for _, transactionID := range stale {
		status, err := s.provider.GetPayoutStatus(ctx, providers.PayoutKey(transactionID))
		if err != nil {
			if ctx.Err() != nil {
				return summary, ctx.Err()
			}
			summary.Errors++
			s.log.Warn("querying the provider for withdrawal failed", "transaction_id", transactionID, "error", err)
			continue
		}
		switch status {
		case providers.StatusSucceeded:
			if _, err := s.settle.Handle(ctx, transactionID); err != nil {
				summary.Errors++
			} else {
				summary.Settled++
			}
		case providers.StatusFailed:
			if _, err := s.release.Handle(ctx, transactionID, "Provider reported the payout failed during reconciliation."); err != nil {
				summary.Errors++
			} else {
				summary.Released++
			}
		case providers.StatusNotFound:
			if err := s.redrive(ctx, transactionID); err != nil {
				summary.Errors++
				s.log.Warn("re-queueing payout failed", "transaction_id", transactionID, "error", err)
			} else {
				summary.Redriven++
			}
		default:
			summary.StillPending++
		}
	}
	return summary, nil
}

// redrive keeps the hold and queues the payout request again. The provider never received it (for example the
// message was dead-lettered) or has lost it. The payout key is idempotent and the processor skips final withdrawals.
func (s *Sweeper) redrive(ctx context.Context, transactionID uuid.UUID) error {
	_, err := store.Run(ctx, s.store, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		withdrawal, err := tx.Transaction(ctx, transactionID)
		if err != nil {
			return struct{}{}, err
		}
		if withdrawal == nil {
			return struct{}{}, fmt.Errorf("withdrawal %s not found", transactionID)
		}
		// A stored provider reference would make the processor skip the resubmission, so it goes in the same save.
		if withdrawal.ProviderReferenceID != nil {
			s.log.Error("provider lost accepted payout; resubmitting it",
				"transaction_id", transactionID, "provider_reference_id", *withdrawal.ProviderReferenceID)
			withdrawal.ClearProviderReference()
		}
		tx.AddOutbox(messages.NewPayoutRequested(transactionID, s.clock.Now()))
		return struct{}{}, nil
	})
	if err == nil {
		s.log.Warn("withdrawal is past the pending threshold but unknown to the provider; keeping the hold and re-queueing the payout",
			"transaction_id", transactionID)
	}
	return err
}
```

Create `golang/internal/ledger/reconciliation/checker.go`:

```go
package reconciliation

import (
	"context"
	"fmt"
	"log/slog"

	"wallet/internal/ledger/store"
)

type Violation struct {
	Invariant string
	Detail    string
}

// Checker checks the ledger invariants and logs every violation. It never corrects anything: drift needs a human.
//   - I1: every posted transaction has exactly two entries, one debit, and debits equal credits.
//   - I3: an account's balance equals the net of its entries.
//   - I4: a user wallet's balances are valid (non-negative, available ≤ balance).
//   - I6: all balances sum to zero.
type Checker struct {
	store *store.Store
	log   *slog.Logger
}

func NewChecker(s *store.Store, log *slog.Logger) *Checker { return &Checker{store: s, log: log} }

func (c *Checker) Check(ctx context.Context) ([]Violation, error) {
	q := c.store.Queries()
	var violations []Violation

	unbalanced, err := q.UnbalancedTransactions(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range unbalanced {
		violations = append(violations, Violation{"I1", fmt.Sprintf("Transaction %s has %d entries (debits %d, credits %d).",
			x.TransactionID, x.EntryCount, x.Debits, x.Credits)})
	}

	drifted, err := q.DriftedAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range drifted {
		violations = append(violations, Violation{"I3", fmt.Sprintf("Account %s balance %d differs from its entries' net %d.",
			x.ID, x.Balance, x.Net)})
	}

	invalid, err := q.InvalidWallets(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range invalid {
		violations = append(violations, Violation{"I4", fmt.Sprintf("Wallet %s has balance %d and available %d.",
			x.ID, x.Balance, x.AvailableBalance)})
	}

	total, err := q.TotalBalance(ctx)
	if err != nil {
		return nil, err
	}
	if total != 0 {
		violations = append(violations, Violation{"I6", fmt.Sprintf("Account balances sum to %d, not 0.", total)})
	}

	for _, v := range violations {
		c.log.Error("ledger invariant violated", "invariant", v.Invariant, "detail", v.Detail)
	}
	return violations, nil
}
```

Create `golang/internal/ledger/reconciliation/run.go`:

```go
package reconciliation

import (
	"context"
	"log/slog"
)

// RunOnce is one reconciliation pass: sweep stale payouts, log the summary, then check the ledger invariants.
func RunOnce(ctx context.Context, sweeper *Sweeper, checker *Checker, log *slog.Logger) error {
	summary, err := sweeper.Sweep(ctx)
	if err != nil {
		return err
	}
	log.Info("payout sweep",
		"settled", summary.Settled,
		"released", summary.Released,
		"redriven", summary.Redriven,
		"still_pending", summary.StillPending,
		"errors", summary.Errors)
	_, err = checker.Check(ctx)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go test ./internal/ledger/reconciliation/...`
Expected: `ok  	wallet/internal/ledger/reconciliation`

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): add payout sweeper and ledger invariant checker"
```

---

### Task 13: RabbitMQ (connection, topology, publisher, consumer)

**Files:**
- Create: `golang/internal/rabbit/conn.go`
- Create: `golang/internal/ledger/payouts/topology.go`, `golang/internal/ledger/payouts/consumer.go`
- Create: `golang/internal/ledger/outbox/rabbit_publisher.go`
- Create: `golang/internal/ledger/ledgertest/rabbitmq.go`
- Test: `golang/internal/rabbit/conn_test.go`
- Test: `golang/internal/ledger/payouts/consumer_test.go` (unit, no broker),
  `golang/internal/ledger/payouts/consumer_rabbit_test.go`
- Test: `golang/internal/ledger/outbox/rabbit_publisher_test.go`

**Interfaces:**
- Consumes: `messages.PayoutRequested`, `messages.PayoutRequestedType`, `outbox.Publisher`, `payouts.Outcome`.
- Produces:
  - **`rabbit` package:** `New(url string, log *slog.Logger) *Conn`, `(*Conn).Channel() (*amqp.Channel, error)`
    (dials when there is no open connection), `(*Conn).IsOpen() bool`, `(*Conn).Close() error`.
  - **`payouts` package:**
    - Constants: `QueueName = "wallet.payouts"`, `DeadLetterExchange = "wallet.payouts.dlx"`,
      `DeadLetterQueue = "wallet.payouts.dead"`, `DeliveryLimit = 5`
    - `func DeclareTopology(ch *amqp.Channel) error`
    - `type PayoutHandler interface{ Process(ctx, messages.PayoutRequested) (Outcome, error) }`
    - `NewConsumer(conn *rabbit.Conn, handler PayoutHandler, log *slog.Logger) *Consumer`, with an exported field
      `RetryDelay time.Duration` (default 2s)
    - `(*Consumer).Run(ctx) error` (reconnects until ctx ends, then returns nil) and
      `(*Consumer).Handle(ctx, amqp.Delivery)`
  - **`outbox` package:** `NewRabbitPublisher(conn *rabbit.Conn) *RabbitPublisher` (implements `Publisher`), and
    `var ErrUnroutable`.
  - **`ledgertest` package:** `NewRabbitMQ(t) *RabbitMQ`, which declares the topology and purges the payout queues.
    Its fields and methods: `.URL`, `.Messages(t, queue) int`, `.Get(t, queue) amqp.Delivery`,
    `.PublishRaw(t, body []byte)`, `.CloseAllConnections(t)` (skips the test if the broker is not a container
    started by `ledgertest`).

- [ ] **Step 1: Add the dependency and write the broker helper**

```bash
cd golang && go get github.com/rabbitmq/amqp091-go@latest github.com/testcontainers/testcontainers-go/modules/rabbitmq@latest
mkdir -p internal/rabbit
```

Create `golang/internal/rabbit/conn.go`:

```go
// Package rabbit holds the RabbitMQ connection shared by the outbox publisher and the payout consumer.
package rabbit

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Conn is a RabbitMQ connection that re-dials on demand. The .NET client recovers connections automatically;
// amqp091-go does not, so callers ask for a new channel whenever theirs has closed and Conn dials again if the
// connection is gone.
type Conn struct {
	url  string
	log  *slog.Logger
	mu   sync.Mutex
	conn *amqp.Connection
}

func New(url string, log *slog.Logger) *Conn { return &Conn{url: url, log: log} }

// Channel opens a channel, dialling first when there is no open connection.
func (c *Conn) Channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.DialConfig(c.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("dial rabbitmq: %w", err)
		}
		c.conn = conn
		c.log.Info("connected to rabbitmq")
	}
	return c.conn.Channel()
}

// IsOpen reports whether the current connection is up. The /health check uses it.
func (c *Conn) IsOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.conn.IsClosed()
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	return c.conn.Close()
}
```

Create `golang/internal/ledger/payouts/topology.go`:

```go
package payouts

import amqp "github.com/rabbitmq/amqp091-go"

// The payout queue is a durable quorum queue. RabbitMQ counts redeliveries and dead-letters a message after
// DeliveryLimit attempts. Declaring is idempotent; the publisher and the consumer both declare before use.
const (
	QueueName          = "wallet.payouts"
	DeadLetterExchange = "wallet.payouts.dlx"
	DeadLetterQueue    = "wallet.payouts.dead"
	DeliveryLimit      = 5
)

func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(DeadLetterExchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(DeadLetterQueue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(DeadLetterQueue, "", DeadLetterExchange, false, nil); err != nil {
		return err
	}
	_, err := ch.QueueDeclare(QueueName, true, false, false, false, amqp.Table{
		"x-queue-type":           "quorum",
		"x-delivery-limit":       int32(DeliveryLimit),
		"x-dead-letter-exchange": DeadLetterExchange,
	})
	return err
}
```

Create `golang/internal/ledger/ledgertest/rabbitmq.go`:

```go
package ledgertest

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"wallet/internal/ledger/payouts"
)

type RabbitMQ struct {
	URL       string
	container *rabbitmq.RabbitMQContainer
}

var (
	rmqOnce sync.Once
	rmq     *RabbitMQ
	rmqErr  error
)

// NewRabbitMQ returns the test broker (WALLET_TEST_RABBITMQ_URL, or a rabbitmq:4-management container started once
// per test binary) with the payout topology declared and its queues purged.
func NewRabbitMQ(t *testing.T) *RabbitMQ {
	t.Helper()
	rmqOnce.Do(func() { rmq, rmqErr = setupRabbitMQ(context.Background()) })
	require.NoError(t, rmqErr)
	rmq.withChannel(t, func(ch *amqp.Channel) {
		require.NoError(t, payouts.DeclareTopology(ch))
		_, err := ch.QueuePurge(payouts.QueueName, false)
		require.NoError(t, err)
		_, err = ch.QueuePurge(payouts.DeadLetterQueue, false)
		require.NoError(t, err)
	})
	return rmq
}

func setupRabbitMQ(ctx context.Context) (*RabbitMQ, error) {
	if url := os.Getenv("WALLET_TEST_RABBITMQ_URL"); url != "" {
		return &RabbitMQ{URL: url}, nil
	}
	container, err := rabbitmq.Run(ctx, "rabbitmq:4-management")
	if err != nil {
		return nil, err
	}
	url, err := container.AmqpURL(ctx)
	if err != nil {
		return nil, err
	}
	return &RabbitMQ{URL: url, container: container}, nil
}

// Messages returns the number of ready messages in queue.
func (r *RabbitMQ) Messages(t *testing.T, queue string) int {
	t.Helper()
	var n int
	r.withChannel(t, func(ch *amqp.Channel) {
		q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
		require.NoError(t, err)
		n = q.Messages
	})
	return n
}

// Get takes one message off queue, waiting up to 10s for it.
func (r *RabbitMQ) Get(t *testing.T, queue string) amqp.Delivery {
	t.Helper()
	var delivery amqp.Delivery
	r.withChannel(t, func(ch *amqp.Channel) {
		require.Eventually(t, func() bool {
			d, ok, err := ch.Get(queue, true)
			require.NoError(t, err)
			delivery = d
			return ok
		}, 10*time.Second, 50*time.Millisecond)
	})
	return delivery
}

// PublishRaw puts body on the payout queue without going through the wallet's publisher.
func (r *RabbitMQ) PublishRaw(t *testing.T, body []byte) {
	t.Helper()
	r.withChannel(t, func(ch *amqp.Channel) {
		require.NoError(t, ch.PublishWithContext(context.Background(), "", payouts.QueueName, false, false,
			amqp.Publishing{Body: body, MessageId: "raw"}))
	})
}

// CloseAllConnections forcibly closes every client connection, as a broker restart would.
func (r *RabbitMQ) CloseAllConnections(t *testing.T) {
	t.Helper()
	if r.container == nil {
		t.Skip("needs a broker container started by ledgertest")
	}
	code, _, err := r.container.Exec(context.Background(), []string{"rabbitmqctl", "close_all_connections", "test"})
	require.NoError(t, err)
	require.Zero(t, code)
}

func (r *RabbitMQ) withChannel(t *testing.T, use func(*amqp.Channel)) {
	t.Helper()
	conn, err := amqp.Dial(r.URL)
	require.NoError(t, err)
	defer conn.Close()
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()
	use(ch)
}
```

- [ ] **Step 2: Write the failing tests**

Create `golang/internal/rabbit/conn_test.go`:

```go
package rabbit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/rabbit"
)

func TestConnDialsLazilyAndRedialsAfterTheBrokerClosesIt(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	t.Cleanup(func() { _ = conn.Close() })
	require.False(t, conn.IsOpen())

	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, ch.Close())
	require.True(t, conn.IsOpen())

	broker.CloseAllConnections(t)
	require.Eventually(t, func() bool { return !conn.IsOpen() }, 10*time.Second, 50*time.Millisecond)

	ch, err = conn.Channel()
	require.NoError(t, err)
	require.NoError(t, ch.Close())
	require.True(t, conn.IsOpen())
}

func TestAnUnreachableBrokerIsAnErrorNotAPanic(t *testing.T) {
	conn := rabbit.New("amqp://guest:guest@127.0.0.1:1/", ledgertest.Logger())

	_, err := conn.Channel()

	require.Error(t, err)
	require.False(t, conn.IsOpen())
}
```

Create `golang/internal/ledger/payouts/consumer_test.go`:

```go
package payouts_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
)

type handlerFunc func(context.Context, messages.PayoutRequested) (payouts.Outcome, error)

func (f handlerFunc) Process(ctx context.Context, m messages.PayoutRequested) (payouts.Outcome, error) {
	return f(ctx, m)
}

type acknowledger struct {
	mu    sync.Mutex
	acks  int
	nacks []bool // requeue flag of each nack
}

func (a *acknowledger) Ack(uint64, bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return nil
}

func (a *acknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nacks = append(a.nacks, requeue)
	return nil
}

func (a *acknowledger) Reject(tag uint64, requeue bool) error { return a.Nack(tag, false, requeue) }

func consumer(handler handlerFunc) *payouts.Consumer {
	c := payouts.NewConsumer(nil, handler, ledgertest.Logger())
	c.RetryDelay = 0
	return c
}

func delivery(a *acknowledger, body string) amqp.Delivery {
	return amqp.Delivery{Acknowledger: a, DeliveryTag: 1, Body: []byte(body)}
}

func validBody() string { return `{"transactionId":"` + uuid.NewString() + `"}` }

func TestHandleAcksAProcessedDelivery(t *testing.T) {
	a := &acknowledger{}
	c := consumer(func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) { return payouts.Initiated, nil })

	c.Handle(ctx, delivery(a, validBody()))

	require.Equal(t, 1, a.acks)
	require.Empty(t, a.nacks)
}

func TestHandleRequeuesAFailedDelivery(t *testing.T) {
	a := &acknowledger{}
	c := consumer(func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) { return "", errors.New("provider down") })

	c.Handle(ctx, delivery(a, validBody()))

	require.Zero(t, a.acks)
	require.Equal(t, []bool{true}, a.nacks)
}

func TestHandleDeadLettersUnreadableDeliveries(t *testing.T) {
	for _, body := range []string{"not json", `{"transactionId":"00000000-0000-0000-0000-000000000000"}`, `{}`} {
		a := &acknowledger{}
		called := false
		c := consumer(func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) { called = true; return "", nil })

		c.Handle(ctx, delivery(a, body))

		require.False(t, called, body)
		require.Equal(t, []bool{false}, a.nacks, body)
	}
}

func TestHandleLeavesADeliveryUnsettledWhenShuttingDown(t *testing.T) {
	a := &acknowledger{}
	shutdown, cancel := context.WithCancel(ctx)
	c := consumer(func(ctx context.Context, _ messages.PayoutRequested) (payouts.Outcome, error) {
		cancel()
		return "", ctx.Err()
	})

	c.Handle(shutdown, delivery(a, validBody()))

	require.Zero(t, a.acks)
	require.Empty(t, a.nacks, "the broker redelivers unacked messages after the channel closes")
}
```

Create `golang/internal/ledger/payouts/consumer_rabbit_test.go`:

```go
package payouts_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

// runConsumer starts a consumer against the test broker and returns a function that stops it.
func runConsumer(t *testing.T, broker *ledgertest.RabbitMQ, handler handlerFunc) (stop func()) {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	c := payouts.NewConsumer(conn, handler, ledgertest.Logger())
	c.RetryDelay = 0
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		require.NoError(t, <-done)
		_ = conn.Close()
	}
	t.Cleanup(stop)
	return stop
}

func publish(t *testing.T, broker *ledgertest.RabbitMQ) {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	defer conn.Close()
	body := []byte(`{"transactionId":"` + uuid.NewString() + `"}`)
	require.NoError(t, outbox.NewRabbitPublisher(conn).Publish(ctx, messages.PayoutRequestedType, body, uuid.New()))
}

func TestTheConsumerAcksAProcessedMessage(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	stop := runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return payouts.Initiated, nil
	})

	publish(t, broker)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 10*time.Second, 50*time.Millisecond)
	stop() // an unacked message would return to the queue when the channel closes

	require.Zero(t, broker.Messages(t, payouts.QueueName))
	require.Zero(t, broker.Messages(t, payouts.DeadLetterQueue))
}

func TestTheConsumerDeadLettersAMessageThatKeepsFailing(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return "", errors.New("provider down")
	})

	publish(t, broker)

	require.Eventually(t, func() bool { return broker.Messages(t, payouts.DeadLetterQueue) == 1 }, 30*time.Second, 100*time.Millisecond)
	require.GreaterOrEqual(t, calls.Load(), int32(payouts.DeliveryLimit))
}

func TestTheConsumerDeadLettersAnUnreadableMessageImmediately(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return payouts.Initiated, nil
	})

	broker.PublishRaw(t, []byte("not json"))

	require.Eventually(t, func() bool { return broker.Messages(t, payouts.DeadLetterQueue) == 1 }, 10*time.Second, 50*time.Millisecond)
	require.Zero(t, calls.Load())
}
```

Create `golang/internal/ledger/outbox/rabbit_publisher_test.go`:

```go
package outbox_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

func newRabbitPublisher(t *testing.T, broker *ledgertest.RabbitMQ) *outbox.RabbitPublisher {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	t.Cleanup(func() { _ = conn.Close() })
	return outbox.NewRabbitPublisher(conn)
}

func TestTheRabbitPublisherDeliversPersistentConfirmedMessages(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	publisher := newRabbitPublisher(t, broker)
	id := uuid.New()

	require.NoError(t, publisher.Publish(ctx, messages.PayoutRequestedType, []byte(`{"transactionId":"x"}`), id))

	delivered := broker.Get(t, payouts.QueueName)
	require.Equal(t, id.String(), delivered.MessageId)
	require.Equal(t, messages.PayoutRequestedType, delivered.Type)
	require.Equal(t, "application/json", delivered.ContentType)
	require.Equal(t, amqp.Persistent, delivered.DeliveryMode)
	require.JSONEq(t, `{"transactionId":"x"}`, string(delivered.Body))
}

func TestTheRabbitPublisherRejectsUnknownMessageTypes(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)

	err := newRabbitPublisher(t, broker).Publish(ctx, "test.message", []byte("{}"), uuid.New())

	require.ErrorContains(t, err, "no queue is configured")
}

func TestRabbitPublisherRecoversAfterTheConnectionDrops(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	publisher := newRabbitPublisher(t, broker)
	require.NoError(t, publisher.Publish(ctx, messages.PayoutRequestedType, []byte("{}"), uuid.New()))

	broker.CloseAllConnections(t)

	require.Eventually(t, func() bool {
		return publisher.Publish(ctx, messages.PayoutRequestedType, []byte("{}"), uuid.New()) == nil
	}, 20*time.Second, 200*time.Millisecond)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/rabbit/... ./internal/ledger/payouts/... ./internal/ledger/outbox/...`
Expected: FAIL to compile, with `undefined: payouts.NewConsumer`, `undefined: outbox.NewRabbitPublisher`.

- [ ] **Step 4: Implement the consumer and the publisher**

Create `golang/internal/ledger/payouts/consumer.go`:

```go
package payouts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"wallet/internal/ledger/messages"
	"wallet/internal/rabbit"
)

const (
	prefetch   = 10
	maxBackoff = 30 * time.Second
)

// PayoutHandler is what the consumer feeds; Processor implements it.
type PayoutHandler interface {
	Process(ctx context.Context, message messages.PayoutRequested) (Outcome, error)
}

// Consumer feeds wallet.payouts messages to the handler one at a time. It acks on success. On failure it nacks with
// requeue, so the quorum queue dead-letters the message after the delivery limit. Unreadable messages go straight to
// the dead-letter queue.
type Consumer struct {
	conn    *rabbit.Conn
	handler PayoutHandler
	log     *slog.Logger
	// RetryDelay is a short pause before a requeue so a struggling provider isn't hammered.
	RetryDelay time.Duration
}

func NewConsumer(conn *rabbit.Conn, handler PayoutHandler, log *slog.Logger) *Consumer {
	return &Consumer{conn: conn, handler: handler, log: log, RetryDelay: 2 * time.Second}
}

// Run consumes until ctx ends, reconnecting with exponential backoff whenever the connection or channel drops.
func (c *Consumer) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		subscribed, err := c.consume(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if subscribed {
			backoff = time.Second
		}
		c.log.Warn("payout consumer disconnected; reconnecting", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (c *Consumer) consume(ctx context.Context) (subscribed bool, err error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return false, err
	}
	defer ch.Close()
	if err := DeclareTopology(ch); err != nil {
		return false, err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return false, err
	}
	deliveries, err := ch.ConsumeWithContext(ctx, QueueName, "", false, false, false, false, nil)
	if err != nil {
		return false, err
	}
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return true, errors.New("delivery channel closed")
			}
			c.Handle(ctx, delivery)
		}
	}
}

// Handle processes one delivery and settles it with the broker.
func (c *Consumer) Handle(ctx context.Context, delivery amqp.Delivery) {
	var message messages.PayoutRequested
	if err := json.Unmarshal(delivery.Body, &message); err != nil || message.TransactionID == uuid.Nil {
		c.log.Error("dead-lettering unreadable payout message", "message_id", delivery.MessageId, "error", err)
		c.settle(delivery.Nack(false, false))
		return
	}
	outcome, err := c.handler.Process(ctx, message)
	if err == nil {
		c.log.Info("payout processed", "transaction_id", message.TransactionID, "outcome", outcome)
		c.settle(delivery.Ack(false))
		return
	}
	if ctx.Err() != nil {
		return // shutting down: the broker redelivers unacked messages once the channel closes
	}
	c.log.Warn("payout failed; requeueing", "transaction_id", message.TransactionID, "error", err)
	select {
	case <-ctx.Done():
		return
	case <-time.After(c.RetryDelay):
	}
	c.settle(delivery.Nack(false, true))
}

func (c *Consumer) settle(err error) {
	if err != nil {
		c.log.Warn("settling payout delivery failed", "error", err)
	}
}
```

Create `golang/internal/ledger/outbox/rabbit_publisher.go`:

```go
package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

// ErrUnroutable means the broker returned a mandatory message because no queue was bound for it.
var ErrUnroutable = errors.New("rabbitmq returned the message as unroutable")

const confirmTimeout = 10 * time.Second

// RabbitPublisher publishes outbox messages with publisher confirms, so Publish returns only after the broker has
// the message. Publishes are serialised on one confirm-mode channel, which is reopened when it closes.
type RabbitPublisher struct {
	conn    *rabbit.Conn
	mu      sync.Mutex
	ch      *amqp.Channel
	returns chan amqp.Return
}

func NewRabbitPublisher(conn *rabbit.Conn) *RabbitPublisher { return &RabbitPublisher{conn: conn} }

func (p *RabbitPublisher) Publish(ctx context.Context, messageType string, payload []byte, messageID uuid.UUID) error {
	if messageType != messages.PayoutRequestedType {
		return fmt.Errorf("no queue is configured for message type %q", messageType)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, err := p.channel()
	if err != nil {
		return err
	}
	for len(p.returns) > 0 { // drop a stale return so it can't be blamed on this message
		<-p.returns
	}

	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", payouts.QueueName, true, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		MessageId:    messageID.String(),
		Type:         messageType,
		ContentType:  "application/json",
		Body:         payload,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", messageID, err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm %s: %w", messageID, err)
	}
	if !acked {
		return fmt.Errorf("broker nacked message %s", messageID)
	}
	// The broker sends basic.return before the ack, and amqp091 delivers it to p.returns before resolving the
	// confirmation, so a return for this message is already buffered.
	select {
	case returned := <-p.returns:
		return fmt.Errorf("%w: %s (%d %s)", ErrUnroutable, messageID, returned.ReplyCode, returned.ReplyText)
	default:
		return nil
	}
}

func (p *RabbitPublisher) channel() (*amqp.Channel, error) {
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	ch, err := p.conn.Channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	if err := payouts.DeclareTopology(ch); err != nil {
		_ = ch.Close()
		return nil, err
	}
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 1))
	p.ch = ch
	return ch, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go test ./internal/rabbit/... ./internal/ledger/payouts/... ./internal/ledger/outbox/...`
Expected: all three `ok`. The first run pulls `rabbitmq:4-management`.

If `TestTheConsumerDeadLettersAMessageThatKeepsFailing` times out, the broker is not counting nack-requeue redeliveries
toward `x-delivery-limit`. Check the queue arguments with
`docker exec <container> rabbitmqctl list_queues name arguments`. Do not raise the timeout until you know why.

- [ ] **Step 6: Commit**

```bash
git add golang
git commit -m "feat(golang): add RabbitMQ connection, publisher with confirms and payout consumer"
```

---

### Task 14: Configuration and the HTTP API

**Files:**
- Create: `golang/internal/config/config.go`
- Create in `golang/internal/api/`: `router.go`, `problem.go`, `decode.go`, `wallets.go`, `topups.go`,
  `withdrawals.go`, `webhooks.go`, `dev.go`, `health.go`, `recover.go`
- Test: `golang/internal/config/config_test.go`
- Test in `golang/internal/api/`: `problem_test.go`, `api_test.go`

**Interfaces:**
- Consumes: every handler from Tasks 6–9, `providers.FakePaymentProvider`, `common.Error`, `domain.Validation`.
- Produces:
  - **`config` package:**
    - `type Config struct{ AppEnv, HTTPAddr, DatabaseURL, RabbitMQURL string; Wallet Wallet }`
    - `type Wallet struct{ OutboxPollInterval time.Duration; OutboxBatchSize int; SweeperInterval, PendingThreshold time.Duration }`
    - `func Load() (Config, error)`, `(Config).IsDevelopment() bool`
  - **`api` package:**
    - `type HealthCheck func(ctx context.Context) error`
    - `type Deps struct{ Log *slog.Logger; Development bool; CreateWallet *wallets.CreateHandler; GetWallet *wallets.GetHandler; ListTransactions *wallets.ListHandler; InitiateTopUp *topups.InitiateHandler; RequestWithdrawal *withdrawals.RequestHandler; Webhooks *webhooks.Handler; FakeProvider *providers.FakePaymentProvider; HealthChecks []HealthCheck }`
    - `func NewRouter(d Deps) http.Handler`
    - `func StatusFor(code string) int`
    - `func RecoverPanics(log *slog.Logger, next http.Handler) http.Handler`

- [ ] **Step 1: Add the dependency and write the failing tests**

```bash
cd golang && mkdir -p internal/config internal/api && go get github.com/caarlos0/env/v11@latest
```

Create `golang/internal/config/config_test.go`:

```go
package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/config"
)

func TestDefaultsMatchTheDotnetWalletOptions(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/wallet")

	cfg, err := config.Load()

	require.NoError(t, err)
	require.Equal(t, "production", cfg.AppEnv)
	require.False(t, cfg.IsDevelopment())
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, time.Second, cfg.Wallet.OutboxPollInterval)
	require.Equal(t, 50, cfg.Wallet.OutboxBatchSize)
	require.Equal(t, 5*time.Minute, cfg.Wallet.SweeperInterval)
	require.Equal(t, 15*time.Minute, cfg.Wallet.PendingThreshold)
}

func TestEnvironmentVariablesOverrideTheDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/wallet")
	t.Setenv("APP_ENV", "development")
	t.Setenv("WALLET_PENDING_THRESHOLD", "30m")
	t.Setenv("WALLET_SWEEPER_INTERVAL", "5s")

	cfg, err := config.Load()

	require.NoError(t, err)
	require.True(t, cfg.IsDevelopment())
	require.Equal(t, 30*time.Minute, cfg.Wallet.PendingThreshold)
	require.Equal(t, 5*time.Second, cfg.Wallet.SweeperInterval)
}

func TestTheDatabaseURLIsRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	_, err := config.Load()

	require.ErrorContains(t, err, "DATABASE_URL")
}
```

Create `golang/internal/api/problem_test.go`:

```go
package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/api"
)

func TestErrorCodesMapToHTTPStatuses(t *testing.T) {
	cases := map[string]int{
		"Money.InvalidAmount":            400,
		"Money.UnsupportedCurrency":      400,
		"Validation.AccountNumber":       400,
		"Wallet.NotFound":                404,
		"Transaction.NotFound":           404,
		"Transaction.DuplicateReference": 409,
		"Concurrency.Exhausted":          409,
		"Ledger.HoldMissing":             409,
		"Wallet.InsufficientFunds":       422,
		"Something.Unexpected":           500,
	}
	for code, status := range cases {
		require.Equal(t, status, api.StatusFor(code), code)
	}
}

func TestRecoverPanicsReturnsA500Problem(t *testing.T) {
	var logs bytes.Buffer
	calls := 0
	handler := api.RecoverPanics(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			panic("transaction is already Succeeded")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/webhooks/provider", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/webhooks/provider", nil))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, "application/problem+json", first.Header().Get("Content-Type"))
	require.NotContains(t, first.Body.String(), "already Succeeded", "internal detail must not leak")
	require.Contains(t, logs.String(), `"msg":"unhandled error"`)
	require.Equal(t, http.StatusNoContent, second.Code)
}
```

Create `golang/internal/api/api_test.go`:

```go
package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/api"
	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

type server struct {
	url      string
	provider *providers.FakePaymentProvider
}

func newServer(t *testing.T, development bool, checks ...api.HealthCheck) *server {
	t.Helper()
	s := ledgertest.NewStore(t)
	clock := common.SystemClock{}
	log := ledgertest.Logger()
	provider := providers.NewFakePaymentProvider()
	router := api.NewRouter(api.Deps{
		Log:               log,
		Development:       development,
		CreateWallet:      wallets.NewCreateHandler(s, clock),
		GetWallet:         wallets.NewGetHandler(s),
		ListTransactions:  wallets.NewListHandler(s),
		InitiateTopUp:     topups.NewInitiateHandler(s, provider, clock),
		RequestWithdrawal: withdrawals.NewRequestHandler(s, clock),
		Webhooks: webhooks.NewHandler(s, topups.NewCompleteHandler(s, clock, log),
			withdrawals.NewSettleHandler(s, clock), withdrawals.NewReleaseHandler(s, clock, log)),
		FakeProvider: provider,
		HealthChecks: checks,
	})
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	return &server{url: ts.URL, provider: provider}
}

// do sends body as-is when it is a string, otherwise as JSON.
func (s *server) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		encoded, err := json.Marshal(b)
		require.NoError(t, err)
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, s.url+path, reader)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func read[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	var value T
	require.NoError(t, json.NewDecoder(response.Body).Decode(&value))
	return value
}

func title(t *testing.T, response *http.Response) string {
	t.Helper()
	require.Equal(t, "application/problem+json", response.Header.Get("Content-Type"))
	return read[struct{ Title string }](t, response).Title
}

func withdrawal(referenceID string, amount int64) map[string]any {
	return map[string]any{"referenceId": referenceID, "amount": amount, "bankCode": "058",
		"accountNumber": "0123456789", "accountName": "Ada Lovelace"}
}

func (s *server) fund(t *testing.T, userID uuid.UUID, amount int64) {
	t.Helper()
	started := s.do(t, http.MethodPost, "/users/"+userID.String()+"/top-ups",
		map[string]any{"referenceId": "topup-" + uuid.NewString(), "amount": amount})
	require.Equal(t, http.StatusCreated, started.StatusCode)
	topUp := read[wallets.TransactionResponse](t, started)
	confirmed := s.do(t, http.MethodPost, "/webhooks/provider",
		map[string]any{"event": "charge.success", "providerReferenceId": *topUp.ProviderReferenceID, "amount": amount})
	require.Equal(t, http.StatusOK, confirmed.StatusCode)
}

func TestCreatingAWalletReturns201Then200(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()

	created := s.do(t, http.MethodPost, "/users/"+userID.String()+"/wallet", nil)
	existing := s.do(t, http.MethodPost, "/users/"+userID.String()+"/wallet", nil)

	require.Equal(t, http.StatusCreated, created.StatusCode)
	require.Equal(t, "/users/"+userID.String()+"/wallet", created.Header.Get("Location"))
	require.Equal(t, http.StatusOK, existing.StatusCode)
	require.Equal(t, "NGN", read[wallets.WalletResponse](t, existing).Currency)
}

func TestAnUnknownWalletReturnsA404Problem(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodGet, "/users/"+uuid.NewString()+"/wallet", nil)

	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.Equal(t, "Wallet.NotFound", title(t, response))
}

func TestATopUpConfirmedByWebhookCreditsTheWallet(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()

	s.fund(t, userID, 5_000)

	wallet := read[wallets.WalletResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet", nil))
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	history := read[[]wallets.TransactionResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet/transactions", nil))
	require.Len(t, history, 1)
	require.Equal(t, "TopUp", history[0].Type)
	require.Equal(t, "Succeeded", history[0].Status)
}

func TestAWithdrawalReturns202AndAnIdenticalRetryReturns200(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 10_000)

	first := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-retry", 4_000))
	retry := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-retry", 4_000))

	require.Equal(t, http.StatusAccepted, first.StatusCode)
	require.Equal(t, http.StatusOK, retry.StatusCode)
	require.Equal(t, read[wallets.TransactionResponse](t, first).ID, read[wallets.TransactionResponse](t, retry).ID)
	wallet := read[wallets.WalletResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet", nil))
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
}

func TestAWithdrawalBeyondTheAvailableBalanceReturns422(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 1_000)

	response := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-big", 1_001))

	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	require.Equal(t, "Wallet.InsufficientFunds", title(t, response))
}

func TestReusingAReferenceWithADifferentBodyReturns409(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 10_000)
	s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-reused", 1_000))

	response := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-reused", 2_000))

	require.Equal(t, http.StatusConflict, response.StatusCode)
	require.Equal(t, "Transaction.DuplicateReference", title(t, response))
}

func TestANonPositiveAmountReturns400(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/users/"+uuid.NewString()+"/top-ups",
		map[string]any{"referenceId": "zero", "amount": 0})

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Money.InvalidAmount", title(t, response))
}

func TestAnUnknownWebhookEventReturns400(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/webhooks/provider",
		map[string]any{"event": "refund.created", "providerReferenceId": "x", "amount": 1})

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Validation.Event", title(t, response))
}

func TestTheDevProviderEndpointIsHiddenOutsideDevelopment(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/dev/provider/chg_anything/complete?outcome=success", nil)

	require.Equal(t, http.StatusNotFound, response.StatusCode)
}

func TestTheDevProviderEndpointCompletesATopUpInDevelopment(t *testing.T) {
	s, userID := newServer(t, true), uuid.New()
	started := read[wallets.TransactionResponse](t, s.do(t, http.MethodPost, "/users/"+userID.String()+"/top-ups",
		map[string]any{"referenceId": "tu-1", "amount": 500}))
	path := "/dev/provider/" + *started.ProviderReferenceID + "/complete"

	bad := s.do(t, http.MethodPost, path+"?outcome=maybe", nil)
	unknown := s.do(t, http.MethodPost, "/dev/provider/chg_nope/complete?outcome=success", nil)
	completed := s.do(t, http.MethodPost, path+"?outcome=success", nil)

	require.Equal(t, http.StatusBadRequest, bad.StatusCode)
	require.Equal(t, "Validation.Outcome", title(t, bad))
	require.Equal(t, http.StatusNotFound, unknown.StatusCode)
	require.Equal(t, http.StatusOK, completed.StatusCode)
	require.Equal(t, "Succeeded", read[wallets.TransactionResponse](t, completed).Status)
}

func TestAliveAndHealthAreServedInEveryEnvironment(t *testing.T) {
	healthy := newServer(t, false, func(context.Context) error { return nil })
	unhealthy := newServer(t, false, func(context.Context) error { return errors.New("rabbitmq connection is closed") })

	alive := healthy.do(t, http.MethodGet, "/alive", nil)
	ok := healthy.do(t, http.MethodGet, "/health", nil)
	down := unhealthy.do(t, http.MethodGet, "/health", nil)

	require.Equal(t, http.StatusOK, alive.StatusCode)
	require.Equal(t, http.StatusOK, ok.StatusCode)
	okBody, _ := io.ReadAll(ok.Body)
	require.Equal(t, "Healthy", string(okBody))
	require.Equal(t, http.StatusServiceUnavailable, down.StatusCode)
	downBody, _ := io.ReadAll(down.Body)
	require.Equal(t, "Unhealthy", string(downBody))
}

func TestThereIsNoRootRouteAndNonUUIDUsersAreNotFound(t *testing.T) {
	s := newServer(t, false)

	require.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, "/", nil).StatusCode)
	require.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, "/users/not-a-guid/wallet", nil).StatusCode)
}

func TestBadBodiesAreValidationProblems(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	topUps, withdrawals := "/users/"+userID.String()+"/top-ups", "/users/"+userID.String()+"/withdrawals"
	cases := []struct{ path, body, title string }{
		{topUps, `{"referenceId":`, "Validation.Body"},
		{topUps, `{"referenceId":"x","amount":"lots"}`, "Validation.Body"},
		{topUps, `{"referenceId":"x","amount":5} trailing`, "Validation.Body"},
		{topUps, `{"amount":5}`, "Validation.ReferenceId"},
		{topUps, `{"referenceId":null,"amount":5}`, "Validation.ReferenceId"},
		{topUps, `{"referenceId":"x"}`, "Validation.Amount"},
		{topUps, `null`, "Validation.ReferenceId"},
		{topUps, ``, "Validation.Body"},
		{withdrawals, `{"referenceId":"w","amount":5,"accountNumber":"0123456789","accountName":"A"}`, "Validation.BankCode"},
		{"/webhooks/provider", `{"providerReferenceId":"x","amount":1}`, "Validation.Event"},
		{"/webhooks/provider", `{"event":"charge.success","amount":1}`, "Validation.ProviderReferenceId"},
	}
	for _, c := range cases {
		response := s.do(t, http.MethodPost, c.path, c.body)

		require.Equal(t, http.StatusBadRequest, response.StatusCode, c.body)
		require.Equal(t, c.title, title(t, response), c.body)
	}
}

func TestAnOversizedBodyIsRejected(t *testing.T) {
	s := newServer(t, false)
	// Just over the 1 MiB limit: the server drains the small remainder instead of resetting the connection mid-upload.
	body := `{"referenceId":"` + strings.Repeat("x", 1<<20) + `","amount":5}`

	response := s.do(t, http.MethodPost, "/users/"+uuid.NewString()+"/top-ups", body)

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Validation.Body", title(t, response))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/config/... ./internal/api/...`
Expected: FAIL to compile, with `undefined: config.Load`, `undefined: api.NewRouter`.

- [ ] **Step 3: Implement config**

Create `golang/internal/config/config.go`:

```go
// Package config reads the service configuration from environment variables.
package config

import (
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	// AppEnv "development" applies migrations on startup and enables /dev/provider.
	AppEnv      string `env:"APP_ENV" envDefault:"production"`
	HTTPAddr    string `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	// RabbitMQURL is checked by app.New rather than here, so `wallet-api migrate` runs without it.
	RabbitMQURL string `env:"RABBITMQ_URL"`
	Wallet      Wallet `envPrefix:"WALLET_"`
}

// Wallet holds the background worker settings; the defaults are the .NET WalletOptions defaults.
type Wallet struct {
	OutboxPollInterval time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"1s"`
	OutboxBatchSize    int           `env:"OUTBOX_BATCH_SIZE" envDefault:"50"`
	SweeperInterval    time.Duration `env:"SWEEPER_INTERVAL" envDefault:"5m"`
	// PendingThreshold is how long a withdrawal may stay Pending before the sweeper asks the provider about it.
	PendingThreshold time.Duration `env:"PENDING_THRESHOLD" envDefault:"15m"`
}

func Load() (Config, error) { return env.ParseAs[Config]() }

func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }
```

- [ ] **Step 4: Implement the API**

Create `golang/internal/api/router.go`:

```go
// Package api is the HTTP surface: the same routes, status codes and problem responses as the .NET controllers.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

// HealthCheck returns an error when a dependency is unusable.
type HealthCheck func(ctx context.Context) error

type Deps struct {
	Log               *slog.Logger
	Development       bool
	CreateWallet      *wallets.CreateHandler
	GetWallet         *wallets.GetHandler
	ListTransactions  *wallets.ListHandler
	InitiateTopUp     *topups.InitiateHandler
	RequestWithdrawal *withdrawals.RequestHandler
	Webhooks          *webhooks.Handler
	FakeProvider      *providers.FakePaymentProvider
	HealthChecks      []HealthCheck
}

type api struct {
	Deps
}

func NewRouter(d Deps) http.Handler {
	a := &api{d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users/{userId}/wallet", a.createWallet)
	mux.HandleFunc("GET /users/{userId}/wallet", a.getWallet)
	mux.HandleFunc("GET /users/{userId}/wallet/transactions", a.listTransactions)
	mux.HandleFunc("POST /users/{userId}/top-ups", a.initiateTopUp)
	mux.HandleFunc("POST /users/{userId}/withdrawals", a.requestWithdrawal)
	mux.HandleFunc("POST /webhooks/provider", a.receiveWebhook)
	mux.HandleFunc("GET /alive", a.alive)
	mux.HandleFunc("GET /health", a.health)
	if d.Development {
		mux.HandleFunc("POST /dev/provider/{providerReferenceId}/complete", a.completeDevProvider)
	}
	return RecoverPanics(d.Log, mux)
}

// userID parses the {userId} path segment. Like the .NET :guid route constraint, a non-UUID is a plain 404.
func userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		http.NotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}
```

Create `golang/internal/api/problem.go`:

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"wallet/internal/common"
)

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// StatusFor maps a stable error code to its HTTP status, exactly like .NET's ProblemResults.StatusCodeFor.
func StatusFor(code string) int {
	switch {
	case code == "Money.InvalidAmount", code == "Money.UnsupportedCurrency":
		return http.StatusBadRequest
	case code == "Wallet.NotFound", code == "Transaction.NotFound":
		return http.StatusNotFound
	case code == "Transaction.DuplicateReference", code == "Concurrency.Exhausted":
		return http.StatusConflict
	case code == "Wallet.InsufficientFunds":
		return http.StatusUnprocessableEntity
	case strings.HasPrefix(code, "Validation."):
		return http.StatusBadRequest
	case strings.HasPrefix(code, "Ledger."):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeError turns a handler's *common.Error into an RFC 7807 problem whose title is the stable error code. Anything
// else is unexpected: it is logged and answered with a generic 500.
func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var e *common.Error
	if errors.As(err, &e) {
		writeProblem(w, StatusFor(e.Code), e.Code, e.Message)
		return
	}
	a.Log.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeProblem(w, http.StatusInternalServerError, "Internal.Error", "An unexpected error occurred.")
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
```

Create `golang/internal/api/decode.go`:

```go
package api

import (
	"encoding/json"
	"net/http"

	"wallet/internal/ledger/domain"
)

const maxBodyBytes = 1 << 20

var errBadBody = domain.Validation("Body", "The request body is not valid JSON for this endpoint.")

// decode reads exactly one JSON value of at most 1 MiB into dst.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(dst); err != nil {
		return errBadBody
	}
	if decoder.More() {
		return errBadBody
	}
	return nil
}

// required unwraps a JSON field that must be present and non-null, like a .NET `required` property.
func required[T any](value *T, field string) (T, error) {
	if value == nil {
		var zero T
		return zero, domain.Validation(field, field+" is required.")
	}
	return *value, nil
}
```

`decoder.More()` returns true for trailing non-whitespace such as `trailing`. That is what the test case
`{"referenceId":"x","amount":5} trailing` relies on.

Create `golang/internal/api/wallets.go`:

```go
package api

import "net/http"

func (a *api) createWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	result, err := a.CreateWallet.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if !result.Created {
		writeJSON(w, http.StatusOK, result.Wallet)
		return
	}
	w.Header().Set("Location", "/users/"+id.String()+"/wallet")
	writeJSON(w, http.StatusCreated, result.Wallet)
}

func (a *api) getWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	wallet, err := a.GetWallet.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (a *api) listTransactions(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	transactions, err := a.ListTransactions.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactions)
}
```

Create `golang/internal/api/topups.go`:

```go
package api

import (
	"net/http"

	"wallet/internal/ledger/topups"
)

type topUpRequest struct {
	ReferenceID *string `json:"referenceId"`
	Amount      *int64  `json:"amount"`
}

func (a *api) initiateTopUp(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	command, err := topUpCommand(w, r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	command.UserID = id
	result, err := a.InitiateTopUp.Handle(r.Context(), command)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if result.Replayed {
		writeJSON(w, http.StatusOK, result.Transaction)
		return
	}
	w.Header().Set("Location", "/users/"+id.String()+"/wallet/transactions")
	writeJSON(w, http.StatusCreated, result.Transaction)
}

func topUpCommand(w http.ResponseWriter, r *http.Request) (topups.InitiateCommand, error) {
	var body topUpRequest
	if err := decode(w, r, &body); err != nil {
		return topups.InitiateCommand{}, err
	}
	referenceID, err := required(body.ReferenceID, "ReferenceId")
	if err != nil {
		return topups.InitiateCommand{}, err
	}
	amount, err := required(body.Amount, "Amount")
	if err != nil {
		return topups.InitiateCommand{}, err
	}
	return topups.InitiateCommand{ReferenceID: referenceID, Amount: amount}, nil
}
```

Create `golang/internal/api/withdrawals.go`:

```go
package api

import (
	"net/http"

	"wallet/internal/ledger/withdrawals"
)

type withdrawalRequest struct {
	ReferenceID   *string `json:"referenceId"`
	Amount        *int64  `json:"amount"`
	BankCode      *string `json:"bankCode"`
	AccountNumber *string `json:"accountNumber"`
	AccountName   *string `json:"accountName"`
}

func (a *api) requestWithdrawal(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	command, err := withdrawalCommand(w, r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	command.UserID = id
	result, err := a.RequestWithdrawal.Handle(r.Context(), command)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if result.Replayed {
		writeJSON(w, http.StatusOK, result.Transaction)
		return
	}
	writeJSON(w, http.StatusAccepted, result.Transaction)
}

func withdrawalCommand(w http.ResponseWriter, r *http.Request) (withdrawals.RequestCommand, error) {
	var body withdrawalRequest
	if err := decode(w, r, &body); err != nil {
		return withdrawals.RequestCommand{}, err
	}
	var command withdrawals.RequestCommand
	var err error
	if command.ReferenceID, err = required(body.ReferenceID, "ReferenceId"); err != nil {
		return command, err
	}
	if command.Amount, err = required(body.Amount, "Amount"); err != nil {
		return command, err
	}
	if command.BankCode, err = required(body.BankCode, "BankCode"); err != nil {
		return command, err
	}
	if command.AccountNumber, err = required(body.AccountNumber, "AccountNumber"); err != nil {
		return command, err
	}
	if command.AccountName, err = required(body.AccountName, "AccountName"); err != nil {
		return command, err
	}
	return command, nil
}
```

Create `golang/internal/api/webhooks.go`:

```go
package api

import (
	"net/http"

	"wallet/internal/ledger/webhooks"
)

type webhookRequest struct {
	Event               *string `json:"event"`
	ProviderReferenceID *string `json:"providerReferenceId"`
	Reference           *string `json:"reference"`
	Amount              int64   `json:"amount"`
}

func (a *api) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	var body webhookRequest
	if err := decode(w, r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	event, err := required(body.Event, "Event")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	providerReferenceID, err := required(body.ProviderReferenceID, "ProviderReferenceId")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.Webhooks.Handle(r.Context(), webhooks.Webhook{
		Event: event, ProviderReferenceID: providerReferenceID, Reference: body.Reference, Amount: body.Amount,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

Create `golang/internal/api/dev.go`:

```go
package api

import (
	"net/http"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/webhooks"
)

// completeDevProvider plays the fake provider's webhook so the whole flow can be tried by hand. Registered only in
// development.
func (a *api) completeDevProvider(w http.ResponseWriter, r *http.Request) {
	var succeeded bool
	switch r.URL.Query().Get("outcome") {
	case "success":
		succeeded = true
	case "failed":
		succeeded = false
	default:
		a.writeError(w, r, domain.Validation("Outcome", "outcome must be 'success' or 'failed'."))
		return
	}
	record, ok := a.FakeProvider.FindByProviderReference(r.PathValue("providerReferenceId"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	event := webhooks.ChargeFailed
	switch {
	case record.IsPayout && succeeded:
		event = webhooks.TransferSuccess
	case record.IsPayout:
		event = webhooks.TransferFailed
	case succeeded:
		event = webhooks.ChargeSuccess
	}
	if record.IsPayout {
		status := providers.StatusFailed
		if succeeded {
			status = providers.StatusSucceeded
		}
		a.FakeProvider.SetPayoutStatus(record.ReferenceID, status)
	}
	reference := record.ReferenceID
	result, err := a.Webhooks.Handle(r.Context(), webhooks.Webhook{
		Event: event, ProviderReferenceID: record.ProviderReferenceID, Reference: &reference, Amount: record.Amount,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

Create `golang/internal/api/health.go`:

```go
package api

import (
	"context"
	"net/http"
	"time"
)

// alive is liveness: the process is up and serving. It checks no dependencies.
func (a *api) alive(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "Healthy")
}

// health is readiness: every dependency check must pass. The plain-text body matches ASP.NET health checks.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, check := range a.HealthChecks {
		if err := check(ctx); err != nil {
			a.Log.Warn("health check failed", "error", err)
			writeText(w, http.StatusServiceUnavailable, "Unhealthy")
			return
		}
	}
	writeText(w, http.StatusOK, "Healthy")
}
```

Create `golang/internal/api/recover.go`:

```go
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverPanics turns a panic in next into a logged 500 problem, so one bad request never takes the server's
// response with it.
func RecoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			log.Error("unhandled error", "method", r.Method, "path", r.URL.Path,
				"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			writeProblem(w, http.StatusInternalServerError, "Internal.Error", "An unexpected error occurred.")
		}()
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go test ./internal/config/... ./internal/api/...`
Expected: both `ok`.

- [ ] **Step 6: Commit**

```bash
git add golang
git commit -m "feat(golang): add env config and the HTTP API"
```

---

### Task 15: Composition root (`internal/app`) and `cmd/wallet-api`

**Files:**
- Create: `golang/internal/app/app.go`
- Create: `golang/cmd/wallet-api/main.go`
- Test: `golang/internal/app/app_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–14.
- Produces (package `app`):
  - `New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error)`. It opens the pool, migrates in
    development, and binds `cfg.HTTPAddr` immediately, so `127.0.0.1:0` works in tests.
  - `(*App).Addr() string`
  - `(*App).Run(ctx) error`. It serves HTTP and runs the outbox relay, payout consumer and reconciliation loops until
    ctx ends, then shuts down gracefully and returns nil.

- [ ] **Step 1: Add the dependency and write the failing tests**

```bash
cd golang && mkdir -p internal/app cmd/wallet-api && go get golang.org/x/sync@latest
```

Create `golang/internal/app/app_test.go`:

```go
package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"wallet/internal/app"
	"wallet/internal/config"
	"wallet/internal/ledger/ledgertest"
)

type running struct {
	base string
	pool *pgxpool.Pool
	stop func() error
}

func start(t *testing.T, rabbitURL string) *running {
	t.Helper()
	pool := ledgertest.NewPool(t)
	cfg := config.Config{
		AppEnv: "development", HTTPAddr: "127.0.0.1:0",
		DatabaseURL: pool.Config().ConnString(), RabbitMQURL: rabbitURL,
		Wallet: config.Wallet{
			OutboxPollInterval: 50 * time.Millisecond, OutboxBatchSize: 50,
			SweeperInterval: time.Hour, PendingThreshold: 15 * time.Minute,
		},
	}
	a, err := app.New(context.Background(), cfg, ledgertest.Logger())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(15 * time.Second):
				stopErr = errors.New("Run did not return within 15s of cancellation")
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	return &running{base: "http://" + a.Addr(), pool: pool, stop: stop}
}

func (r *running) call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, r.base+path, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(text)
}

func (r *running) fundAndWithdraw(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	status, body := r.call(t, http.MethodPost, "/users/"+userID.String()+"/top-ups", `{"referenceId":"tu-1","amount":9000}`)
	require.Equal(t, http.StatusCreated, status, body)
	var topUp struct{ ProviderReferenceID string `json:"providerReferenceId"` }
	require.NoError(t, json.Unmarshal([]byte(body), &topUp))
	status, body = r.call(t, http.MethodPost, "/webhooks/provider",
		`{"event":"charge.success","providerReferenceId":"`+topUp.ProviderReferenceID+`","amount":9000}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = r.call(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals",
		`{"referenceId":"wd-1","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}`)
	require.Equal(t, http.StatusAccepted, status, body)
	var withdrawal struct{ ID string `json:"id"` }
	require.NoError(t, json.Unmarshal([]byte(body), &withdrawal))
	return withdrawal.ID
}

func TestRunServesAndShutsDownCleanly(t *testing.T) {
	r := start(t, ledgertest.NewRabbitMQ(t).URL)

	status, body := r.call(t, http.MethodGet, "/alive", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Healthy", body)
	require.Eventually(t, func() bool {
		status, _ := r.call(t, http.MethodGet, "/health", "")
		return status == http.StatusOK
	}, 15*time.Second, 100*time.Millisecond)

	require.NoError(t, r.stop())
}

func TestAWithdrawalFlowsThroughRabbitMQToTheProvider(t *testing.T) {
	r := start(t, ledgertest.NewRabbitMQ(t).URL)

	withdrawalID := r.fundAndWithdraw(t, uuid.New())

	require.Eventually(t, func() bool {
		var providerReferenceID *string
		err := r.pool.QueryRow(context.Background(),
			"SELECT provider_reference_id FROM transactions WHERE id = $1", withdrawalID).Scan(&providerReferenceID)
		return err == nil && providerReferenceID != nil && strings.HasPrefix(*providerReferenceID, "trf_")
	}, 20*time.Second, 100*time.Millisecond)
}

func TestTheAPIServesWhileRabbitMQIsDown(t *testing.T) {
	r := start(t, "amqp://guest:guest@127.0.0.1:1/")

	status, _ := r.call(t, http.MethodGet, "/alive", "")
	require.Equal(t, http.StatusOK, status)
	status, body := r.call(t, http.MethodGet, "/health", "")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "Unhealthy", body)

	withdrawalID := r.fundAndWithdraw(t, uuid.New())

	require.Eventually(t, func() bool {
		var attempts int32
		err := r.pool.QueryRow(context.Background(),
			"SELECT attempts FROM outbox_messages WHERE processed_at IS NULL AND payload->>'transactionId' = $1",
			withdrawalID).Scan(&attempts)
		return err == nil && attempts >= 1
	}, 10*time.Second, 100*time.Millisecond, "the relay records failed publishes and keeps the message")
	require.NoError(t, r.stop())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd golang && go test ./internal/app/...`
Expected: FAIL to compile, with `undefined: app.New`.

- [ ] **Step 3: Implement the app and the binary**

Create `golang/internal/app/app.go`:

```go
// Package app is the composition root: it builds every component from Config and runs them together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"wallet/internal/api"
	"wallet/internal/common"
	"wallet/internal/config"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/reconciliation"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
	"wallet/internal/rabbit"
)

type App struct {
	cfg      config.Config
	log      *slog.Logger
	pool     *pgxpool.Pool
	rabbit   *rabbit.Conn
	listener net.Listener
	server   *http.Server
	relay    *outbox.Relay
	consumer *payouts.Consumer
	sweeper  *reconciliation.Sweeper
	checker  *reconciliation.Checker
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if cfg.RabbitMQURL == "" {
		return nil, errors.New("RABBITMQ_URL is required")
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.IsDevelopment() {
		if err := store.Migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		pool.Close()
		return nil, err
	}

	s := store.New(pool)
	clock := common.SystemClock{}
	provider := providers.NewFakePaymentProvider()
	conn := rabbit.New(cfg.RabbitMQURL, log)
	settle := withdrawals.NewSettleHandler(s, clock)
	release := withdrawals.NewReleaseHandler(s, clock, log)

	router := api.NewRouter(api.Deps{
		Log:               log,
		Development:       cfg.IsDevelopment(),
		CreateWallet:      wallets.NewCreateHandler(s, clock),
		GetWallet:         wallets.NewGetHandler(s),
		ListTransactions:  wallets.NewListHandler(s),
		InitiateTopUp:     topups.NewInitiateHandler(s, provider, clock),
		RequestWithdrawal: withdrawals.NewRequestHandler(s, clock),
		Webhooks:          webhooks.NewHandler(s, topups.NewCompleteHandler(s, clock, log), settle, release),
		FakeProvider:      provider,
		HealthChecks: []api.HealthCheck{
			pool.Ping,
			func(context.Context) error {
				if !conn.IsOpen() {
					return errors.New("rabbitmq connection is closed")
				}
				return nil
			},
		},
	})

	return &App{
		cfg:      cfg,
		log:      log,
		pool:     pool,
		rabbit:   conn,
		listener: listener,
		server:   &http.Server{Handler: router, ReadHeaderTimeout: 10 * time.Second},
		relay:    outbox.NewRelay(s, outbox.NewRabbitPublisher(conn), clock, cfg.Wallet.OutboxBatchSize, log),
		consumer: payouts.NewConsumer(conn, payouts.NewProcessor(s, provider, release, log), log),
		sweeper:  reconciliation.NewSweeper(s, provider, settle, release, clock, cfg.Wallet.PendingThreshold, log),
		checker:  reconciliation.NewChecker(s, log),
	}, nil
}

func (a *App) Addr() string { return a.listener.Addr().String() }

// Run serves HTTP and runs the background workers until ctx ends, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	defer a.pool.Close()
	defer func() { _ = a.rabbit.Close() }()

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a.log.Info("listening", "addr", a.Addr())
		if err := a.server.Serve(a.listener); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return a.server.Shutdown(shutdownCtx)
	})
	g.Go(func() error {
		return common.RunEvery(ctx, a.cfg.Wallet.OutboxPollInterval, func(ctx context.Context) error {
			_, err := a.relay.RelayPending(ctx)
			return err
		}, func(err error) { a.log.Error("outbox relay run failed", "error", err) })
	})
	g.Go(func() error { return a.consumer.Run(ctx) })
	g.Go(func() error {
		return common.RunEvery(ctx, a.cfg.Wallet.SweeperInterval, func(ctx context.Context) error {
			return reconciliation.RunOnce(ctx, a.sweeper, a.checker, a.log)
		}, func(err error) { a.log.Error("reconciliation run failed", "error", err) })
	})
	return g.Wait()
}
```

Create `golang/cmd/wallet-api/main.go`:

```go
// Command wallet-api serves the wallet HTTP API and runs its background workers. `wallet-api migrate` applies the
// database migrations and exits.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/app"
	"wallet/internal/config"
	"wallet/internal/ledger/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("wallet-api failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case len(args) == 0:
		a, err := app.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		return a.Run(ctx)
	case args[0] == "migrate":
		pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		return store.Migrate(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (run with no arguments, or \"migrate\")", args[0])
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd golang && go mod tidy && go build ./... && go test ./internal/app/...`
Expected: the build succeeds and `ok  	wallet/internal/app` is printed.

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "feat(golang): wire the app together and add the wallet-api command"
```

---

### Task 16: End-to-end script, AGENTS.md, final verification

**Files:**
- Create: `golang/scripts/e2e.sh` (executable)
- Create: `golang/AGENTS.md`

**Interfaces:**
- Consumes: the `wallet-api` binary and the exact log messages from the Global Constraints.
- Produces: `scripts/e2e.sh`, which prints `RESULT: <n> passed, 0 failed` and exits 0 on success.

- [ ] **Step 1: Write the e2e script**

Create `golang/scripts/e2e.sh`:

```bash
#!/usr/bin/env bash
# End-to-end test of the Go wallet API against real Postgres and RabbitMQ.
#
# Starts throwaway containers, builds and runs the API with APP_ENV=development (which applies migrations and enables
# /dev/provider), exercises every endpoint and background flow, checks the ledger invariants in the database, then
# tears everything down. Exits non-zero if any check fails.
#
# Usage: golang/scripts/e2e.sh            (KEEP=1 leaves the containers running afterwards)
# Needs: docker, go, curl, jq, uuidgen
#
# The sweeper runs every 5s with a 20s pending threshold so reconciliation can be observed in seconds.
# Production uses the defaults (5 minute interval, 15 minute threshold).
set -u
cd "$(dirname "$0")/.."

PORT=${PORT:-5591}; PG_PORT=${PG_PORT:-55433}; RMQ_PORT=${RMQ_PORT:-55673}
BASE=http://localhost:$PORT
PG=wallet-go-e2e-pg; RMQ=wallet-go-e2e-rmq
LOG=$(mktemp -t wallet-go-e2e-api.XXXXXX.log)
BIN_DIR=$(mktemp -d -t wallet-go-e2e-bin.XXXXXX); BIN=$BIN_DIR/wallet-api
PASS=0; FAIL=0; API_PID=

# Log lines the service writes (log/slog JSON); see Global Constraints in the implementation plan.
SWEEP='"msg":"payout sweep"'
SWEEP_STILL_PENDING='"msg":"payout sweep","settled":0,"released":0,"redriven":0,"still_pending":[1-9]'
SWEEP_REQUEUED='"msg":"payout sweep","settled":0,"released":0,"redriven":[1-9]'
RESUBMITTED='resubmitting it'
VIOLATION='"msg":"ledger invariant violated"'
UNHANDLED='"msg":"unhandled error"'

for tool in docker go curl jq uuidgen; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 2; }
done

stop_api() {
  [ -n "$API_PID" ] || return 0
  kill "$API_PID" 2>/dev/null; wait "$API_PID" 2>/dev/null; API_PID=
}
cleanup() {
  stop_api
  if [ "${KEEP:-0}" != 1 ]; then docker rm -f "$PG" "$RMQ" >/dev/null 2>&1; fi
  rm -rf "$BIN_DIR"
  echo "API log: $LOG"
}
trap cleanup EXIT

start_api() {
  APP_ENV=development HTTP_ADDR=":$PORT" \
  DATABASE_URL="postgres://postgres:e2e@localhost:$PG_PORT/appdb?sslmode=disable" \
  RABBITMQ_URL="amqp://guest:guest@localhost:$RMQ_PORT/" \
  WALLET_SWEEPER_INTERVAL=5s WALLET_PENDING_THRESHOLD=20s \
    "$BIN" >>"$LOG" 2>&1 &
  API_PID=$!
  wait_for "curl -sf $BASE/health >/dev/null && echo up" up 60 || { echo "API did not become healthy; see $LOG" >&2; exit 1; }
}
wait_rmq() { wait_for "docker exec $RMQ rabbitmq-diagnostics -q check_port_connectivity >/dev/null 2>&1 && echo up" up 120; }

psql_q() { docker exec "$PG" psql -U postgres -d appdb -tAc "$1"; }
log_count() { grep -cE "$1" "$LOG"; }
has_log() { grep -qE "$1" "$LOG" && echo yes; }

# req METHOD PATH [JSON] -> sets CODE and BODY
req() {
  local out
  if [ $# -ge 3 ]; then out=$(curl -s -X "$1" "$BASE$2" -H 'content-type: application/json' -d "$3" -w '\n%{http_code}')
  else out=$(curl -s -X "$1" "$BASE$2" -w '\n%{http_code}'); fi
  CODE=$(tail -n1 <<<"$out"); BODY=$(sed '$d' <<<"$out")
}
check() { # name expected actual
  if [ "$2" == "$3" ]; then PASS=$((PASS+1)); echo "  PASS $1"; else FAIL=$((FAIL+1)); echo "  FAIL $1 (expected '$2', got '$3')"; echo "       body: $BODY"; fi
}
j() { jq -r "$1" <<<"$BODY"; }
wallet() { req GET "/users/$1/wallet"; echo "$(j .balance)/$(j .availableBalance)"; }
wait_for() { # cmd expected timeout-seconds
  local i; for i in $(seq "$3"); do [ "$(eval "$1")" == "$2" ] && return 0; sleep 1; done; return 1; }

echo "== Setup"
docker rm -f "$PG" "$RMQ" >/dev/null 2>&1
docker run -d --name "$PG" -e POSTGRES_PASSWORD=e2e -e POSTGRES_DB=appdb -p "$PG_PORT:5432" postgres:17 >/dev/null || exit 1
docker run -d --name "$RMQ" -p "$RMQ_PORT:5672" rabbitmq:4-management >/dev/null || exit 1
go build -o "$BIN" ./cmd/wallet-api || { echo "build failed" >&2; exit 1; }
wait_for "docker exec $PG pg_isready -U postgres -d appdb >/dev/null 2>&1 && echo up" up 60 || exit 1
wait_rmq || exit 1
start_api
echo "  API running on $BASE (log: $LOG)"

U=$(uuidgen); U2=$(uuidgen); NOWALLET=$(uuidgen)
echo "== Infrastructure"
req GET /health; check "health endpoint" 200 "$CODE"; check "health body" Healthy "$BODY"
req GET /alive; check "alive endpoint" 200 "$CODE"
req GET /; check "no root route" 404 "$CODE"

echo "== Wallet creation"
req POST "/users/$U/wallet"; check "create wallet -> 201" 201 "$CODE"
check "new wallet zero balance NGN" "0/0/NGN" "$(j .balance)/$(j .availableBalance)/$(j .currency)"
req POST "/users/$U/wallet"; check "create again is idempotent -> 200" 200 "$CODE"
req GET "/users/$U/wallet"; check "get wallet -> 200" 200 "$CODE"
req GET "/users/$NOWALLET/wallet"; check "get missing wallet -> 404" 404 "$CODE"; check "error code" Wallet.NotFound "$(j .title)"
req GET "/users/not-a-guid/wallet"; check "non-guid user id -> 404" 404 "$CODE"
req GET "/users/$NOWALLET/wallet/transactions"; check "transactions of missing wallet -> 404" 404 "$CODE"
req POST "/users/$U2/wallet"; check "second wallet -> 201" 201 "$CODE"

echo "== Top-up: success path"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":500000}'
check "initiate top-up -> 201" 201 "$CODE"; check "top-up pending" Pending "$(j .status)"
TU1_ID=$(j .id); TU1_PRV=$(j .providerReferenceId)
check "provider reference assigned" true "$([[ $TU1_PRV == chg_* ]] && echo true)"
check "balance unchanged while pending" "0/0" "$(wallet "$U")"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":500000}'
check "replayed top-up -> 200" 200 "$CODE"; check "replay returns same transaction" "$TU1_ID" "$(j .id)"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":999}'
check "same ref, different body -> 409" 409 "$CODE"; check "error code" Transaction.DuplicateReference "$(j .title)"
req POST "/dev/provider/$TU1_PRV/complete?outcome=success"
check "dev provider charge success -> 200" 200 "$CODE"; check "top-up succeeded" Succeeded "$(j .status)"
check "balance credited" "500000/500000" "$(wallet "$U")"
req POST "/dev/provider/$TU1_PRV/complete?outcome=success"
check "duplicate success webhook -> 200" 200 "$CODE"
check "no double credit" "500000/500000" "$(wallet "$U")"

echo "== Top-up: failure path and webhook endpoint"
req POST "/users/$U/top-ups" '{"referenceId":"tu-2","amount":1000}'; TU2_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.failed\",\"providerReferenceId\":\"$TU2_PRV\",\"amount\":1000}"
check "charge.failed webhook -> 200" 200 "$CODE"; check "top-up failed" Failed "$(j .status)"
check "balance unchanged after failed top-up" "500000/500000" "$(wallet "$U")"
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU2_PRV\",\"amount\":1000}"
check "late charge.success leaves it Failed" Failed "$(j .status)"
check "success after failure does not credit" "500000/500000" "$(wallet "$U")"
req POST "/users/$U/top-ups" '{"referenceId":"tu-3","amount":2500}'; TU3_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU3_PRV\",\"amount\":2500}"
check "charge.success via public webhook -> 200" 200 "$CODE"
check "balance credited via webhook" "502500/502500" "$(wallet "$U")"
req POST "/users/$U/top-ups" '{"referenceId":"tu-4","amount":4000}'; TU4_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU4_PRV\",\"amount\":1}"
check "amount mismatch marks top-up Failed" Failed "$(j .status)"
check "mismatched amount not credited as 4000/1" "502500/502500" "$(wallet "$U")"
req POST /webhooks/provider '{"event":"charge.success","providerReferenceId":"chg_unknown","amount":1}'
check "webhook for unknown charge -> 404" 404 "$CODE"
req POST /webhooks/provider '{"event":"bogus.event","providerReferenceId":"x","amount":1}'
check "unknown webhook event -> 400" 400 "$CODE"; check "error code" Validation.Event "$(j .title)"

echo "== Top-up validation"
req POST "/users/$U/top-ups" '{"referenceId":"tu-zero","amount":0}'; check "zero amount -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"tu-neg","amount":-5}'; check "negative amount -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"","amount":5}'; check "empty referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" "{\"referenceId\":\"$(printf 'x%.0s' {1..129})\",\"amount\":5}"; check "129-char referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"amount":5}'; check "missing referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"x","amount":"lots"}'; check "non-numeric amount -> 400" 400 "$CODE"
req POST "/users/$NOWALLET/top-ups" '{"referenceId":"tu-x","amount":5}'; check "top-up lazily creates wallet -> 201" 201 "$CODE"
req GET "/users/$NOWALLET/wallet"; check "lazily created wallet exists" 200 "$CODE"
NOWALLET=$(uuidgen)

echo "== Withdrawal: success path (outbox -> RabbitMQ -> payout worker -> webhook)"
WD='{"referenceId":"wd-1","amount":100000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada Lovelace"}'
req POST "/users/$U/withdrawals" "$WD"
check "request withdrawal -> 202" 202 "$CODE"; check "withdrawal pending" Pending "$(j .status)"; WD1_ID=$(j .id)
check "funds held (balance kept, available reduced)" "502500/402500" "$(wallet "$U")"
req POST "/users/$U/withdrawals" "$WD"; check "replayed withdrawal -> 200" 200 "$CODE"; check "same transaction" "$WD1_ID" "$(j .id)"
check "replay does not hold twice" "502500/402500" "$(wallet "$U")"
prv_of() { psql_q "select coalesce(provider_reference_id,'') from transactions where id='$1'" 2>/dev/null; }
wait_for "[[ \$(prv_of $WD1_ID) == trf_* ]] && echo yes" yes 20
WD1_PRV=$(prv_of "$WD1_ID"); check "payout worker sent payout to provider" true "$([[ $WD1_PRV == trf_* ]] && echo true)"
check "outbox drained" 0 "$(psql_q "select count(*) from outbox_messages where processed_at is null" 2>/dev/null)"
req POST "/dev/provider/$WD1_PRV/complete?outcome=success"
check "transfer success -> Succeeded" Succeeded "$(j .status)"
check "hold settled (balance debited)" "402500/402500" "$(wallet "$U")"
req POST "/dev/provider/$WD1_PRV/complete?outcome=failed"
check "late transfer.failed leaves it Succeeded" Succeeded "$(j .status)"
check "late failure does not refund a settled payout" "402500/402500" "$(wallet "$U")"

echo "== Withdrawal: failure path releases hold"
req POST "/users/$U/withdrawals" '{"referenceId":"wd-2","amount":2500,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; WD2_ID=$(j .id)
check "held" "402500/400000" "$(wallet "$U")"
wait_for "[[ \$(prv_of $WD2_ID) == trf_* ]] && echo yes" yes 20; WD2_PRV=$(prv_of "$WD2_ID")
req POST /webhooks/provider "{\"event\":\"transfer.failed\",\"providerReferenceId\":\"$WD2_PRV\",\"reference\":\"${WD2_ID//-/}\",\"amount\":2500}"
check "transfer.failed webhook -> Failed" Failed "$(j .status)"
check "hold released" "402500/402500" "$(wallet "$U")"
req POST /webhooks/provider "{\"event\":\"transfer.failed\",\"providerReferenceId\":\"$WD2_PRV\",\"amount\":2500}"
check "duplicate transfer.failed does not double release" "402500/402500" "$(wallet "$U")"

echo "== Withdrawal: webhook matched by reference before provider ref stored"
req POST "/users/$U/withdrawals" '{"referenceId":"wd-3","amount":500,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; WD3_ID=$(j .id)
req POST /webhooks/provider "{\"event\":\"transfer.success\",\"providerReferenceId\":\"trf_not_yet_stored\",\"reference\":\"${WD3_ID//-/}\",\"amount\":500}"
check "transfer.success by reference -> Succeeded" Succeeded "$(j .status)"
check "settled" "402000/402000" "$(wallet "$U")"

echo "== Withdrawal validation"
w() { req POST "/users/$U/withdrawals" "$1"; }
w '{"referenceId":"wd-big","amount":999999999,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'
check "insufficient funds -> 422" 422 "$CODE"; check "error code" Wallet.InsufficientFunds "$(j .title)"
w '{"referenceId":"wd-v1","amount":0,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "zero amount -> 400" 400 "$CODE"
w '{"referenceId":"wd-v2","amount":10,"bankCode":"058","accountNumber":"123","accountName":"Ada"}'; check "short account number -> 400" 400 "$CODE"
w '{"referenceId":"wd-v3","amount":10,"bankCode":"058","accountNumber":"01234abcde","accountName":"Ada"}'; check "non-digit account number -> 400" 400 "$CODE"
w '{"referenceId":"wd-v4","amount":10,"bankCode":"","accountNumber":"0123456789","accountName":"Ada"}'; check "empty bank code -> 400" 400 "$CODE"
w '{"referenceId":"wd-v5","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":""}'; check "empty account name -> 400" 400 "$CODE"
w '{"referenceId":"tu-1","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "referenceId reused from a top-up -> 409" 409 "$CODE"
req POST "/users/$NOWALLET/withdrawals" '{"referenceId":"wd-x","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "withdrawal without wallet -> 404" 404 "$CODE"
check "balance untouched by rejected requests" "402000/402000" "$(wallet "$U")"

echo "== Concurrency: parallel withdrawals cannot overspend"
req POST "/users/$U2/top-ups" '{"referenceId":"c-tu","amount":10000}'; C_PRV=$(j .providerReferenceId)
req POST "/dev/provider/$C_PRV/complete?outcome=success"
codes=$(pids=(); for i in $(seq 10); do curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/users/$U2/withdrawals" -H 'content-type: application/json' \
  -d "{\"referenceId\":\"c-wd-$i\",\"amount\":3000,\"bankCode\":\"058\",\"accountNumber\":\"0123456789\",\"accountName\":\"Ada\"}" & pids+=($!); done; wait "${pids[@]}")
accepted=$(grep -c '^202$' <<<"$codes"); echo "       statuses: $(sort <<<"$codes" | uniq -c | tr '\n' ' ')"
check "exactly 3 of 10 x 3000 accepted from 10000" 3 "$accepted"
check "available never negative" "10000/1000" "$(wallet "$U2")"
codes=$(pids=(); for i in $(seq 5); do curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/users/$U2/top-ups" -H 'content-type: application/json' -d '{"referenceId":"same-ref","amount":77}' & pids+=($!); done; wait "${pids[@]}")
echo "       parallel same-ref top-ups: $(sort <<<"$codes" | uniq -c | tr '\n' ' ')"
U2_ACCOUNT=$(req GET "/users/$U2/wallet"; j .accountId)
check "exactly one transaction for parallel same-ref" 1 "$(psql_q "select count(*) from transactions where reference_id='same-ref' and account_id='$U2_ACCOUNT'" 2>/dev/null)"

echo "== Transactions listing"
req GET "/users/$U/wallet/transactions"; check "list -> 200" 200 "$CODE"
check "all 7 of user's transactions listed" 7 "$(j 'length')"
check "newest first" true "$(j '[.[].createdAt] == ([.[].createdAt] | sort | reverse)')"
check "no cross-user leakage" 0 "$(j '[.[] | select(.referenceId | startswith("c-"))] | length')"

echo "== Dev provider endpoint validation"
req POST "/dev/provider/$TU1_PRV/complete?outcome=maybe"; check "bad outcome -> 400" 400 "$CODE"
req POST "/dev/provider/chg_nope/complete?outcome=success"; check "unknown provider ref -> 404" 404 "$CODE"

echo "== Reconciliation sweeper (interval 5s, threshold 20s)"
SW_ID=$(psql_q "select id from transactions where reference_id like 'c-wd-%' and status='Pending' order by created_at limit 1")
echo "       waiting up to 60s for a sweep that sees the stale pending withdrawals..."
wait_for 'has_log "$SWEEP_STILL_PENDING"' yes 60
check "sweeper ran and kept provider-pending payouts held" yes "$(has_log "$SWEEP_STILL_PENDING")"
check "swept withdrawal remains Pending" Pending "$(psql_q "select status from transactions where id='$SW_ID'")"
check "held funds still held after sweep" "10000/1000" "$(wallet "$U2")"

echo "== Ledger invariants (database)"
check "I1 every posted transaction is a balanced 2-leg entry" 0 "$(psql_q "select count(*) from (select transaction_id from ledger_entries group by transaction_id having count(*)<>2 or sum(case when direction='Debit' then amount else 0 end)<>sum(case when direction='Credit' then amount else 0 end)) x")"
check "I3 account balances equal entry nets" 0 "$(psql_q "select count(*) from accounts a where a.balance <> coalesce((select sum(case when direction='Credit' then amount else -amount end) from ledger_entries e where e.account_id=a.id),0)")"
check "I4 no negative/invalid wallets" 0 "$(psql_q "select count(*) from accounts where type='User' and (balance<0 or available_balance<0 or available_balance>balance)")"
check "I6 all balances sum to zero" 0 "$(psql_q "select sum(balance) from accounts")"
check "no invariant violations logged" 0 "$(log_count "$VIOLATION")"
check "no unhandled errors logged" 0 "$(log_count "$UNHANDLED")"

echo "== RabbitMQ topology"
q=$(docker exec "$RMQ" rabbitmqctl -q list_queues name type messages 2>/dev/null)
check "wallet.payouts is a quorum queue" quorum "$(awk '$1=="wallet.payouts"{print $2}' <<<"$q")"
check "dead-letter queue exists" true "$(grep -q '^wallet.payouts.dead' <<<"$q" && echo true)"
check "no dead-lettered payouts" 0 "$(awk '$1=="wallet.payouts.dead"{print $3}' <<<"$q")"

echo "== Outbox survives a RabbitMQ outage"
OU=$(uuidgen)
req POST "/users/$OU/top-ups" '{"referenceId":"o-tu","amount":9000}'; req POST "/dev/provider/$(j .providerReferenceId)/complete?outcome=success"
docker stop "$RMQ" >/dev/null
req POST "/users/$OU/withdrawals" '{"referenceId":"o-wd","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'
check "withdrawal accepted while broker is down -> 202" 202 "$CODE"; O_ID=$(j .id)
check "funds held while broker is down" "9000/5000" "$(wallet "$OU")"
wait_for "curl -s $BASE/health" Unhealthy 30; check "health reports Unhealthy" Unhealthy "$(curl -s $BASE/health)"
check "payout request waits in the outbox" 1 "$(psql_q "select count(*) from outbox_messages where processed_at is null and payload::text like '%$O_ID%'")"
docker start "$RMQ" >/dev/null; wait_rmq
wait_for "[[ \$(prv_of $O_ID) == trf_* ]] && echo yes" yes 90
check "payout delivered after broker recovers" true "$([[ $(prv_of "$O_ID") == trf_* ]] && echo true)"
check "outbox drained after recovery" 0 "$(psql_q "select count(*) from outbox_messages where processed_at is null")"

echo "== Sweeper resubmits payouts the provider lost (API restart wipes the in-memory fake provider)"
stop_api; start_api
PENDING=$(psql_q "select count(*) from transactions where type='Withdrawal' and status='Pending'")
echo "       $PENDING pending withdrawals the restarted provider has never seen; waiting up to 60s..."
wait_for 'has_log "$SWEEP_REQUEUED"' yes 60
check "sweeper re-queued the lost payouts" yes "$(has_log "$SWEEP_REQUEUED")"
wait_for "psql_q \"select count(*) from transactions where type='Withdrawal' and status='Pending' and provider_reference_id is null\"" 0 30
check "every pending payout was resubmitted to the provider" 0 "$(psql_q "select count(*) from transactions where type='Withdrawal' and status='Pending' and provider_reference_id is null")"
wait_for '[ "$(log_count "$RESUBMITTED")" -ge "$PENDING" ] && echo yes' yes 30
check "every lost provider reference was logged" true "$([ "$(log_count "$RESUBMITTED")" -ge "$PENDING" ] && echo true)"
SWEEPS=$(log_count "$SWEEP"); wait_for 'echo $(( $(log_count "$SWEEP") - SWEEPS ))' 2 30
OUTBOX=$(psql_q "select count(*) from outbox_messages"); SWEEPS=$(log_count "$SWEEP")
wait_for 'echo $(( $(log_count "$SWEEP") - SWEEPS ))' 2 30
check "no further re-queues once resubmitted (outbox stable over 2 sweeps)" "$OUTBOX" "$(psql_q "select count(*) from outbox_messages")"
check "held funds still held" "10000/1000" "$(wallet "$U2")"
check "no invariant violations after restart" 0 "$(log_count "$VIOLATION")"

echo; echo "RESULT: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
```

Make it executable: `chmod +x golang/scripts/e2e.sh`.

- [ ] **Step 2: Run the e2e script**

Run: `cd golang && scripts/e2e.sh`
Expected: the final line is `RESULT: <n> passed, 0 failed` and the exit code is 0. It takes about 3–4 minutes.

If a check fails, open the API log path the script prints. Use superpowers:systematic-debugging, and fix the Go code
rather than loosening the check: every check mirrors one in `dotnet/scripts/e2e.sh`.

- [ ] **Step 3: Write `golang/AGENTS.md`**

```markdown
# AGENTS.md: Go wallet

Go port of `dotnet/`, behaviour-compatible with it: same HTTP contract, ledger rules, outbox → RabbitMQ → payout
semantics. Design: `docs/superpowers/specs/2026-10-13-golang-port-design.md`.

## Commands (run from `golang/`)
- **Test:** `go test ./...`. Needs Docker: Postgres/RabbitMQ come from testcontainers. To reuse running servers set
  `WALLET_TEST_DATABASE_URL` / `WALLET_TEST_RABBITMQ_URL` and run `go test -p 1 ./...`.
- **Lint:** `make lint` (gofmt, go vet, golangci-lint if installed)
- **Regenerate queries:** `make generate` (sqlc in Docker). Never edit `internal/ledger/store/db/` by hand.
- **End-to-end:** `make e2e` (needs docker, go, curl, jq, uuidgen)
- **Run locally:** `make up && make run` (APP_ENV=development migrates and enables `/dev/provider`)

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
```

- [ ] **Step 4: Run the full verification**

Run each command and confirm the expected output before claiming completion:

```bash
cd golang
make lint            # no output, exit 0
go test ./...        # every package ok
scripts/e2e.sh       # RESULT: <n> passed, 0 failed
git status --short   # only the files from this task (and nothing under internal/ledger/store/db is modified unexpectedly)
```

- [ ] **Step 5: Commit**

```bash
git add golang
git commit -m "test(golang): add end-to-end script and agent guidelines"
```
