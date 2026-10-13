# Wallet service

A single-currency (NGN) wallet backed by a double-entry ledger.

- **Top-ups:** users top up through a payment provider.
- **Withdrawals:** users withdraw to a Nigerian bank account. Payouts leave through a transactional outbox and
  RabbitMQ.
- **Reconciliation:** a background loop repairs payouts that went quiet and checks the ledger's invariants.

The service exists in two implementations with the **same HTTP contract and behaviour**:

| Folder | Implementation | Stack |
|---|---|---|
| [`dotnet/`](dotnet/README.md) | Reference implementation | .NET 10, ASP.NET Core MVC, EF Core + Npgsql, RabbitMQ.Client, .NET Aspire, xUnit |
| [`golang/`](golang/README.md) | Behaviour-compatible port | Go 1.26+, stdlib `net/http`, pgx + sqlc, goose, amqp091-go, testify + testcontainers |

Each folder is self-contained, with its own README (architecture, API, flows, configuration, testing), `AGENTS.md`
(contributor rules), `Dockerfile`, `compose.yaml` and end-to-end script.

## Quick start

Both stacks need only Docker. Each starts Postgres 17, RabbitMQ 4 and its API, and they use different host ports so
they can run side by side.

```bash
cd dotnet && docker compose up --build     # API on http://localhost:5568
cd golang && docker compose up --build     # API on http://localhost:8080
```

Try a full flow (set `BASE` to either API):

```bash
BASE=http://localhost:8080; U=$(uuidgen)
curl -X POST $BASE/users/$U/wallet
PRV=$(curl -s -X POST $BASE/users/$U/top-ups -H 'content-type: application/json' \
  -d '{"referenceId":"tu-1","amount":10000}' | jq -r .providerReferenceId)
curl -X POST "$BASE/dev/provider/$PRV/complete?outcome=success"      # fake provider confirms the charge
curl -X POST $BASE/users/$U/withdrawals -H 'content-type: application/json' \
  -d '{"referenceId":"wd-1","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'
curl $BASE/users/$U/wallet                       # {"balance":10000,"availableBalance":6000,...}
curl $BASE/users/$U/wallet/transactions          # the withdrawal gains a providerReferenceId once the payout worker sends it
```

Amounts are in kobo. `/dev/provider` exists only in development mode, which both compose stacks use by default.

## The shared contract

| Endpoint | Purpose |
|---|---|
| `POST /users/{userId}/wallet` | Open a wallet (`201`, or `200` if it exists) |
| `GET /users/{userId}/wallet` | Balance and available balance |
| `GET /users/{userId}/wallet/transactions` | Newest 100 transactions |
| `POST /users/{userId}/top-ups` | Start a top-up with the provider (`201`, or `200` on an idempotent replay) |
| `POST /users/{userId}/withdrawals` | Hold funds and queue a payout (`202`, or `200` on replay) |
| `POST /webhooks/provider` | Provider callbacks: `charge.success`, `charge.failed`, `transfer.success`, `transfer.failed` |

- **Errors:** RFC 7807 problem documents whose `title` is a stable code, for example `Wallet.InsufficientFunds` → 422
  and `Transaction.DuplicateReference` → 409.
- **Idempotency:** every command is idempotent on a client `referenceId`, which is unique per wallet.
- **Concurrency:** writes use optimistic concurrency with retries, so parallel requests can't overspend a wallet.

The full API, the ledger model and its invariants, and the payout flow are documented in each implementation's
README.

## Testing

| | .NET | Go |
|---|---|---|
| Unit + integration | `cd dotnet && dotnet test` (EF InMemory) | `cd golang && go test ./...` (real Postgres/RabbitMQ via testcontainers) |
| End-to-end | `dotnet/scripts/e2e.sh` | `golang/scripts/e2e.sh` (or `make e2e`) |

The e2e scripts start throwaway containers and exercise:
- every endpoint;
- parallel overspend attempts;
- a broker outage;
- an API restart.

They then check the ledger invariants directly in SQL. They need `docker`, `curl`, `jq`, `uuidgen`, and the
language toolchain.

## Repository layout

```
dotnet/                 .NET implementation (see dotnet/README.md)
golang/                 Go implementation (see golang/README.md)
docs/superpowers/
  specs/                design specs: the wallet ledger, and the Go port
  plans/                the implementation plans the code was built from
```

## Known limitations

These apply to both implementations and must be addressed before handling real money:

- **No authentication or authorization.** Any caller can act on any `{userId}`.
- **Unsigned webhooks.** `/webhooks/provider` does not verify a provider signature, so a forged `charge.success`
  would credit a wallet.
- **The payment provider is an in-memory fake.**
- **The outbox relay isn't safe to run on several instances at once.**

The .NET payout consumer also retries a failing payout forever on RabbitMQ 4, which ignores AMQP 0.9.1 requeues for
its delivery limit. The Go port counts attempts itself instead. Each implementation's README lists its own
remaining issues.
