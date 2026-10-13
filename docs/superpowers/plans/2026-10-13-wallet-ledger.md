# Wallet Ledger MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single-currency (NGN) digital wallet backend with a double-entry Postgres ledger. Users top up through a payment provider and withdraw to a bank account through an outbox → RabbitMQ → payout worker pipeline. A reconciliation sweeper finalises withdrawals that stay pending too long.

**Architecture:** A new `src/Live.Wallet` class library holds the domain, EF Core persistence, the feature slices (Wallets, TopUps, Withdrawals, Webhooks, Outbox, Payouts, Reconciliation, Providers) and the RabbitMQ adapters. `Live.Api` hosts MVC controllers and registers the background workers. Every handler returns `Result<T>`. Balance changes go through `ConcurrencyRetry`, which uses the account `Version` concurrency token, so a concurrent change is detected, the account is reloaded and the request is checked again.

**Tech Stack:** .NET 10 / C# 14, ASP.NET Core MVC controllers, EF Core 10 + Npgsql, EF Core InMemory (tests), RabbitMQ.Client 7.2.1 via Aspire.RabbitMQ.Client, Aspire 13.5.3, xUnit 2.9.3, FluentAssertions 7.2.0, NSubstitute.

**Spec:** `docs/superpowers/specs/2026-10-13-wallet-ledger-design.md`

## Global Constraints

- Target `net10.0`, `LangVersion 14`, `Nullable enable`, `TreatWarningsAsErrors true`. These are inherited from `Directory.Build.props`, so don't repeat them in csproj files.
- Package versions live only in `Directory.Packages.props` (central package management). `PackageReference` elements have no `Version`.
- No primary constructors, including positional records. Use explicit constructors that assign `private readonly _camelCase` fields, and records with `required` init properties.
- One primary type per file, with the file name matching the type.
- No `!` null-forgiving operator. Use `?? throw new InvalidOperationException(...)` or pattern matching.
- No `Console.WriteLine`. Log through `ILogger<T>` with `[LoggerMessage]` source-generated methods, which means the class is `partial`.
- Async methods end in `Async`, take a `CancellationToken` and pass it through. Library code (`src/Live.Wallet`) uses `.ConfigureAwait(false)` on every await. Test and API code does not.
- No `.Result` or `.Wait()`.
- API style: `[ApiController]` controllers with attribute routing. No Minimal API endpoint mapping.
- Expected failures are `Result<T>` failures built from `WalletErrors`, never exceptions. Exceptions are only for bugs and infrastructure faults.
- Currency is `NGN` only (`WalletCurrency.Code`). Amounts are `long` kobo and must be > 0.
- Error code → HTTP status: `Money.*` → 400, `Validation.*` → 400, `Wallet.NotFound`/`Transaction.NotFound` → 404, `Transaction.DuplicateReference`/`Concurrency.Exhausted`/`Ledger.*` → 409, `Wallet.InsufficientFunds` → 422.
- `ConcurrencyRetry.MaxAttempts = 3`. Defaults: outbox poll 1 s, outbox batch 50, sweeper interval 5 min, pending threshold 15 min. Queue `wallet.payouts` dead-letters to `wallet.payouts.dlx` → `wallet.payouts.dead` after 5 deliveries.
- Every commit: conventional message plus trailers, i.e. `git commit -m "<type>(wallet): <summary>" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"`.
- Work on branch `feat/wallet-ledger`.
- Run tests with `dotnet test tests/Live.Tests --filter "FullyQualifiedName~<ClassName>"`. In a compiled language the "red" step is often a build error naming the missing type; that counts as the expected failure.

### Test-infrastructure fact (verified before writing this plan)

EF Core InMemory **does** throw `DbUpdateConcurrencyException` for a stale `IsConcurrencyToken()` property. However, it **does not** roll back the other rows in the same `SaveChanges`: rows processed before the conflict stay written. Postgres rolls the whole save back. Task 3's `AtomicSaveInterceptor` (test-only) checks account versions *before* anything is written, so tests see Postgres-like atomicity. Every test context gets it through `TestDb.CreateContext`.

### Deviations from the spec (call these out in review)

1. **Payouts the provider reports as `NotFound` stay held.** The sweeper logs a warning and does not release them, where the spec said "release". Releasing while a delayed outbox message might still submit the payout could pay out *and* refund. Holding funds is the safe failure mode.
2. **Webhook body has an optional `reference` field** (our `ReferenceId`). Transfer webhooks can then be matched even if they arrive before the payout worker has stored the provider reference.
3. **Currency is the constant `WalletCurrency.Code = "NGN"`, not a `Wallet:Currency` config option.** With one currency, the config option is unnecessary (YAGNI).
4. **Dead-lettering uses a quorum queue with `x-delivery-limit = 5`.** RabbitMQ counts the deliveries, so the code doesn't have to.
5. **The integration test disables the background workers by removing their hosted-service registrations** in `WebApplicationFactory`. There's no config flag.

## Review Focus

Five ways the system can be used or fail that the spec implies but doesn't spell out. A test for each is added to the task that owns the code:

1. **Withdrawing exactly the whole available balance** should succeed and leave 0; one kobo more should give 422 → Task 8 `Withdrawing_the_entire_available_balance_succeeds`.
2. **A `transfer.success` webhook arriving before the payout worker stored the provider reference** should still settle → Task 10 `Transfer_success_before_the_provider_reference_is_stored_matches_our_reference`.
3. **RabbitMQ redelivering a payout message the provider already accepted** must not pay twice → Task 12 `Redelivered_message_after_acceptance_does_not_pay_twice`.
4. **A stale payout the provider has no record of** must keep the funds held, not release them → Task 13 `Keeps_a_payout_the_provider_has_never_seen_on_hold`.
5. **The webhook and the sweeper finalising the same withdrawal at once** must produce exactly one outcome → Task 9 `Settle_and_release_racing_apply_exactly_one`.

**Known gaps (accepted in the spec; record them in the PR description):**

- InMemory can't exercise the Postgres unique indexes or CHECK constraints. Two concurrent *first-ever* requests for one user can collide on the `(UserId, Currency)` index, and two identical top-up initiations can collide on `ReferenceId`. `CreateWalletHandler` recovers from the first case; top-up initiation surfaces a 500.
- No real-RabbitMQ test. No webhook authentication: anyone who knows a `providerReferenceId` can post `charge.success`. HMAC signature verification is needed before any real provider or public deployment.
- Every posting writes to the single `ProviderSettlement` row, which serialises postings under heavy load.

## File Structure

```
src/Live.Wallet/
  Live.Wallet.csproj
  WalletOptions.cs                         options bound from "Wallet"
  ServiceCollectionExtensions.cs           AddWallet / AddWalletBackgroundWorkers
  Ledger/
    WalletCurrency.cs  WalletErrors.cs  AccountType.cs  EntryDirection.cs
    TransactionType.cs  TransactionStatus.cs
    Account.cs  LedgerEntry.cs  LedgerTransaction.cs
    LedgerPosting.cs                       pure double-entry posting
    RequestHash.cs                         idempotency fingerprint
  Persistence/
    WalletDbContext.cs  ConcurrencyRetry.cs  WalletDbContextDesignTimeFactory.cs
    AccountConfiguration.cs  LedgerTransactionConfiguration.cs  LedgerEntryConfiguration.cs
    WithdrawalDetailsConfiguration.cs  OutboxMessageConfiguration.cs
    Migrations/                            generated
  Wallets/
    WalletResponse.cs  WalletCreation.cs  TransactionResponse.cs  IdempotentResponse.cs
    WalletAccounts.cs  Idempotency.cs
    CreateWalletHandler.cs  GetWalletHandler.cs  ListTransactionsHandler.cs
  Providers/
    IPaymentProvider.cs  ChargeInitiation.cs  PayoutInstruction.cs  PayoutInitiation.cs
    PayoutOutcome.cs  PayoutStatus.cs  FakeProviderRecord.cs  FakePaymentProvider.cs
  TopUps/
    InitiateTopUpCommand.cs  InitiateTopUpHandler.cs  CompleteTopUpHandler.cs
  Withdrawals/
    WithdrawalDetails.cs  RequestWithdrawalCommand.cs  RequestWithdrawalHandler.cs
    SettleWithdrawalHandler.cs  ReleaseWithdrawalHandler.cs
  Webhooks/
    ProviderWebhook.cs  ProviderWebhookEvents.cs  ProviderWebhookHandler.cs
  Outbox/
    OutboxMessage.cs  WalletJsonContext.cs  IMessagePublisher.cs  OutboxRelay.cs
    RabbitMqMessagePublisher.cs  OutboxRelayService.cs
  Payouts/
    PayoutRequested.cs  PayoutProcessingOutcome.cs  PayoutProcessor.cs
    PayoutQueueTopology.cs  RabbitMqPayoutConsumer.cs
  Reconciliation/
    SweepSummary.cs  PendingPayoutSweeper.cs  InvariantViolation.cs
    LedgerInvariantChecker.cs  ReconciliationService.cs
src/Live.Api/
  Live.Api.csproj (modify)  Program.cs (modify)
  Controllers/ProblemResults.cs  WalletsController.cs  TopUpsController.cs  TopUpRequest.cs
  Controllers/WithdrawalsController.cs  WithdrawalRequest.cs  ProviderWebhooksController.cs
  Controllers/DevProviderController.cs
tests/Live.Tests/
  Live.Tests.csproj (modify)
  Wallet/TestDb.cs  AtomicSaveInterceptor.cs  ConcurrentWriteInterceptor.cs
  Wallet/TestCommands.cs  WalletScenarios.cs  RecordingPublisher.cs
  Wallet/Ledger/*Tests.cs  Wallet/Persistence/*Tests.cs  Wallet/<Slice>/*Tests.cs
  Api/WalletApiFactory.cs  Api/WalletApiTests.cs  Api/ProblemResultsTests.cs
```

---

### Task 1: Scaffold `Live.Wallet` and the `Account` domain

**Files:**
- Modify: `Directory.Packages.props`, `Interview.slnx`, `tests/Live.Tests/Live.Tests.csproj`
- Create: `src/Live.Wallet/Live.Wallet.csproj`
- Create: `src/Live.Wallet/Ledger/WalletCurrency.cs`, `WalletErrors.cs`, `AccountType.cs`, `EntryDirection.cs`, `LedgerEntry.cs`, `Account.cs`
- Test: `tests/Live.Tests/Wallet/Ledger/AccountTests.cs`

**Interfaces:**
- Consumes: `Practice.Common.Result<T>`, `Practice.Common.Error`
- Produces:
  - `WalletCurrency.Code`, the `WalletErrors.*` errors, `AccountType { User, System }`, `EntryDirection { Debit, Credit }`
  - `Account`:
    - Fields: `ProviderSettlementId`, `ProviderSettlementName`
    - Factories: `OpenUserWallet(Guid, DateTimeOffset)`, `OpenSystem(Guid, string, DateTimeOffset)`
    - Public operations: `Result<long> PlaceHold(long)`, `Result<long> ReleaseHold(long)`
    - Internal operations: `Result<long> CanDebit(long, bool fromHold)`, `LedgerEntry Credit(Guid, long, DateTimeOffset)`, `LedgerEntry Debit(Guid, long, bool fromHold, DateTimeOffset)`
  - `LedgerEntry`: `internal static Create(...)`

- [ ] **Step 1: Add package versions**

In `Directory.Packages.props`, add inside the existing `<ItemGroup>` after the `<!-- Service defaults -->` block:

```xml
    <!-- Wallet -->
    <PackageVersion Include="Aspire.Npgsql.EntityFrameworkCore.PostgreSQL" Version="13.5.3" />
    <PackageVersion Include="Aspire.RabbitMQ.Client" Version="13.5.3" />
    <PackageVersion Include="Microsoft.EntityFrameworkCore.Design" Version="10.0.12" />
    <PackageVersion Include="Microsoft.EntityFrameworkCore.InMemory" Version="10.0.12" />
    <PackageVersion Include="Npgsql.EntityFrameworkCore.PostgreSQL" Version="10.0.1" />
    <PackageVersion Include="RabbitMQ.Client" Version="7.2.1" />
```

- [ ] **Step 2: Create the project**

`src/Live.Wallet/Live.Wallet.csproj`:

```xml
<Project Sdk="Microsoft.NET.Sdk">

  <ItemGroup>
    <FrameworkReference Include="Microsoft.AspNetCore.App" />
  </ItemGroup>

  <ItemGroup>
    <PackageReference Include="Npgsql.EntityFrameworkCore.PostgreSQL" />
    <PackageReference Include="RabbitMQ.Client" />
  </ItemGroup>

  <ItemGroup>
    <InternalsVisibleTo Include="Live.Tests" />
  </ItemGroup>

  <ItemGroup>
    <ProjectReference Include="..\Practice.Common\Practice.Common.csproj" />
  </ItemGroup>

</Project>
```

In `Interview.slnx`, add inside `<Folder Name="/src/">` after the `Live.Core` line:

```xml
    <Project Path="src/Live.Wallet/Live.Wallet.csproj" />
```

In `tests/Live.Tests/Live.Tests.csproj`, add `<PackageReference Include="Microsoft.EntityFrameworkCore.InMemory" />` to the package `ItemGroup`, and `<ProjectReference Include="..\..\src\Live.Wallet\Live.Wallet.csproj" />` to the project-reference `ItemGroup`.

- [ ] **Step 3: Write the failing tests**

`tests/Live.Tests/Wallet/Ledger/AccountTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class AccountTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void OpenUserWallet_starts_empty_in_NGN()
    {
        var userId = Guid.NewGuid();

        var account = Account.OpenUserWallet(userId, Now);

        account.UserId.Should().Be(userId);
        account.Type.Should().Be(AccountType.User);
        account.Currency.Should().Be("NGN");
        account.Balance.Should().Be(0);
        account.AvailableBalance.Should().Be(0);
        account.CreatedAt.Should().Be(Now);
    }

    [Fact]
    public void Credit_raises_both_balances_and_records_the_running_balance()
    {
        var account = Account.OpenUserWallet(Guid.NewGuid(), Now);
        var transactionId = Guid.NewGuid();

        var entry = account.Credit(transactionId, 500, Now);

        account.Balance.Should().Be(500);
        account.AvailableBalance.Should().Be(500);
        entry.Direction.Should().Be(EntryDirection.Credit);
        entry.TransactionId.Should().Be(transactionId);
        entry.AccountId.Should().Be(account.Id);
        entry.Amount.Should().Be(500);
        entry.Currency.Should().Be("NGN");
        entry.BalanceBefore.Should().Be(0);
        entry.BalanceAfter.Should().Be(500);
    }

    [Fact]
    public void PlaceHold_reduces_only_the_available_balance()
    {
        var account = Funded(500);

        var result = account.PlaceHold(200);

        result.IsSuccess.Should().BeTrue();
        account.AvailableBalance.Should().Be(300);
        account.Balance.Should().Be(500);
    }

    [Fact]
    public void PlaceHold_for_the_entire_available_balance_succeeds()
    {
        var account = Funded(500);

        account.PlaceHold(500).IsSuccess.Should().BeTrue();

        account.AvailableBalance.Should().Be(0);
    }

    [Fact]
    public void PlaceHold_beyond_the_available_balance_fails_and_changes_nothing()
    {
        var account = Funded(100);
        var versionBefore = account.Version;

        var result = account.PlaceHold(101);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        account.AvailableBalance.Should().Be(100);
        account.Version.Should().Be(versionBefore);
    }

    [Theory]
    [InlineData(0)]
    [InlineData(-1)]
    public void PlaceHold_rejects_non_positive_amounts(long amount)
    {
        Funded(100).PlaceHold(amount).Error.Code.Should().Be("Money.InvalidAmount");
    }

    [Fact]
    public void ReleaseHold_restores_exactly_the_held_amount()
    {
        var account = Funded(500);
        account.PlaceHold(200);

        account.ReleaseHold(200).IsSuccess.Should().BeTrue();

        account.AvailableBalance.Should().Be(500);
        account.Balance.Should().Be(500);
    }

    [Fact]
    public void ReleaseHold_without_a_matching_hold_fails()
    {
        var account = Funded(500);

        account.ReleaseHold(1).Error.Code.Should().Be("Ledger.HoldMissing");

        account.AvailableBalance.Should().Be(500);
    }

    [Fact]
    public void Debit_from_a_hold_lowers_the_balance_without_lowering_available_again()
    {
        var account = Funded(500);
        account.PlaceHold(200);

        var entry = account.Debit(Guid.NewGuid(), 200, fromHold: true, Now);

        account.Balance.Should().Be(300);
        account.AvailableBalance.Should().Be(300);
        entry.Direction.Should().Be(EntryDirection.Debit);
        entry.BalanceBefore.Should().Be(500);
        entry.BalanceAfter.Should().Be(300);
    }

    [Fact]
    public void CanDebit_without_a_hold_requires_available_funds()
    {
        var account = Funded(500);
        account.PlaceHold(400);

        account.CanDebit(200, fromHold: false).Error.Code.Should().Be("Wallet.InsufficientFunds");
    }

    [Fact]
    public void CanDebit_from_a_hold_requires_a_matching_hold()
    {
        Funded(500).CanDebit(200, fromHold: true).Error.Code.Should().Be("Ledger.HoldMissing");
    }

    [Fact]
    public void A_system_account_can_be_debited_below_zero()
    {
        var settlement = Account.OpenSystem(Account.ProviderSettlementId, Account.ProviderSettlementName, Now);

        settlement.CanDebit(1_000, fromHold: false).IsSuccess.Should().BeTrue();
        settlement.Debit(Guid.NewGuid(), 1_000, fromHold: false, Now);

        settlement.Balance.Should().Be(-1_000);
        settlement.Type.Should().Be(AccountType.System);
        settlement.UserId.Should().BeNull();
    }

    [Fact]
    public void Every_change_bumps_the_version()
    {
        var account = Funded(500);
        var versionBefore = account.Version;

        account.PlaceHold(100);

        account.Version.Should().Be(versionBefore + 1);
    }

    private static Account Funded(long amount)
    {
        var account = Account.OpenUserWallet(Guid.NewGuid(), Now);
        account.Credit(Guid.NewGuid(), amount, Now);
        return account;
    }
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~AccountTests"`
Expected: build FAILS with `CS0246: The type or namespace name 'Account' could not be found`.

- [ ] **Step 5: Implement the domain types**

`src/Live.Wallet/Ledger/WalletCurrency.cs`:

```csharp
namespace Live.Wallet.Ledger;

/// <summary>The wallet handles exactly one currency. Amounts are integer kobo.</summary>
public static class WalletCurrency
{
    public const string Code = "NGN";
}
```

`src/Live.Wallet/Ledger/WalletErrors.cs`:

```csharp
using Practice.Common;

namespace Live.Wallet.Ledger;

/// <summary>Every expected wallet failure. Codes are stable: the API maps them to HTTP statuses.</summary>
public static class WalletErrors
{
    public static readonly Error InvalidAmount = Error.Create("Money.InvalidAmount", "Amount must be a positive number of kobo.");

    public static readonly Error UnsupportedCurrency = Error.Create("Money.UnsupportedCurrency", "Only NGN is supported.");

    public static readonly Error WalletNotFound = Error.Create("Wallet.NotFound", "The user has no wallet.");

    public static readonly Error InsufficientFunds = Error.Create("Wallet.InsufficientFunds", "The available balance is too low.");

    public static readonly Error TransactionNotFound = Error.Create("Transaction.NotFound", "No matching transaction.");

    public static readonly Error DuplicateReference = Error.Create("Transaction.DuplicateReference", "This referenceId was already used for a different request.");

    public static readonly Error ConcurrencyExhausted = Error.Create("Concurrency.Exhausted", "The wallet kept changing concurrently; retry the request.");

    public static readonly Error HoldMissing = Error.Create("Ledger.HoldMissing", "There is no matching hold for this amount.");

    public static readonly Error SameAccount = Error.Create("Ledger.SameAccount", "A posting needs two different accounts.");

    public static readonly Error UnknownWebhookEvent = Error.Create("Validation.Event", "Unknown webhook event.");

    public static Error Validation(string field, string message) => Error.Create($"Validation.{field}", message);
}
```

`src/Live.Wallet/Ledger/AccountType.cs`:

```csharp
namespace Live.Wallet.Ledger;

public enum AccountType
{
    User,
    System,
}
```

`src/Live.Wallet/Ledger/EntryDirection.cs`:

```csharp
namespace Live.Wallet.Ledger;

/// <summary>Credit raises an account's balance, Debit lowers it, for every account type.</summary>
public enum EntryDirection
{
    Debit,
    Credit,
}
```

`src/Live.Wallet/Ledger/LedgerEntry.cs`:

```csharp
namespace Live.Wallet.Ledger;

/// <summary>One side of a posting. Append-only: entries are never updated or deleted.</summary>
public sealed class LedgerEntry
{
    private LedgerEntry()
    {
        Currency = WalletCurrency.Code;
    }

    public Guid Id { get; private set; }

    public Guid TransactionId { get; private set; }

    public Guid AccountId { get; private set; }

    public EntryDirection Direction { get; private set; }

    public long Amount { get; private set; }

    public string Currency { get; private set; }

    public long BalanceBefore { get; private set; }

    public long BalanceAfter { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    internal static LedgerEntry Create(
        Guid transactionId,
        Guid accountId,
        EntryDirection direction,
        long amount,
        string currency,
        long balanceBefore,
        long balanceAfter,
        DateTimeOffset now) =>
        new()
        {
            Id = Guid.CreateVersion7(now),
            TransactionId = transactionId,
            AccountId = accountId,
            Direction = direction,
            Amount = amount,
            Currency = currency,
            BalanceBefore = balanceBefore,
            BalanceAfter = balanceAfter,
            CreatedAt = now,
        };
}
```

`src/Live.Wallet/Ledger/Account.cs`:

```csharp
using Practice.Common;

namespace Live.Wallet.Ledger;

/// <summary>
/// A balance holder. Balance is settled money and always equals the sum of the account's ledger entries.
/// AvailableBalance is Balance minus active withdrawal holds. Version is the optimistic-concurrency token.
/// </summary>
public sealed class Account
{
    public const string ProviderSettlementName = "ProviderSettlement";

    public static readonly Guid ProviderSettlementId = new("00000000-0000-0000-0000-000000000001");

    private Account()
    {
        Currency = WalletCurrency.Code;
    }

    public Guid Id { get; private set; }

    public Guid? UserId { get; private set; }

    public AccountType Type { get; private set; }

    public string? Name { get; private set; }

    public string Currency { get; private set; }

    public long Balance { get; private set; }

    public long AvailableBalance { get; private set; }

    public int Version { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    public static Account OpenUserWallet(Guid userId, DateTimeOffset now) =>
        new() { Id = Guid.CreateVersion7(now), UserId = userId, Type = AccountType.User, CreatedAt = now };

    public static Account OpenSystem(Guid id, string name, DateTimeOffset now) =>
        new() { Id = id, Name = name, Type = AccountType.System, CreatedAt = now };

    public Result<long> PlaceHold(long amount)
    {
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (AvailableBalance < amount)
        {
            return WalletErrors.InsufficientFunds;
        }

        AvailableBalance -= amount;
        Version++;
        return AvailableBalance;
    }

    public Result<long> ReleaseHold(long amount)
    {
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (AvailableBalance + amount > Balance)
        {
            return WalletErrors.HoldMissing;
        }

        AvailableBalance += amount;
        Version++;
        return AvailableBalance;
    }

    /// <summary>System accounts may go negative. A user wallet needs available funds, or, when fromHold is true, a hold covering the amount.</summary>
    internal Result<long> CanDebit(long amount, bool fromHold)
    {
        if (Type == AccountType.System)
        {
            return amount;
        }

        if (fromHold)
        {
            return Balance - amount >= AvailableBalance ? amount : WalletErrors.HoldMissing;
        }

        return AvailableBalance >= amount ? amount : WalletErrors.InsufficientFunds;
    }

    internal LedgerEntry Credit(Guid transactionId, long amount, DateTimeOffset now)
    {
        var before = Balance;
        Balance += amount;
        AvailableBalance += amount;
        Version++;
        return LedgerEntry.Create(transactionId, Id, EntryDirection.Credit, amount, Currency, before, Balance, now);
    }

    /// <summary>When fromHold is true, AvailableBalance already dropped when the hold was placed, so only Balance moves.</summary>
    internal LedgerEntry Debit(Guid transactionId, long amount, bool fromHold, DateTimeOffset now)
    {
        var before = Balance;
        Balance -= amount;
        if (!fromHold)
        {
            AvailableBalance -= amount;
        }

        Version++;
        return LedgerEntry.Create(transactionId, Id, EntryDirection.Debit, amount, Currency, before, Balance, now);
    }
}
```

Note: the ternaries in `CanDebit` mix `long` and `Error`. They compile because the return type `Result<long>` is the conversion target for both branches (C# 9 target-typed conditional).

- [ ] **Step 6: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~AccountTests"`
Expected: PASS (14 tests).

- [ ] **Step 7: Commit**

```bash
git add Directory.Packages.props Interview.slnx src/Live.Wallet tests/Live.Tests
git commit -m "feat(wallet): add Live.Wallet project with account holds and entries" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 2: Transactions, double-entry posting, request hashing

**Files:**
- Create: `src/Live.Wallet/Ledger/TransactionType.cs`, `TransactionStatus.cs`, `LedgerTransaction.cs`, `LedgerPosting.cs`, `RequestHash.cs`
- Test: `tests/Live.Tests/Wallet/Ledger/LedgerPostingTests.cs`, `LedgerTransactionTests.cs`, `RequestHashTests.cs`

**Interfaces:**
- Consumes: `Account`, `LedgerEntry`, `WalletErrors` (Task 1)
- Produces:
  - `TransactionType { TopUp, Withdrawal }`
  - `TransactionStatus { Pending, Succeeded, Failed }`
  - `LedgerTransaction`:
    - Factories: `StartTopUp(Guid accountId, long amount, string referenceId, string requestHash, string providerReferenceId, DateTimeOffset now)`, `StartWithdrawal(Guid accountId, long amount, string referenceId, string requestHash, DateTimeOffset now)`
    - Members: `IsFinal`, `AssignProviderReference(string)`, `MarkSucceeded(DateTimeOffset)`, `MarkFailed(DateTimeOffset)`
  - `LedgerPosting.Post(LedgerTransaction transaction, Account debitAccount, Account creditAccount, bool debitFromHold, DateTimeOffset now) → Result<LedgerEntry[]>`
  - `RequestHash.Compute(params object[] parts) → string`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Ledger/LedgerPostingTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class LedgerPostingTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void A_top_up_posting_is_balanced_and_moves_money_from_settlement_to_the_wallet()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, 500, "ref-1", "hash", "chg_1", Now);

        var entries = LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, Now).Value;

        entries.Should().HaveCount(2);
        entries.Should().ContainSingle(e => e.Direction == EntryDirection.Debit && e.AccountId == settlement.Id && e.Amount == 500);
        entries.Should().ContainSingle(e => e.Direction == EntryDirection.Credit && e.AccountId == wallet.Id && e.Amount == 500);
        entries.Should().OnlyContain(e => e.TransactionId == topUp.Id && e.Currency == "NGN");
        wallet.Balance.Should().Be(500);
        wallet.AvailableBalance.Should().Be(500);
        settlement.Balance.Should().Be(-500);
        (wallet.Balance + settlement.Balance).Should().Be(0);
    }

    [Fact]
    public void Consecutive_postings_chain_running_balances()
    {
        var settlement = Settlement();
        var wallet = Wallet();

        var first = Fund(wallet, settlement, 500);
        var second = Fund(wallet, settlement, 300);

        var firstCredit = first.Single(e => e.Direction == EntryDirection.Credit);
        var secondCredit = second.Single(e => e.Direction == EntryDirection.Credit);
        secondCredit.BalanceBefore.Should().Be(firstCredit.BalanceAfter);
        secondCredit.BalanceAfter.Should().Be(800);
        var secondDebit = second.Single(e => e.Direction == EntryDirection.Debit);
        secondDebit.BalanceBefore.Should().Be(-500);
        secondDebit.BalanceAfter.Should().Be(-800);
    }

    [Fact]
    public void Settling_a_withdrawal_consumes_the_hold()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 1_000);
        wallet.PlaceHold(400);
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 400, "wd-1", "hash", Now);

        var result = LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: true, Now);

        result.IsSuccess.Should().BeTrue();
        wallet.Balance.Should().Be(600);
        wallet.AvailableBalance.Should().Be(600);
        settlement.Balance.Should().Be(-600);
    }

    [Fact]
    public void A_debit_without_funds_fails_and_leaves_both_accounts_untouched()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 100);
        var walletVersion = wallet.Version;
        var settlementVersion = settlement.Version;
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 150, "wd-1", "hash", Now);

        var result = LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: false, Now);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        wallet.Balance.Should().Be(100);
        wallet.AvailableBalance.Should().Be(100);
        settlement.Balance.Should().Be(-100);
        wallet.Version.Should().Be(walletVersion);
        settlement.Version.Should().Be(settlementVersion);
    }

    [Fact]
    public void Settling_without_a_hold_fails()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 1_000);
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 400, "wd-1", "hash", Now);

        LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: true, Now)
            .Error.Code.Should().Be("Ledger.HoldMissing");
    }

    [Fact]
    public void Posting_to_the_same_account_is_rejected()
    {
        var wallet = Wallet();
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, 10, "ref-1", "hash", "chg_1", Now);

        LedgerPosting.Post(topUp, wallet, wallet, debitFromHold: false, Now)
            .Error.Code.Should().Be("Ledger.SameAccount");
    }

    private static Account Settlement() =>
        Account.OpenSystem(Account.ProviderSettlementId, Account.ProviderSettlementName, Now);

    private static Account Wallet() => Account.OpenUserWallet(Guid.NewGuid(), Now);

    private static LedgerEntry[] Fund(Account wallet, Account settlement, long amount)
    {
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, amount, $"ref-{Guid.NewGuid():N}", "hash", $"chg_{Guid.NewGuid():N}", Now);
        return LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, Now).Value;
    }
}
```

`tests/Live.Tests/Wallet/Ledger/LedgerTransactionTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class LedgerTransactionTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void A_new_withdrawal_is_pending_without_a_provider_reference()
    {
        var accountId = Guid.NewGuid();

        var withdrawal = LedgerTransaction.StartWithdrawal(accountId, 400, "wd-1", "hash", Now);

        withdrawal.Type.Should().Be(TransactionType.Withdrawal);
        withdrawal.Status.Should().Be(TransactionStatus.Pending);
        withdrawal.IsFinal.Should().BeFalse();
        withdrawal.AccountId.Should().Be(accountId);
        withdrawal.Amount.Should().Be(400);
        withdrawal.ReferenceId.Should().Be("wd-1");
        withdrawal.RequestHash.Should().Be("hash");
        withdrawal.ProviderReferenceId.Should().BeNull();
        withdrawal.CreatedAt.Should().Be(Now);
        withdrawal.CompletedAt.Should().BeNull();
    }

    [Fact]
    public void A_new_top_up_carries_the_provider_reference()
    {
        LedgerTransaction.StartTopUp(Guid.NewGuid(), 500, "ref-1", "hash", "chg_1", Now)
            .ProviderReferenceId.Should().Be("chg_1");
    }

    [Fact]
    public void A_transaction_can_be_finalised_only_once()
    {
        var withdrawal = LedgerTransaction.StartWithdrawal(Guid.NewGuid(), 400, "wd-1", "hash", Now);
        var later = Now.AddMinutes(1);

        withdrawal.MarkSucceeded(later);

        withdrawal.Status.Should().Be(TransactionStatus.Succeeded);
        withdrawal.IsFinal.Should().BeTrue();
        withdrawal.CompletedAt.Should().Be(later);
        var again = () => withdrawal.MarkFailed(later);
        again.Should().Throw<InvalidOperationException>();
    }
}
```

`tests/Live.Tests/Wallet/Ledger/RequestHashTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class RequestHashTests
{
    [Fact]
    public void Equal_requests_hash_equally()
    {
        var userId = Guid.NewGuid();

        RequestHash.Compute("Withdrawal", userId, 400L).Should().Be(RequestHash.Compute("Withdrawal", userId, 400L));
    }

    [Fact]
    public void Any_difference_changes_the_hash()
    {
        var userId = Guid.NewGuid();

        RequestHash.Compute("Withdrawal", userId, 400L).Should().NotBe(RequestHash.Compute("Withdrawal", userId, 401L));
        RequestHash.Compute("a", "b").Should().NotBe(RequestHash.Compute("b", "a"));
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet.Ledger"`
Expected: build FAILS with `CS0246` for `LedgerTransaction`, `LedgerPosting`, `RequestHash`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Ledger/TransactionType.cs`:

```csharp
namespace Live.Wallet.Ledger;

public enum TransactionType
{
    TopUp,
    Withdrawal,
}
```

`src/Live.Wallet/Ledger/TransactionStatus.cs`:

```csharp
namespace Live.Wallet.Ledger;

public enum TransactionStatus
{
    Pending,
    Succeeded,
    Failed,
}
```

`src/Live.Wallet/Ledger/LedgerTransaction.cs`:

```csharp
namespace Live.Wallet.Ledger;

/// <summary>One money movement (a top-up or a withdrawal) and its lifecycle. Stored in the Transactions table.</summary>
public sealed class LedgerTransaction
{
    private LedgerTransaction()
    {
        ReferenceId = string.Empty;
        RequestHash = string.Empty;
    }

    public Guid Id { get; private set; }

    public Guid AccountId { get; private set; }

    public TransactionType Type { get; private set; }

    public TransactionStatus Status { get; private set; }

    public long Amount { get; private set; }

    /// <summary>The client's idempotency key.</summary>
    public string ReferenceId { get; private set; }

    public string? ProviderReferenceId { get; private set; }

    /// <summary>Fingerprint of the original request, to tell a safe retry from a reused ReferenceId.</summary>
    public string RequestHash { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    public DateTimeOffset? CompletedAt { get; private set; }

    public bool IsFinal => Status != TransactionStatus.Pending;

    public static LedgerTransaction StartTopUp(
        Guid accountId, long amount, string referenceId, string requestHash, string providerReferenceId, DateTimeOffset now) =>
        Start(TransactionType.TopUp, accountId, amount, referenceId, requestHash, providerReferenceId, now);

    public static LedgerTransaction StartWithdrawal(
        Guid accountId, long amount, string referenceId, string requestHash, DateTimeOffset now) =>
        Start(TransactionType.Withdrawal, accountId, amount, referenceId, requestHash, providerReferenceId: null, now);

    public void AssignProviderReference(string providerReferenceId) => ProviderReferenceId = providerReferenceId;

    public void MarkSucceeded(DateTimeOffset now) => Complete(TransactionStatus.Succeeded, now);

    public void MarkFailed(DateTimeOffset now) => Complete(TransactionStatus.Failed, now);

    private void Complete(TransactionStatus status, DateTimeOffset now)
    {
        if (IsFinal)
        {
            // Handlers check IsFinal first; reaching this is a bug, not an expected failure.
            throw new InvalidOperationException($"Transaction {Id} is already {Status}.");
        }

        Status = status;
        CompletedAt = now;
    }

    private static LedgerTransaction Start(
        TransactionType type,
        Guid accountId,
        long amount,
        string referenceId,
        string requestHash,
        string? providerReferenceId,
        DateTimeOffset now) =>
        new()
        {
            Id = Guid.CreateVersion7(now),
            AccountId = accountId,
            Type = type,
            Status = TransactionStatus.Pending,
            Amount = amount,
            ReferenceId = referenceId,
            RequestHash = requestHash,
            ProviderReferenceId = providerReferenceId,
            CreatedAt = now,
        };
}
```

`src/Live.Wallet/Ledger/LedgerPosting.cs`:

```csharp
using Practice.Common;

namespace Live.Wallet.Ledger;

/// <summary>Double-entry posting: every settled transaction is exactly one Debit and one Credit of the same amount.</summary>
public static class LedgerPosting
{
    /// <summary>
    /// Moves the transaction amount from debitAccount to creditAccount. Every check runs before any account changes,
    /// so a failure leaves both accounts untouched. debitFromHold means the amount was already held on debitAccount.
    /// </summary>
    public static Result<LedgerEntry[]> Post(
        LedgerTransaction transaction,
        Account debitAccount,
        Account creditAccount,
        bool debitFromHold,
        DateTimeOffset now)
    {
        var amount = transaction.Amount;
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (debitAccount.Currency != WalletCurrency.Code || creditAccount.Currency != WalletCurrency.Code)
        {
            return WalletErrors.UnsupportedCurrency;
        }

        if (debitAccount.Id == creditAccount.Id)
        {
            return WalletErrors.SameAccount;
        }

        var canDebit = debitAccount.CanDebit(amount, debitFromHold);
        if (canDebit.IsFailure)
        {
            return canDebit.Error;
        }

        LedgerEntry[] entries =
        [
            debitAccount.Debit(transaction.Id, amount, debitFromHold, now),
            creditAccount.Credit(transaction.Id, amount, now),
        ];
        return entries;
    }
}
```

`src/Live.Wallet/Ledger/RequestHash.cs`:

```csharp
using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace Live.Wallet.Ledger;

/// <summary>A stable fingerprint of a request's meaningful fields, stored with the transaction for idempotency checks.</summary>
public static class RequestHash
{
    public static string Compute(params object[] parts)
    {
        var canonical = string.Join('|', parts.Select(part => Convert.ToString(part, CultureInfo.InvariantCulture)));
        return Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(canonical)));
    }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet.Ledger"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Ledger tests/Live.Tests/Wallet/Ledger
git commit -m "feat(wallet): add ledger transactions and double-entry posting" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 3: Persistence, test database, concurrency retry

**Files:**
- Create: `src/Live.Wallet/Withdrawals/WithdrawalDetails.cs`, `src/Live.Wallet/Outbox/OutboxMessage.cs`
- Create: `src/Live.Wallet/Persistence/WalletDbContext.cs`, `AccountConfiguration.cs`, `LedgerTransactionConfiguration.cs`, `LedgerEntryConfiguration.cs`, `WithdrawalDetailsConfiguration.cs`, `OutboxMessageConfiguration.cs`, `ConcurrencyRetry.cs`
- Create (tests): `tests/Live.Tests/Wallet/TestDb.cs`, `AtomicSaveInterceptor.cs`, `ConcurrentWriteInterceptor.cs`
- Test: `tests/Live.Tests/Wallet/Persistence/WalletDbContextTests.cs`, `ConcurrencyRetryTests.cs`

**Interfaces:**
- Consumes: the ledger types (Tasks 1–2)
- Produces:
  - `WalletDbContext(DbContextOptions<WalletDbContext>)`, with sets `Accounts`, `Transactions`, `LedgerEntries`, `Withdrawals`, `OutboxMessages`
  - `WithdrawalDetails.Create(Guid transactionId, string bankCode, string accountNumber, string accountName)`
  - `OutboxMessage`:
    - `Create(string type, string payload, DateTimeOffset now)`
    - `MarkProcessed(DateTimeOffset)`, `RecordFailure(string)`
    - Properties: `Id`, `Type`, `Payload`, `OccurredAt`, `ProcessedAt`, `Attempts`, `LastError`
  - `ConcurrencyRetry`: `MaxAttempts = 3`, `ExecuteAsync<T>(WalletDbContext, Func<CancellationToken, Task<Result<T>>>, CancellationToken) → Task<Result<T>>`
  - Test helpers:
    - `TestDb.Start`, `TestDb.CreateContext(params IInterceptor[])`
    - Read helpers: `QueryAsync<T>(Func<WalletDbContext, Task<T>>)`, `GetWalletAsync(Guid userId)`, `GetSettlementAsync()`, `GetTransactionAsync(Guid)`
    - Seed helpers: `SeedWalletAsync(Guid userId, long balance, DateTimeOffset at) → Guid`, `SeedTopUpAsync(Guid accountId, long amount, DateTimeOffset at)`
    - `ConcurrentWriteInterceptor(int times, Func<Task> concurrentWrite)`

- [ ] **Step 1: Write the test infrastructure and failing tests**

`tests/Live.Tests/Wallet/AtomicSaveInterceptor.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Diagnostics;

namespace Live.Tests.Wallet;

/// <summary>
/// EF InMemory writes a SaveChanges row by row, so a concurrency conflict can leave earlier rows saved.
/// Postgres rolls the whole save back. Checking account versions before anything is written makes a conflicting save write nothing, as on Postgres.
/// </summary>
internal sealed class AtomicSaveInterceptor : SaveChangesInterceptor
{
    private readonly Func<WalletDbContext> _openStoreView;

    public AtomicSaveInterceptor(Func<WalletDbContext> openStoreView)
    {
        _openStoreView = openStoreView;
    }

    public override async ValueTask<InterceptionResult<int>> SavingChangesAsync(
        DbContextEventData eventData,
        InterceptionResult<int> result,
        CancellationToken cancellationToken = default)
    {
        if (eventData.Context is null)
        {
            return result;
        }

        var modifiedAccounts = eventData.Context.ChangeTracker.Entries<Account>()
            .Where(entry => entry.State == EntityState.Modified)
            .ToList();
        if (modifiedAccounts.Count == 0)
        {
            return result;
        }

        await using var store = _openStoreView();
        foreach (var entry in modifiedAccounts)
        {
            var storedVersion = await store.Accounts.AsNoTracking()
                .Where(account => account.Id == entry.Entity.Id)
                .Select(account => account.Version)
                .SingleAsync(cancellationToken);
            if (storedVersion != entry.Property(account => account.Version).OriginalValue)
            {
                throw new DbUpdateConcurrencyException($"Account {entry.Entity.Id} was modified concurrently.");
            }
        }

        return result;
    }
}
```

`tests/Live.Tests/Wallet/ConcurrentWriteInterceptor.cs`:

```csharp
using Microsoft.EntityFrameworkCore.Diagnostics;

namespace Live.Tests.Wallet;

/// <summary>Runs another writer's change just before this context saves, to force a concurrency conflict deterministically.</summary>
internal sealed class ConcurrentWriteInterceptor : SaveChangesInterceptor
{
    private readonly Func<Task> _concurrentWrite;
    private int _remaining;

    public ConcurrentWriteInterceptor(int times, Func<Task> concurrentWrite)
    {
        _remaining = times;
        _concurrentWrite = concurrentWrite;
    }

    public override async ValueTask<InterceptionResult<int>> SavingChangesAsync(
        DbContextEventData eventData,
        InterceptionResult<int> result,
        CancellationToken cancellationToken = default)
    {
        if (_remaining > 0)
        {
            _remaining--;
            await _concurrentWrite();
        }

        return result;
    }
}
```

`tests/Live.Tests/Wallet/TestDb.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Diagnostics;
using Microsoft.EntityFrameworkCore.Storage;

namespace Live.Tests.Wallet;

/// <summary>An isolated InMemory wallet database per test. Its contexts share one store, so a test can play several concurrent writers.</summary>
internal sealed class TestDb
{
    public static readonly DateTimeOffset Start = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    private readonly string _databaseName = Guid.NewGuid().ToString();
    private readonly InMemoryDatabaseRoot _root = new();

    public WalletDbContext CreateContext(params IInterceptor[] interceptors)
    {
        // AtomicSaveInterceptor runs last, so a test's concurrent write lands before the version check.
        var db = new WalletDbContext(BuildOptions([.. interceptors, new AtomicSaveInterceptor(OpenStoreView)]));
        db.Database.EnsureCreated();
        return db;
    }

    public async Task<T> QueryAsync<T>(Func<WalletDbContext, Task<T>> query)
    {
        await using var db = CreateContext();
        return await query(db);
    }

    public Task<Account> GetWalletAsync(Guid userId) =>
        QueryAsync(db => db.Accounts.AsNoTracking().SingleAsync(a => a.UserId == userId));

    public Task<Account> GetSettlementAsync() =>
        QueryAsync(db => db.Accounts.AsNoTracking().SingleAsync(a => a.Id == Account.ProviderSettlementId));

    public Task<LedgerTransaction> GetTransactionAsync(Guid transactionId) =>
        QueryAsync(db => db.Transactions.AsNoTracking().SingleAsync(t => t.Id == transactionId));

    /// <summary>Opens a wallet and, when balance is positive, funds it with a settled top-up.</summary>
    public async Task<Guid> SeedWalletAsync(Guid userId, long balance, DateTimeOffset at)
    {
        var wallet = Account.OpenUserWallet(userId, at);
        await using (var db = CreateContext())
        {
            db.Accounts.Add(wallet);
            await db.SaveChangesAsync();
        }

        if (balance > 0)
        {
            await SeedTopUpAsync(wallet.Id, balance, at);
        }

        return wallet.Id;
    }

    public async Task SeedTopUpAsync(Guid accountId, long amount, DateTimeOffset at)
    {
        await using var db = CreateContext();
        var wallet = await db.Accounts.SingleAsync(a => a.Id == accountId);
        var settlement = await db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId);
        var topUp = LedgerTransaction.StartTopUp(accountId, amount, $"seed-{Guid.NewGuid():N}", "seed", $"chg_seed_{Guid.NewGuid():N}", at);
        var entries = LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, at).Value;
        topUp.MarkSucceeded(at);
        db.Transactions.Add(topUp);
        db.LedgerEntries.AddRange(entries);
        await db.SaveChangesAsync();
    }

    private WalletDbContext OpenStoreView() => new(BuildOptions([]));

    private DbContextOptions<WalletDbContext> BuildOptions(IInterceptor[] interceptors) =>
        new DbContextOptionsBuilder<WalletDbContext>()
            .UseInMemoryDatabase(_databaseName, _root)
            .AddInterceptors(interceptors)
            .Options;
}
```

`tests/Live.Tests/Wallet/Persistence/WalletDbContextTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Outbox;
using Microsoft.EntityFrameworkCore;

namespace Live.Tests.Wallet.Persistence;

public sealed class WalletDbContextTests
{
    private readonly TestDb _testDb = new();

    [Fact]
    public async Task The_provider_settlement_account_is_seeded()
    {
        var settlement = await _testDb.GetSettlementAsync();

        settlement.Type.Should().Be(AccountType.System);
        settlement.Name.Should().Be(Account.ProviderSettlementName);
        settlement.UserId.Should().BeNull();
        settlement.Balance.Should().Be(0);
        settlement.Currency.Should().Be("NGN");
    }

    [Fact]
    public async Task A_posted_top_up_round_trips()
    {
        var userId = Guid.NewGuid();

        var walletId = await _testDb.SeedWalletAsync(userId, 5_000, TestDb.Start);

        var wallet = await _testDb.GetWalletAsync(userId);
        wallet.Id.Should().Be(walletId);
        wallet.Balance.Should().Be(5_000);
        wallet.AvailableBalance.Should().Be(5_000);
        (await _testDb.GetSettlementAsync()).Balance.Should().Be(-5_000);
        var transaction = await _testDb.QueryAsync(db => db.Transactions.AsNoTracking().SingleAsync(t => t.AccountId == walletId));
        transaction.Status.Should().Be(TransactionStatus.Succeeded);
        (await _testDb.QueryAsync(db => db.LedgerEntries.CountAsync(e => e.TransactionId == transaction.Id))).Should().Be(2);
    }

    [Fact]
    public async Task Saving_a_stale_account_writes_nothing()
    {
        var userId = Guid.NewGuid();
        var walletId = await _testDb.SeedWalletAsync(userId, 1_000, TestDb.Start);
        await using var first = _testDb.CreateContext();
        await using var second = _testDb.CreateContext();
        var staleCopy = await first.Accounts.SingleAsync(a => a.Id == walletId);
        var freshCopy = await second.Accounts.SingleAsync(a => a.Id == walletId);
        freshCopy.PlaceHold(100);
        await second.SaveChangesAsync();

        staleCopy.PlaceHold(200);
        first.OutboxMessages.Add(OutboxMessage.Create("test.message", "{}", TestDb.Start));
        var save = () => first.SaveChangesAsync();

        await save.Should().ThrowAsync<DbUpdateConcurrencyException>();
        (await _testDb.QueryAsync(db => db.OutboxMessages.CountAsync())).Should().Be(0);
        (await _testDb.GetWalletAsync(userId)).AvailableBalance.Should().Be(900);
    }
}
```

`tests/Live.Tests/Wallet/Persistence/ConcurrencyRetryTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Tests.Wallet.Persistence;

public sealed class ConcurrencyRetryTests
{
    private readonly TestDb _testDb = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Saves_the_operation_on_the_first_attempt()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 1_000, TestDb.Start);
        await using var db = _testDb.CreateContext();
        var attempts = 0;

        var result = await ConcurrencyRetry.ExecuteAsync<long>(db, async ct =>
        {
            attempts++;
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId, ct);
            return wallet.PlaceHold(100);
        }, CancellationToken.None);

        result.Value.Should().Be(900);
        attempts.Should().Be(1);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(900);
    }

    [Fact]
    public async Task Retries_from_fresh_state_after_a_concurrent_write()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 1_000, TestDb.Start);
        var interceptor = new ConcurrentWriteInterceptor(times: 1, () => HoldElsewhereAsync(walletId, 300));
        await using var db = _testDb.CreateContext(interceptor);
        var attempts = 0;

        var result = await ConcurrencyRetry.ExecuteAsync<long>(db, async ct =>
        {
            attempts++;
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId, ct);
            return wallet.PlaceHold(100);
        }, CancellationToken.None);

        result.Value.Should().Be(600);
        attempts.Should().Be(2);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(600);
    }

    [Fact]
    public async Task Gives_up_after_the_maximum_attempts()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 1_000, TestDb.Start);
        var interceptor = new ConcurrentWriteInterceptor(ConcurrencyRetry.MaxAttempts, () => HoldElsewhereAsync(walletId, 10));
        await using var db = _testDb.CreateContext(interceptor);
        var attempts = 0;

        var result = await ConcurrencyRetry.ExecuteAsync<long>(db, async ct =>
        {
            attempts++;
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId, ct);
            return wallet.PlaceHold(100);
        }, CancellationToken.None);

        result.Error.Code.Should().Be("Concurrency.Exhausted");
        attempts.Should().Be(ConcurrencyRetry.MaxAttempts);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(1_000 - (10 * ConcurrencyRetry.MaxAttempts));
    }

    [Fact]
    public async Task A_failed_operation_is_not_saved()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 1_000, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var result = await ConcurrencyRetry.ExecuteAsync<long>(db, async ct =>
        {
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId, ct);
            wallet.PlaceHold(100);
            return Result<long>.Failure(WalletErrors.InsufficientFunds);
        }, CancellationToken.None);

        result.IsFailure.Should().BeTrue();
        await db.SaveChangesAsync();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(1_000);
    }

    private async Task HoldElsewhereAsync(Guid walletId, long amount)
    {
        await using var other = _testDb.CreateContext();
        var wallet = await other.Accounts.SingleAsync(a => a.Id == walletId);
        wallet.PlaceHold(amount);
        await other.SaveChangesAsync();
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet.Persistence"`
Expected: build FAILS with `CS0246` for `WalletDbContext`, `OutboxMessage`, `ConcurrencyRetry`.

- [ ] **Step 3: Implement the entities**

`src/Live.Wallet/Withdrawals/WithdrawalDetails.cs`:

```csharp
namespace Live.Wallet.Withdrawals;

/// <summary>Where a withdrawal pays out to. One row per withdrawal transaction.</summary>
public sealed class WithdrawalDetails
{
    private WithdrawalDetails()
    {
        BankCode = string.Empty;
        AccountNumber = string.Empty;
        AccountName = string.Empty;
    }

    public Guid TransactionId { get; private set; }

    public string BankCode { get; private set; }

    public string AccountNumber { get; private set; }

    public string AccountName { get; private set; }

    public static WithdrawalDetails Create(Guid transactionId, string bankCode, string accountNumber, string accountName) =>
        new() { TransactionId = transactionId, BankCode = bankCode, AccountNumber = accountNumber, AccountName = accountName };
}
```

`src/Live.Wallet/Outbox/OutboxMessage.cs`:

```csharp
namespace Live.Wallet.Outbox;

/// <summary>An event saved in the same database transaction as the ledger change it describes, then relayed to the queue.</summary>
public sealed class OutboxMessage
{
    private const int MaxErrorLength = 2_000;

    private OutboxMessage()
    {
        Type = string.Empty;
        Payload = string.Empty;
    }

    public Guid Id { get; private set; }

    public string Type { get; private set; }

    public string Payload { get; private set; }

    public DateTimeOffset OccurredAt { get; private set; }

    public DateTimeOffset? ProcessedAt { get; private set; }

    public int Attempts { get; private set; }

    public string? LastError { get; private set; }

    public static OutboxMessage Create(string type, string payload, DateTimeOffset now) =>
        new() { Id = Guid.CreateVersion7(now), Type = type, Payload = payload, OccurredAt = now };

    public void MarkProcessed(DateTimeOffset now)
    {
        Attempts++;
        ProcessedAt = now;
    }

    public void RecordFailure(string error)
    {
        Attempts++;
        LastError = error.Length > MaxErrorLength ? error[..MaxErrorLength] : error;
    }
}
```

- [ ] **Step 4: Implement the DbContext and configurations**

`src/Live.Wallet/Persistence/WalletDbContext.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Outbox;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;

namespace Live.Wallet.Persistence;

public sealed class WalletDbContext : DbContext
{
    public WalletDbContext(DbContextOptions<WalletDbContext> options)
        : base(options)
    {
    }

    public DbSet<Account> Accounts => Set<Account>();

    public DbSet<LedgerTransaction> Transactions => Set<LedgerTransaction>();

    public DbSet<LedgerEntry> LedgerEntries => Set<LedgerEntry>();

    public DbSet<WithdrawalDetails> Withdrawals => Set<WithdrawalDetails>();

    public DbSet<OutboxMessage> OutboxMessages => Set<OutboxMessage>();

    protected override void OnModelCreating(ModelBuilder modelBuilder) =>
        modelBuilder.ApplyConfigurationsFromAssembly(typeof(WalletDbContext).Assembly);
}
```

`src/Live.Wallet/Persistence/AccountConfiguration.cs`:

```csharp
using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class AccountConfiguration : IEntityTypeConfiguration<Account>
{
    private static readonly DateTimeOffset SeededAt = new(2026, 1, 1, 0, 0, 0, TimeSpan.Zero);

    public void Configure(EntityTypeBuilder<Account> builder)
    {
        builder.ToTable("Accounts", table =>
        {
            table.HasCheckConstraint(
                "CK_Accounts_UserWalletBalances",
                "\"Type\" <> 'User' OR (\"Balance\" >= 0 AND \"AvailableBalance\" >= 0 AND \"AvailableBalance\" <= \"Balance\")");
            table.HasCheckConstraint("CK_Accounts_Currency", "\"Currency\" = 'NGN'");
        });
        builder.HasKey(a => a.Id);
        builder.Property(a => a.Type).HasConversion<string>().HasMaxLength(16);
        builder.Property(a => a.Name).HasMaxLength(64);
        builder.Property(a => a.Currency).HasMaxLength(3).IsFixedLength();
        builder.Property(a => a.Version).IsConcurrencyToken();
        builder.HasIndex(a => new { a.UserId, a.Currency }).IsUnique().HasFilter("\"UserId\" IS NOT NULL");
        builder.HasIndex(a => a.Name).IsUnique().HasFilter("\"Name\" IS NOT NULL");
        builder.HasData(new
        {
            Id = Account.ProviderSettlementId,
            Type = AccountType.System,
            Name = Account.ProviderSettlementName,
            Currency = WalletCurrency.Code,
            Balance = 0L,
            AvailableBalance = 0L,
            Version = 0,
            CreatedAt = SeededAt,
        });
    }
}
```

`src/Live.Wallet/Persistence/LedgerTransactionConfiguration.cs`:

```csharp
using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class LedgerTransactionConfiguration : IEntityTypeConfiguration<LedgerTransaction>
{
    public void Configure(EntityTypeBuilder<LedgerTransaction> builder)
    {
        builder.ToTable("Transactions", table => table.HasCheckConstraint("CK_Transactions_Amount", "\"Amount\" > 0"));
        builder.HasKey(t => t.Id);
        builder.Property(t => t.Type).HasConversion<string>().HasMaxLength(16);
        builder.Property(t => t.Status).HasConversion<string>().HasMaxLength(16);
        builder.Property(t => t.ReferenceId).HasMaxLength(128);
        builder.Property(t => t.ProviderReferenceId).HasMaxLength(128);
        builder.Property(t => t.RequestHash).HasMaxLength(64);
        builder.HasIndex(t => t.ReferenceId).IsUnique();
        builder.HasIndex(t => t.ProviderReferenceId).IsUnique().HasFilter("\"ProviderReferenceId\" IS NOT NULL");
        builder.HasIndex(t => new { t.AccountId, t.CreatedAt });
        builder.HasIndex(t => new { t.Type, t.Status, t.CreatedAt });
        builder.HasOne<Account>().WithMany().HasForeignKey(t => t.AccountId).OnDelete(DeleteBehavior.Restrict);
    }
}
```

`src/Live.Wallet/Persistence/LedgerEntryConfiguration.cs`:

```csharp
using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class LedgerEntryConfiguration : IEntityTypeConfiguration<LedgerEntry>
{
    public void Configure(EntityTypeBuilder<LedgerEntry> builder)
    {
        builder.ToTable("LedgerEntries", table => table.HasCheckConstraint("CK_LedgerEntries_Amount", "\"Amount\" > 0"));
        builder.HasKey(e => e.Id);
        builder.Property(e => e.Direction).HasConversion<string>().HasMaxLength(8);
        builder.Property(e => e.Currency).HasMaxLength(3).IsFixedLength();
        builder.HasIndex(e => e.TransactionId);
        builder.HasIndex(e => new { e.AccountId, e.CreatedAt });
        builder.HasOne<LedgerTransaction>().WithMany().HasForeignKey(e => e.TransactionId).OnDelete(DeleteBehavior.Restrict);
        builder.HasOne<Account>().WithMany().HasForeignKey(e => e.AccountId).OnDelete(DeleteBehavior.Restrict);
    }
}
```

`src/Live.Wallet/Persistence/WithdrawalDetailsConfiguration.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class WithdrawalDetailsConfiguration : IEntityTypeConfiguration<WithdrawalDetails>
{
    public void Configure(EntityTypeBuilder<WithdrawalDetails> builder)
    {
        builder.ToTable("Withdrawals");
        builder.HasKey(w => w.TransactionId);
        builder.Property(w => w.BankCode).HasMaxLength(16);
        builder.Property(w => w.AccountNumber).HasMaxLength(10);
        builder.Property(w => w.AccountName).HasMaxLength(128);
        builder.HasOne<LedgerTransaction>().WithOne().HasForeignKey<WithdrawalDetails>(w => w.TransactionId).OnDelete(DeleteBehavior.Restrict);
    }
}
```

`src/Live.Wallet/Persistence/OutboxMessageConfiguration.cs`:

```csharp
using Live.Wallet.Outbox;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class OutboxMessageConfiguration : IEntityTypeConfiguration<OutboxMessage>
{
    public void Configure(EntityTypeBuilder<OutboxMessage> builder)
    {
        builder.ToTable("OutboxMessages");
        builder.HasKey(m => m.Id);
        builder.Property(m => m.Type).HasMaxLength(128);
        builder.Property(m => m.Payload).HasColumnType("jsonb");
        builder.Property(m => m.LastError).HasMaxLength(2_000);
        builder.HasIndex(m => m.OccurredAt).HasFilter("\"ProcessedAt\" IS NULL");
    }
}
```

`src/Live.Wallet/Persistence/ConcurrencyRetry.cs`:

```csharp
using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Persistence;

/// <summary>
/// Runs load → change → save. If another writer changed a tracked account first, the save fails on its Version token.
/// The operation then reruns from a clean change tracker, so balance checks see the latest state.
/// </summary>
public static class ConcurrencyRetry
{
    public const int MaxAttempts = 3;

    public static async Task<Result<T>> ExecuteAsync<T>(
        WalletDbContext db,
        Func<CancellationToken, Task<Result<T>>> operation,
        CancellationToken cancellationToken)
    {
        for (var attempt = 1; ; attempt++)
        {
            var result = await operation(cancellationToken).ConfigureAwait(false);
            if (result.IsFailure)
            {
                db.ChangeTracker.Clear();
                return result;
            }

            try
            {
                await db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
                return result;
            }
            catch (DbUpdateConcurrencyException)
            {
                db.ChangeTracker.Clear();
                if (attempt == MaxAttempts)
                {
                    return WalletErrors.ConcurrencyExhausted;
                }
            }
        }
    }
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet"`
Expected: PASS, including all of Tasks 1–2.

- [ ] **Step 6: Commit**

```bash
git add src/Live.Wallet tests/Live.Tests/Wallet
git commit -m "feat(wallet): add EF Core persistence and optimistic concurrency retry" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 4: Postgres migration

**Files:**
- Modify: `src/Live.Wallet/Live.Wallet.csproj`
- Create: `src/Live.Wallet/Persistence/WalletDbContextDesignTimeFactory.cs`
- Create (generated): `src/Live.Wallet/Persistence/Migrations/*`
- Test: `tests/Live.Tests/Wallet/Persistence/WalletModelTests.cs`

**Interfaces:**
- Consumes: `WalletDbContext` and its configurations (Task 3)
- Produces: the initial migration `InitialWallet`, which Task 15 applies in Development with `Database.MigrateAsync()`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Persistence/WalletModelTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Infrastructure;
using Microsoft.EntityFrameworkCore.Metadata;

namespace Live.Tests.Wallet.Persistence;

/// <summary>Checks the Postgres model. InMemory can't enforce these rules, so this confirms the migration declares them.</summary>
public sealed class WalletModelTests
{
    [Fact]
    public void The_migrations_match_the_model()
    {
        using var db = CreateNpgsqlContext();

        db.Database.HasPendingModelChanges().Should().BeFalse();
    }

    [Fact]
    public void Reference_ids_are_unique()
    {
        using var db = CreateNpgsqlContext();
        var transactions = Entity<LedgerTransaction>(db);

        transactions.GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Count == 1 && i.Properties[0].Name == nameof(LedgerTransaction.ReferenceId));
        transactions.GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Count == 1 && i.Properties[0].Name == nameof(LedgerTransaction.ProviderReferenceId)
            && i.GetFilter() == "\"ProviderReferenceId\" IS NOT NULL");
    }

    [Fact]
    public void A_user_has_one_wallet_per_currency()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Select(p => p.Name).SequenceEqual(new[] { nameof(Account.UserId), nameof(Account.Currency) }));
    }

    [Fact]
    public void The_account_version_is_a_concurrency_token()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetProperty(nameof(Account.Version)).IsConcurrencyToken.Should().BeTrue();
    }

    [Fact]
    public void Balance_and_amount_rules_are_check_constraints()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetCheckConstraints().Select(c => c.Name)
            .Should().Contain("CK_Accounts_UserWalletBalances").And.Contain("CK_Accounts_Currency");
        Entity<LedgerTransaction>(db).GetCheckConstraints().Select(c => c.Name).Should().Contain("CK_Transactions_Amount");
        Entity<LedgerEntry>(db).GetCheckConstraints().Select(c => c.Name).Should().Contain("CK_LedgerEntries_Amount");
    }

    private static WalletDbContext CreateNpgsqlContext() =>
        new(new DbContextOptionsBuilder<WalletDbContext>().UseNpgsql("Host=localhost;Database=wallet_model_tests").Options);

    // The design-time model keeps check constraints and index filters; the runtime model drops them.
    private static IEntityType Entity<T>(WalletDbContext db) =>
        db.GetService<IDesignTimeModel>().Model.FindEntityType(typeof(T))
        ?? throw new InvalidOperationException($"{typeof(T).Name} is not mapped.");
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~WalletModelTests"`
Expected: `The_migrations_match_the_model` FAILS because there is no migration yet, so `HasPendingModelChanges()` returns true. The others pass, since they read the model from Task 3. That's fine: they guard against regressions.

- [ ] **Step 3: Add the design-time pieces**

In `src/Live.Wallet/Live.Wallet.csproj`, add to the package `ItemGroup`:

```xml
    <PackageReference Include="Microsoft.EntityFrameworkCore.Design">
      <PrivateAssets>all</PrivateAssets>
      <IncludeAssets>runtime; build; native; contentfiles; analyzers; buildtransitive</IncludeAssets>
    </PackageReference>
```

`src/Live.Wallet/Persistence/WalletDbContextDesignTimeFactory.cs`:

```csharp
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Design;

namespace Live.Wallet.Persistence;

/// <summary>Used only by `dotnet ef`. Generating migrations needs a provider, not a reachable database.</summary>
internal sealed class WalletDbContextDesignTimeFactory : IDesignTimeDbContextFactory<WalletDbContext>
{
    public WalletDbContext CreateDbContext(string[] args) =>
        new(new DbContextOptionsBuilder<WalletDbContext>().UseNpgsql("Host=localhost;Database=wallet_design").Options);
}
```

- [ ] **Step 4: Generate the migration**

Run:

```bash
dotnet ef migrations add InitialWallet --project src/Live.Wallet --startup-project src/Live.Wallet --output-dir Persistence/Migrations
```

Expected: `Done.` (a warning that the tools version 10.0.8 is older than the runtime is fine). There are three new files under `src/Live.Wallet/Persistence/Migrations/`.

Inspect the SQL:

```bash
dotnet ef migrations script --project src/Live.Wallet --startup-project src/Live.Wallet | grep -E 'CK_|UNIQUE INDEX|INSERT INTO "Accounts"'
```

Expected: lines for `CK_Accounts_UserWalletBalances`, `CK_Accounts_Currency`, `CK_Transactions_Amount` and `CK_LedgerEntries_Amount`. Also unique indexes on `"ReferenceId"`, on `"ProviderReferenceId"` (with `WHERE "ProviderReferenceId" IS NOT NULL`) and on `("UserId", "Currency")`, and the `INSERT INTO "Accounts"` seed for `ProviderSettlement`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~WalletModelTests"`
Expected: PASS (5 tests).

- [ ] **Step 6: Commit**

```bash
git add src/Live.Wallet tests/Live.Tests/Wallet/Persistence/WalletModelTests.cs
git commit -m "feat(wallet): add initial Postgres migration for the ledger" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 5: Wallets slice (create, get, list transactions)

**Files:**
- Create: `src/Live.Wallet/Wallets/WalletResponse.cs`, `WalletCreation.cs`, `TransactionResponse.cs`, `IdempotentResponse.cs`, `WalletAccounts.cs`, `Idempotency.cs`, `CreateWalletHandler.cs`, `GetWalletHandler.cs`, `ListTransactionsHandler.cs`
- Test: `tests/Live.Tests/Wallet/Wallets/WalletHandlersTests.cs`

**Interfaces:**
- Consumes: `WalletDbContext`, `Account`, `LedgerTransaction`, `IClock`
- Produces:
  - Response types:
    - `WalletResponse { AccountId, UserId, Balance, AvailableBalance, Currency }` + `From(Account)`
    - `WalletCreation { Wallet, Created }`
    - `TransactionResponse { Id, Type, Status, Amount, Currency, ReferenceId, ProviderReferenceId, CreatedAt, CompletedAt }` + `From(LedgerTransaction)`
    - `IdempotentResponse { Transaction, Replayed }`
  - Internal helpers:
    - `WalletAccounts.FindAsync(db, userId, ct) → Task<Account?>`
    - `WalletAccounts.GetOrCreateAsync(db, userId, now, ct) → Task<(Account Account, bool Created)>`
    - `Idempotency.FindReplayAsync(db, referenceId, requestHash, ct) → Task<Result<IdempotentResponse>?>`
  - Handlers:
    - `CreateWalletHandler(WalletDbContext, IClock).HandleAsync(Guid userId, ct) → Result<WalletCreation>`
    - `GetWalletHandler(WalletDbContext).HandleAsync(Guid userId, ct) → Result<WalletResponse>`
    - `ListTransactionsHandler(WalletDbContext).HandleAsync(Guid userId, ct) → Result<TransactionResponse[]>`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Wallets/WalletHandlersTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Tests.Wallet.Wallets;

public sealed class WalletHandlersTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Create_opens_exactly_one_empty_wallet_per_user()
    {
        await using var db = _testDb.CreateContext();
        var handler = new CreateWalletHandler(db, _clock);

        var first = await handler.HandleAsync(_userId, CancellationToken.None);
        var second = await handler.HandleAsync(_userId, CancellationToken.None);

        first.Value.Created.Should().BeTrue();
        first.Value.Wallet.UserId.Should().Be(_userId);
        first.Value.Wallet.Balance.Should().Be(0);
        first.Value.Wallet.Currency.Should().Be("NGN");
        second.Value.Created.Should().BeFalse();
        second.Value.Wallet.AccountId.Should().Be(first.Value.Wallet.AccountId);
        (await _testDb.QueryAsync(q => q.Accounts.CountAsync(a => a.UserId == _userId))).Should().Be(1);
    }

    [Fact]
    public async Task Get_for_an_unknown_user_returns_not_found()
    {
        await using var db = _testDb.CreateContext();

        var result = await new GetWalletHandler(db).HandleAsync(_userId, CancellationToken.None);

        result.Error.Code.Should().Be("Wallet.NotFound");
    }

    [Fact]
    public async Task Get_returns_the_balances()
    {
        await _testDb.SeedWalletAsync(_userId, 2_500, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var wallet = (await new GetWalletHandler(db).HandleAsync(_userId, CancellationToken.None)).Value;

        wallet.Balance.Should().Be(2_500);
        wallet.AvailableBalance.Should().Be(2_500);
    }

    [Fact]
    public async Task List_returns_transactions_newest_first()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 1_000, TestDb.Start);
        await _testDb.SeedTopUpAsync(walletId, 200, TestDb.Start.AddMinutes(5));
        await using var db = _testDb.CreateContext();

        var transactions = (await new ListTransactionsHandler(db).HandleAsync(_userId, CancellationToken.None)).Value;

        transactions.Select(t => t.Amount).Should().Equal(200, 1_000);
        transactions.Should().OnlyContain(t => t.Type == "TopUp" && t.Status == "Succeeded" && t.Currency == "NGN");
    }

    [Fact]
    public async Task List_for_an_unknown_user_returns_not_found()
    {
        await using var db = _testDb.CreateContext();

        var result = await new ListTransactionsHandler(db).HandleAsync(_userId, CancellationToken.None);

        result.Error.Code.Should().Be("Wallet.NotFound");
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~WalletHandlersTests"`
Expected: build FAILS with `CS0246` for `CreateWalletHandler`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Wallets/WalletResponse.cs`:

```csharp
using Live.Wallet.Ledger;

namespace Live.Wallet.Wallets;

public sealed record WalletResponse
{
    public required Guid AccountId { get; init; }

    public required Guid UserId { get; init; }

    public required long Balance { get; init; }

    public required long AvailableBalance { get; init; }

    public required string Currency { get; init; }

    public static WalletResponse From(Account account) =>
        new()
        {
            AccountId = account.Id,
            UserId = account.UserId.GetValueOrDefault(),
            Balance = account.Balance,
            AvailableBalance = account.AvailableBalance,
            Currency = account.Currency,
        };
}
```

`src/Live.Wallet/Wallets/WalletCreation.cs`:

```csharp
namespace Live.Wallet.Wallets;

public sealed record WalletCreation
{
    public required WalletResponse Wallet { get; init; }

    public required bool Created { get; init; }
}
```

`src/Live.Wallet/Wallets/TransactionResponse.cs`:

```csharp
using Live.Wallet.Ledger;

namespace Live.Wallet.Wallets;

public sealed record TransactionResponse
{
    public required Guid Id { get; init; }

    public required string Type { get; init; }

    public required string Status { get; init; }

    public required long Amount { get; init; }

    public required string Currency { get; init; }

    public required string ReferenceId { get; init; }

    public string? ProviderReferenceId { get; init; }

    public required DateTimeOffset CreatedAt { get; init; }

    public DateTimeOffset? CompletedAt { get; init; }

    public static TransactionResponse From(LedgerTransaction transaction) =>
        new()
        {
            Id = transaction.Id,
            Type = transaction.Type.ToString(),
            Status = transaction.Status.ToString(),
            Amount = transaction.Amount,
            Currency = WalletCurrency.Code,
            ReferenceId = transaction.ReferenceId,
            ProviderReferenceId = transaction.ProviderReferenceId,
            CreatedAt = transaction.CreatedAt,
            CompletedAt = transaction.CompletedAt,
        };
}
```

`src/Live.Wallet/Wallets/IdempotentResponse.cs`:

```csharp
namespace Live.Wallet.Wallets;

/// <summary>The transaction a command produced. Replayed is true when an earlier identical request already produced it.</summary>
public sealed record IdempotentResponse
{
    public required TransactionResponse Transaction { get; init; }

    public required bool Replayed { get; init; }
}
```

`src/Live.Wallet/Wallets/WalletAccounts.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;

namespace Live.Wallet.Wallets;

internal static class WalletAccounts
{
    public static Task<Account?> FindAsync(WalletDbContext db, Guid userId, CancellationToken cancellationToken) =>
        db.Accounts.SingleOrDefaultAsync(a => a.UserId == userId && a.Type == AccountType.User, cancellationToken);

    /// <summary>Returns the user's wallet, adding a new one to the context (not saved) when none exists.</summary>
    public static async Task<(Account Account, bool Created)> GetOrCreateAsync(
        WalletDbContext db, Guid userId, DateTimeOffset now, CancellationToken cancellationToken)
    {
        var existing = await FindAsync(db, userId, cancellationToken).ConfigureAwait(false);
        if (existing is not null)
        {
            return (existing, false);
        }

        var account = Account.OpenUserWallet(userId, now);
        db.Accounts.Add(account);
        return (account, true);
    }
}
```

`src/Live.Wallet/Wallets/Idempotency.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Wallets;

internal static class Idempotency
{
    /// <summary>
    /// Null when the referenceId is new. Otherwise the original transaction when the request matches,
    /// or DuplicateReference when the same referenceId arrives with a different request.
    /// </summary>
    public static async Task<Result<IdempotentResponse>?> FindReplayAsync(
        WalletDbContext db, string referenceId, string requestHash, CancellationToken cancellationToken)
    {
        var existing = await db.Transactions.AsNoTracking()
            .SingleOrDefaultAsync(t => t.ReferenceId == referenceId, cancellationToken)
            .ConfigureAwait(false);
        if (existing is null)
        {
            return null;
        }

        if (existing.RequestHash != requestHash)
        {
            return WalletErrors.DuplicateReference;
        }

        return new IdempotentResponse { Transaction = TransactionResponse.From(existing), Replayed = true };
    }
}
```

`src/Live.Wallet/Wallets/CreateWalletHandler.cs`:

```csharp
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Wallets;

public sealed class CreateWalletHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public CreateWalletHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public async Task<Result<WalletCreation>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var (account, created) = await WalletAccounts.GetOrCreateAsync(_db, userId, _clock.UtcNow, cancellationToken).ConfigureAwait(false);
        if (!created)
        {
            return new WalletCreation { Wallet = WalletResponse.From(account), Created = false };
        }

        try
        {
            await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        }
        catch (DbUpdateException exception) when (exception is not DbUpdateConcurrencyException)
        {
            // A concurrent create won the unique (UserId, Currency) index on Postgres; return the winner's wallet.
            _db.ChangeTracker.Clear();
            var winner = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
            if (winner is null)
            {
                throw;
            }

            return new WalletCreation { Wallet = WalletResponse.From(winner), Created = false };
        }

        return new WalletCreation { Wallet = WalletResponse.From(account), Created = true };
    }
}
```

`src/Live.Wallet/Wallets/GetWalletHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Practice.Common;

namespace Live.Wallet.Wallets;

public sealed class GetWalletHandler
{
    private readonly WalletDbContext _db;

    public GetWalletHandler(WalletDbContext db)
    {
        _db = db;
    }

    public async Task<Result<WalletResponse>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var account = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
        if (account is null)
        {
            return WalletErrors.WalletNotFound;
        }

        return WalletResponse.From(account);
    }
}
```

`src/Live.Wallet/Wallets/ListTransactionsHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Wallets;

public sealed class ListTransactionsHandler
{
    private const int PageSize = 100;

    private readonly WalletDbContext _db;

    public ListTransactionsHandler(WalletDbContext db)
    {
        _db = db;
    }

    public async Task<Result<TransactionResponse[]>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var account = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
        if (account is null)
        {
            return WalletErrors.WalletNotFound;
        }

        var transactions = await _db.Transactions.AsNoTracking()
            .Where(t => t.AccountId == account.Id)
            .OrderByDescending(t => t.CreatedAt)
            .ThenByDescending(t => t.Id)
            .Take(PageSize)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        return transactions.Select(TransactionResponse.From).ToArray();
    }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~WalletHandlersTests"`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Wallets tests/Live.Tests/Wallet/Wallets
git commit -m "feat(wallet): add wallet create, balance and transaction history handlers" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 6: Payment provider abstraction and fake

**Files:**
- Create: `src/Live.Wallet/Providers/IPaymentProvider.cs`, `ChargeInitiation.cs`, `PayoutInstruction.cs`, `PayoutInitiation.cs`, `PayoutOutcome.cs`, `PayoutStatus.cs`, `FakeProviderRecord.cs`, `FakePaymentProvider.cs`
- Test: `tests/Live.Tests/Wallet/Providers/FakePaymentProviderTests.cs`

**Interfaces:**
- Produces:
  - `IPaymentProvider`:
    - `InitiateChargeAsync(string referenceId, long amount, CancellationToken) → Task<ChargeInitiation>`
    - `InitiatePayoutAsync(PayoutInstruction, CancellationToken) → Task<PayoutInitiation>`
    - `GetPayoutStatusAsync(string referenceId, CancellationToken) → Task<PayoutStatus>`
  - Value types:
    - `ChargeInitiation { ProviderReferenceId }`
    - `PayoutInstruction { ReferenceId, Amount, BankCode, AccountNumber, AccountName }`
    - `PayoutInitiation { Outcome, ProviderReferenceId?, FailureReason? }` with `Accepted(string)` and `Rejected(string)`
    - `PayoutOutcome { Accepted, Rejected }`, `PayoutStatus { Pending, Succeeded, Failed, NotFound }`
  - `FakePaymentProvider`:
    - Test controls: `PayoutBehaviour` (a `Func<PayoutInstruction, PayoutInitiation>?`; if it throws, that simulates a transient failure), `PayoutAttempts`, `SetPayoutStatus(string referenceId, PayoutStatus)`
    - `FindByProviderReference(string) → FakeProviderRecord?`
  - `FakeProviderRecord { IsPayout, ReferenceId, ProviderReferenceId, Amount, Status }`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Providers/FakePaymentProviderTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Providers;

namespace Live.Tests.Wallet.Providers;

public sealed class FakePaymentProviderTests
{
    private readonly FakePaymentProvider _provider = new();

    [Fact]
    public async Task A_charge_is_idempotent_per_reference()
    {
        var first = await _provider.InitiateChargeAsync("topup-1", 5_000, CancellationToken.None);
        var second = await _provider.InitiateChargeAsync("topup-1", 5_000, CancellationToken.None);

        first.ProviderReferenceId.Should().StartWith("chg_");
        second.ProviderReferenceId.Should().Be(first.ProviderReferenceId);
        var record = _provider.FindByProviderReference(first.ProviderReferenceId);
        record.Should().NotBeNull();
        record?.IsPayout.Should().BeFalse();
        record?.Amount.Should().Be(5_000);
        record?.ReferenceId.Should().Be("topup-1");
    }

    [Fact]
    public async Task A_payout_is_accepted_by_default_and_idempotent_per_reference()
    {
        var first = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);
        var second = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        first.Outcome.Should().Be(PayoutOutcome.Accepted);
        first.ProviderReferenceId.Should().StartWith("trf_");
        second.ProviderReferenceId.Should().Be(first.ProviderReferenceId);
        _provider.PayoutAttempts.Should().Be(2);
        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.Pending);
    }

    [Fact]
    public async Task A_scripted_rejection_is_returned_and_not_recorded()
    {
        _provider.PayoutBehaviour = _ => PayoutInitiation.Rejected("Invalid account");

        var result = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        result.Outcome.Should().Be(PayoutOutcome.Rejected);
        result.FailureReason.Should().Be("Invalid account");
        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.NotFound);
    }

    [Fact]
    public async Task SetPayoutStatus_changes_the_reported_status()
    {
        await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        _provider.SetPayoutStatus("wd-1", PayoutStatus.Succeeded);

        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.Succeeded);
    }

    [Fact]
    public async Task An_unknown_payout_is_not_found()
    {
        (await _provider.GetPayoutStatusAsync("missing", CancellationToken.None)).Should().Be(PayoutStatus.NotFound);
    }

    private static PayoutInstruction Instruction(string referenceId) =>
        new() { ReferenceId = referenceId, Amount = 4_000, BankCode = "058", AccountNumber = "0123456789", AccountName = "Ada Lovelace" };
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~FakePaymentProviderTests"`
Expected: build FAILS with `CS0246` for `FakePaymentProvider`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Providers/IPaymentProvider.cs`:

```csharp
namespace Live.Wallet.Providers;

/// <summary>
/// The payment provider seen by the wallet. Transient faults (network, 5xx) surface as exceptions;
/// a definite refusal is a Rejected PayoutInitiation. Payouts are idempotent per ReferenceId.
/// </summary>
public interface IPaymentProvider
{
    Task<ChargeInitiation> InitiateChargeAsync(string referenceId, long amount, CancellationToken cancellationToken);

    Task<PayoutInitiation> InitiatePayoutAsync(PayoutInstruction instruction, CancellationToken cancellationToken);

    Task<PayoutStatus> GetPayoutStatusAsync(string referenceId, CancellationToken cancellationToken);
}
```

`src/Live.Wallet/Providers/ChargeInitiation.cs`:

```csharp
namespace Live.Wallet.Providers;

public sealed record ChargeInitiation
{
    public required string ProviderReferenceId { get; init; }
}
```

`src/Live.Wallet/Providers/PayoutInstruction.cs`:

```csharp
namespace Live.Wallet.Providers;

public sealed record PayoutInstruction
{
    /// <summary>Our ReferenceId; the provider's idempotency key for this payout.</summary>
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
```

`src/Live.Wallet/Providers/PayoutOutcome.cs`:

```csharp
namespace Live.Wallet.Providers;

public enum PayoutOutcome
{
    Accepted,
    Rejected,
}
```

`src/Live.Wallet/Providers/PayoutStatus.cs`:

```csharp
namespace Live.Wallet.Providers;

public enum PayoutStatus
{
    Pending,
    Succeeded,
    Failed,
    NotFound,
}
```

`src/Live.Wallet/Providers/PayoutInitiation.cs`:

```csharp
namespace Live.Wallet.Providers;

public sealed record PayoutInitiation
{
    public required PayoutOutcome Outcome { get; init; }

    public string? ProviderReferenceId { get; init; }

    public string? FailureReason { get; init; }

    public static PayoutInitiation Accepted(string providerReferenceId) =>
        new() { Outcome = PayoutOutcome.Accepted, ProviderReferenceId = providerReferenceId };

    public static PayoutInitiation Rejected(string reason) =>
        new() { Outcome = PayoutOutcome.Rejected, FailureReason = reason };
}
```

`src/Live.Wallet/Providers/FakeProviderRecord.cs`:

```csharp
namespace Live.Wallet.Providers;

/// <summary>What the fake provider remembers about a charge or payout.</summary>
public sealed class FakeProviderRecord
{
    public required bool IsPayout { get; init; }

    public required string ReferenceId { get; init; }

    public required string ProviderReferenceId { get; init; }

    public required long Amount { get; init; }

    public PayoutStatus Status { get; set; } = PayoutStatus.Pending;
}
```

`src/Live.Wallet/Providers/FakePaymentProvider.cs`:

```csharp
using System.Collections.Concurrent;

namespace Live.Wallet.Providers;

/// <summary>In-memory stand-in for a real provider. Tests script payout behaviour; the dev endpoint reads it to fake webhooks.</summary>
public sealed class FakePaymentProvider : IPaymentProvider
{
    private readonly ConcurrentDictionary<string, FakeProviderRecord> _charges = new();
    private readonly ConcurrentDictionary<string, FakeProviderRecord> _payouts = new();
    private int _payoutAttempts;

    /// <summary>Decides new payouts. Null accepts them; throwing simulates a transient provider fault.</summary>
    public Func<PayoutInstruction, PayoutInitiation>? PayoutBehaviour { get; set; }

    public int PayoutAttempts => _payoutAttempts;

    public Task<ChargeInitiation> InitiateChargeAsync(string referenceId, long amount, CancellationToken cancellationToken)
    {
        var record = _charges.GetOrAdd(referenceId, reference => new FakeProviderRecord
        {
            IsPayout = false,
            ReferenceId = reference,
            ProviderReferenceId = $"chg_{Guid.NewGuid():N}",
            Amount = amount,
        });
        return Task.FromResult(new ChargeInitiation { ProviderReferenceId = record.ProviderReferenceId });
    }

    public Task<PayoutInitiation> InitiatePayoutAsync(PayoutInstruction instruction, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref _payoutAttempts);
        if (_payouts.TryGetValue(instruction.ReferenceId, out var existing))
        {
            return Task.FromResult(existing.Status == PayoutStatus.Failed
                ? PayoutInitiation.Rejected("The payout already failed.")
                : PayoutInitiation.Accepted(existing.ProviderReferenceId));
        }

        var initiation = PayoutBehaviour?.Invoke(instruction) ?? PayoutInitiation.Accepted($"trf_{Guid.NewGuid():N}");
        if (initiation is { Outcome: PayoutOutcome.Accepted, ProviderReferenceId: { } providerReferenceId })
        {
            _payouts[instruction.ReferenceId] = new FakeProviderRecord
            {
                IsPayout = true,
                ReferenceId = instruction.ReferenceId,
                ProviderReferenceId = providerReferenceId,
                Amount = instruction.Amount,
            };
        }

        return Task.FromResult(initiation);
    }

    public Task<PayoutStatus> GetPayoutStatusAsync(string referenceId, CancellationToken cancellationToken) =>
        Task.FromResult(_payouts.TryGetValue(referenceId, out var record) ? record.Status : PayoutStatus.NotFound);

    public void SetPayoutStatus(string referenceId, PayoutStatus status)
    {
        if (_payouts.TryGetValue(referenceId, out var record))
        {
            record.Status = status;
        }
    }

    public FakeProviderRecord? FindByProviderReference(string providerReferenceId) =>
        _charges.Values.Concat(_payouts.Values).FirstOrDefault(r => r.ProviderReferenceId == providerReferenceId);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~FakePaymentProviderTests"`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Providers tests/Live.Tests/Wallet/Providers
git commit -m "feat(wallet): add payment provider abstraction and in-memory fake" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 7: Top-ups (initiate + complete)

**Files:**
- Create: `src/Live.Wallet/TopUps/InitiateTopUpCommand.cs`, `InitiateTopUpHandler.cs`, `CompleteTopUpHandler.cs`
- Test: `tests/Live.Tests/Wallet/TopUps/TopUpHandlersTests.cs`

**Interfaces:**
- Consumes: the Task 5 helpers (`WalletAccounts`, `Idempotency`, `TransactionResponse`, `IdempotentResponse`), `IPaymentProvider`, `ConcurrencyRetry`, `LedgerPosting`
- Produces:
  - `InitiateTopUpCommand { UserId, ReferenceId, Amount }`
  - `InitiateTopUpHandler(WalletDbContext, IPaymentProvider, IClock).HandleAsync(InitiateTopUpCommand, ct) → Result<IdempotentResponse>`
  - `CompleteTopUpHandler(WalletDbContext, IClock, ILogger<CompleteTopUpHandler>)`:
    - `SucceedAsync(string providerReferenceId, long amount, ct) → Result<TransactionResponse>`
    - `FailAsync(string providerReferenceId, ct) → Result<TransactionResponse>`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/TopUps/TopUpHandlersTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.TopUps;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Practice.Common;

namespace Live.Tests.Wallet.TopUps;

public sealed class TopUpHandlersTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly FakePaymentProvider _provider = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Initiate_opens_the_wallet_lazily_and_records_a_pending_top_up()
    {
        await using var db = _testDb.CreateContext();

        var result = await Initiate(db).HandleAsync(Command(5_000), CancellationToken.None);

        result.Value.Replayed.Should().BeFalse();
        var transaction = result.Value.Transaction;
        transaction.Type.Should().Be("TopUp");
        transaction.Status.Should().Be("Pending");
        transaction.Amount.Should().Be(5_000);
        transaction.ProviderReferenceId.Should().StartWith("chg_");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(0);
    }

    [Theory]
    [InlineData(0)]
    [InlineData(-5)]
    public async Task Initiate_rejects_non_positive_amounts(long amount)
    {
        await using var db = _testDb.CreateContext();

        var result = await Initiate(db).HandleAsync(Command(amount), CancellationToken.None);

        result.Error.Code.Should().Be("Money.InvalidAmount");
        (await _testDb.QueryAsync(q => q.Transactions.CountAsync())).Should().Be(0);
    }

    [Fact]
    public async Task Initiate_rejects_a_blank_reference()
    {
        await using var db = _testDb.CreateContext();

        var result = await Initiate(db).HandleAsync(Command(5_000, referenceId: " "), CancellationToken.None);

        result.Error.Code.Should().Be("Validation.ReferenceId");
    }

    [Fact]
    public async Task Initiate_replays_an_identical_request()
    {
        await using var db = _testDb.CreateContext();
        var first = await Initiate(db).HandleAsync(Command(5_000), CancellationToken.None);

        var second = await Initiate(db).HandleAsync(Command(5_000), CancellationToken.None);

        second.Value.Replayed.Should().BeTrue();
        second.Value.Transaction.Id.Should().Be(first.Value.Transaction.Id);
        (await _testDb.QueryAsync(q => q.Transactions.CountAsync())).Should().Be(1);
    }

    [Fact]
    public async Task Initiate_rejects_a_reused_reference_with_a_different_amount()
    {
        await using var db = _testDb.CreateContext();
        await Initiate(db).HandleAsync(Command(5_000), CancellationToken.None);

        var result = await Initiate(db).HandleAsync(Command(6_000), CancellationToken.None);

        result.Error.Code.Should().Be("Transaction.DuplicateReference");
    }

    [Fact]
    public async Task Succeed_credits_the_wallet_with_a_balanced_posting()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        var result = await Complete(db).SucceedAsync(providerReferenceId, 5_000, CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.Balance.Should().Be(5_000);
        wallet.AvailableBalance.Should().Be(5_000);
        (await _testDb.GetSettlementAsync()).Balance.Should().Be(-5_000);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == result.Value.Id))).Should().Be(2);
    }

    [Fact]
    public async Task Succeed_is_idempotent_for_duplicate_webhooks()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        await Complete(db).SucceedAsync(providerReferenceId, 5_000, CancellationToken.None);
        var again = await Complete(db).SucceedAsync(providerReferenceId, 5_000, CancellationToken.None);

        again.Value.Status.Should().Be("Succeeded");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(5_000);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == again.Value.Id))).Should().Be(2);
    }

    [Fact]
    public async Task Succeed_with_a_mismatched_amount_fails_the_top_up_without_crediting()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        var result = await Complete(db).SucceedAsync(providerReferenceId, 9_000, CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(0);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == result.Value.Id))).Should().Be(0);
    }

    [Fact]
    public async Task Succeed_for_an_unknown_reference_returns_not_found()
    {
        await using var db = _testDb.CreateContext();

        var result = await Complete(db).SucceedAsync("chg_missing", 5_000, CancellationToken.None);

        result.Error.Code.Should().Be("Transaction.NotFound");
    }

    [Fact]
    public async Task Fail_marks_the_top_up_failed_without_entries()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        var result = await Complete(db).FailAsync(providerReferenceId, CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(0);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == result.Value.Id))).Should().Be(0);
    }

    private InitiateTopUpHandler Initiate(WalletDbContext db) => new(db, _provider, _clock);

    private CompleteTopUpHandler Complete(WalletDbContext db) => new(db, _clock, NullLogger<CompleteTopUpHandler>.Instance);

    private InitiateTopUpCommand Command(long amount, string referenceId = "topup-1") =>
        new() { UserId = _userId, Amount = amount, ReferenceId = referenceId };

    private async Task<string> StartTopUpAsync(long amount)
    {
        await using var db = _testDb.CreateContext();
        var started = await Initiate(db).HandleAsync(Command(amount), CancellationToken.None);
        return started.Value.Transaction.ProviderReferenceId
            ?? throw new InvalidOperationException("A started top-up always carries a provider reference.");
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~TopUpHandlersTests"`
Expected: build FAILS with `CS0246` for `InitiateTopUpHandler`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/TopUps/InitiateTopUpCommand.cs`:

```csharp
namespace Live.Wallet.TopUps;

public sealed record InitiateTopUpCommand
{
    public required Guid UserId { get; init; }

    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }
}
```

`src/Live.Wallet/TopUps/InitiateTopUpHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Wallets;
using Practice.Common;

namespace Live.Wallet.TopUps;

/// <summary>Starts a charge with the provider and records a Pending top-up. Money moves only when the provider's webhook confirms it.</summary>
public sealed class InitiateTopUpHandler
{
    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly IClock _clock;

    public InitiateTopUpHandler(WalletDbContext db, IPaymentProvider provider, IClock clock)
    {
        _db = db;
        _provider = provider;
        _clock = clock;
    }

    public async Task<Result<IdempotentResponse>> HandleAsync(InitiateTopUpCommand command, CancellationToken cancellationToken)
    {
        if (command.Amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (string.IsNullOrWhiteSpace(command.ReferenceId))
        {
            return WalletErrors.Validation("ReferenceId", "ReferenceId is required.");
        }

        var requestHash = RequestHash.Compute(nameof(TransactionType.TopUp), command.UserId, command.Amount);
        var replay = await Idempotency.FindReplayAsync(_db, command.ReferenceId, requestHash, cancellationToken).ConfigureAwait(false);
        if (replay is not null)
        {
            return replay;
        }

        var now = _clock.UtcNow;
        var (wallet, _) = await WalletAccounts.GetOrCreateAsync(_db, command.UserId, now, cancellationToken).ConfigureAwait(false);
        var charge = await _provider.InitiateChargeAsync(command.ReferenceId, command.Amount, cancellationToken).ConfigureAwait(false);
        var transaction = LedgerTransaction.StartTopUp(wallet.Id, command.Amount, command.ReferenceId, requestHash, charge.ProviderReferenceId, now);
        _db.Transactions.Add(transaction);
        await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        return new IdempotentResponse { Transaction = TransactionResponse.From(transaction), Replayed = false };
    }
}
```

`src/Live.Wallet/TopUps/CompleteTopUpHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Practice.Common;

namespace Live.Wallet.TopUps;

/// <summary>Applies the provider's verdict on a charge. Repeat deliveries for a final top-up change nothing.</summary>
public sealed partial class CompleteTopUpHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;
    private readonly ILogger<CompleteTopUpHandler> _logger;

    public CompleteTopUpHandler(WalletDbContext db, IClock clock, ILogger<CompleteTopUpHandler> logger)
    {
        _db = db;
        _clock = clock;
        _logger = logger;
    }

    public Task<Result<TransactionResponse>> SucceedAsync(string providerReferenceId, long amount, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await FindAsync(providerReferenceId, ct).ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (transaction.IsFinal)
            {
                return TransactionResponse.From(transaction);
            }

            var now = _clock.UtcNow;
            if (transaction.Amount != amount)
            {
                LogAmountMismatch(_logger, transaction.Id, transaction.Amount, amount);
                transaction.MarkFailed(now);
                return TransactionResponse.From(transaction);
            }

            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var settlement = await _db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId, ct).ConfigureAwait(false);
            var posting = LedgerPosting.Post(transaction, settlement, wallet, debitFromHold: false, now);
            if (posting.IsFailure)
            {
                return posting.Error;
            }

            _db.LedgerEntries.AddRange(posting.Value);
            transaction.MarkSucceeded(now);
            return TransactionResponse.From(transaction);
        }, cancellationToken);

    public Task<Result<TransactionResponse>> FailAsync(string providerReferenceId, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await FindAsync(providerReferenceId, ct).ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (!transaction.IsFinal)
            {
                transaction.MarkFailed(_clock.UtcNow);
            }

            return TransactionResponse.From(transaction);
        }, cancellationToken);

    private Task<LedgerTransaction?> FindAsync(string providerReferenceId, CancellationToken cancellationToken) =>
        _db.Transactions.SingleOrDefaultAsync(
            t => t.ProviderReferenceId == providerReferenceId && t.Type == TransactionType.TopUp, cancellationToken);

    [LoggerMessage(Level = LogLevel.Error, Message = "Top-up {TransactionId} expected {ExpectedAmount} kobo but the provider reported {ReportedAmount}; marking it failed")]
    private static partial void LogAmountMismatch(ILogger logger, Guid transactionId, long expectedAmount, long reportedAmount);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~TopUpHandlersTests"`
Expected: PASS (11 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/TopUps tests/Live.Tests/Wallet/TopUps
git commit -m "feat(wallet): add idempotent top-up initiation and webhook completion" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 8: Withdrawal request (hold + outbox in one save)

**Files:**
- Create: `src/Live.Wallet/Payouts/PayoutRequested.cs`, `src/Live.Wallet/Outbox/WalletJsonContext.cs`
- Create: `src/Live.Wallet/Withdrawals/RequestWithdrawalCommand.cs`, `RequestWithdrawalHandler.cs`
- Create (tests): `tests/Live.Tests/Wallet/TestCommands.cs`
- Test: `tests/Live.Tests/Wallet/Withdrawals/RequestWithdrawalHandlerTests.cs`

**Interfaces:**
- Consumes: `WalletAccounts`, `Idempotency`, `ConcurrencyRetry`, `OutboxMessage`, `WithdrawalDetails`
- Produces:
  - `PayoutRequested { MessageType = "wallet.payout-requested", TransactionId }`
  - `WalletJsonContext.Default.PayoutRequested` (internal)
  - `RequestWithdrawalCommand { UserId, ReferenceId, Amount, BankCode, AccountNumber, AccountName }`
  - `RequestWithdrawalHandler(WalletDbContext, IClock).HandleAsync(RequestWithdrawalCommand, ct) → Result<IdempotentResponse>`
  - Test helper: `TestCommands.Withdrawal(Guid userId, long amount, string referenceId = "wd-1")`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/TestCommands.cs`:

```csharp
using Live.Wallet.Withdrawals;

namespace Live.Tests.Wallet;

internal static class TestCommands
{
    public static RequestWithdrawalCommand Withdrawal(Guid userId, long amount, string referenceId = "wd-1") =>
        new()
        {
            UserId = userId,
            Amount = amount,
            ReferenceId = referenceId,
            BankCode = "058",
            AccountNumber = "0123456789",
            AccountName = "Ada Lovelace",
        };
}
```

`tests/Live.Tests/Wallet/Withdrawals/RequestWithdrawalHandlerTests.cs`:

```csharp
using System.Text.Json;
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Tests.Wallet.Withdrawals;

public sealed class RequestWithdrawalHandlerTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Places_a_hold_and_records_the_withdrawal_details_and_payout_event_together()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);

        var transaction = result.Value.Transaction;
        result.Value.Replayed.Should().BeFalse();
        transaction.Type.Should().Be("Withdrawal");
        transaction.Status.Should().Be("Pending");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.AvailableBalance.Should().Be(6_000);
        wallet.Balance.Should().Be(10_000);
        var details = await _testDb.QueryAsync(q => q.Withdrawals.SingleAsync(w => w.TransactionId == transaction.Id));
        details.AccountNumber.Should().Be("0123456789");
        details.BankCode.Should().Be("058");
        var message = await _testDb.QueryAsync(q => q.OutboxMessages.SingleAsync());
        message.Type.Should().Be(PayoutRequested.MessageType);
        message.ProcessedAt.Should().BeNull();
        JsonSerializer.Deserialize(message.Payload, WalletJsonContext.Default.PayoutRequested)?.TransactionId.Should().Be(transaction.Id);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == transaction.Id))).Should().Be(0);
    }

    [Fact]
    public async Task Withdrawing_the_entire_available_balance_succeeds()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var all = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 10_000, "wd-all"), CancellationToken.None);
        var oneMore = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 1, "wd-one-more"), CancellationToken.None);

        all.IsSuccess.Should().BeTrue();
        oneMore.Error.Code.Should().Be("Wallet.InsufficientFunds");
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(0);
    }

    [Fact]
    public async Task Insufficient_funds_changes_nothing()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 20_000), CancellationToken.None);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
        (await _testDb.QueryAsync(q => q.Transactions.CountAsync(t => t.Type == TransactionType.Withdrawal))).Should().Be(0);
        (await _testDb.QueryAsync(q => q.OutboxMessages.CountAsync())).Should().Be(0);
    }

    [Fact]
    public async Task A_user_without_a_wallet_gets_not_found()
    {
        await using var db = _testDb.CreateContext();

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 100), CancellationToken.None);

        result.Error.Code.Should().Be("Wallet.NotFound");
    }

    [Theory]
    [InlineData(0, "0123456789", "058", "Money.InvalidAmount")]
    [InlineData(100, "12345", "058", "Validation.AccountNumber")]
    [InlineData(100, "01234567AB", "058", "Validation.AccountNumber")]
    [InlineData(100, "0123456789", "", "Validation.BankCode")]
    public async Task Invalid_requests_are_rejected(long amount, string accountNumber, string bankCode, string expectedCode)
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();
        var command = TestCommands.Withdrawal(_userId, amount) with { AccountNumber = accountNumber, BankCode = bankCode };

        var result = await Handler(db).HandleAsync(command, CancellationToken.None);

        result.Error.Code.Should().Be(expectedCode);
    }

    [Fact]
    public async Task An_identical_retry_returns_the_original_without_a_second_hold()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();
        var first = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);

        var retry = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);

        retry.Value.Replayed.Should().BeTrue();
        retry.Value.Transaction.Id.Should().Be(first.Value.Transaction.Id);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
        (await _testDb.QueryAsync(q => q.OutboxMessages.CountAsync())).Should().Be(1);
    }

    [Fact]
    public async Task A_reused_reference_with_a_different_amount_is_rejected()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();
        await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 5_000), CancellationToken.None);

        result.Error.Code.Should().Be("Transaction.DuplicateReference");
    }

    [Fact]
    public async Task Concurrent_withdrawals_cannot_spend_the_same_money()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        var competitor = new ConcurrentWriteInterceptor(times: 1, async () =>
        {
            await using var other = _testDb.CreateContext();
            var competing = await Handler(other).HandleAsync(TestCommands.Withdrawal(_userId, 5_000, "wd-competing"), CancellationToken.None);
            competing.IsSuccess.Should().BeTrue();
        });
        await using var db = _testDb.CreateContext(competitor);

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 8_000, "wd-mine"), CancellationToken.None);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.AvailableBalance.Should().Be(5_000);
        wallet.Balance.Should().Be(10_000);
        (await _testDb.QueryAsync(q => q.Transactions.CountAsync(t => t.Type == TransactionType.Withdrawal))).Should().Be(1);
        (await _testDb.QueryAsync(q => q.OutboxMessages.CountAsync())).Should().Be(1);
    }

    [Fact]
    public async Task Concurrent_identical_requests_create_one_withdrawal()
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        var twin = new ConcurrentWriteInterceptor(times: 1, async () =>
        {
            await using var other = _testDb.CreateContext();
            await Handler(other).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);
        });
        await using var db = _testDb.CreateContext(twin);

        var result = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);

        result.Value.Replayed.Should().BeTrue();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
        (await _testDb.QueryAsync(q => q.Transactions.CountAsync(t => t.Type == TransactionType.Withdrawal))).Should().Be(1);
    }

    private RequestWithdrawalHandler Handler(WalletDbContext db) => new(db, _clock);
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~RequestWithdrawalHandlerTests"`
Expected: build FAILS with `CS0246` for `RequestWithdrawalCommand`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Payouts/PayoutRequested.cs`:

```csharp
namespace Live.Wallet.Payouts;

/// <summary>Outbox event asking the payout worker to send a held withdrawal to the provider.</summary>
public sealed record PayoutRequested
{
    public const string MessageType = "wallet.payout-requested";

    public required Guid TransactionId { get; init; }
}
```

`src/Live.Wallet/Outbox/WalletJsonContext.cs`:

```csharp
using System.Text.Json.Serialization;
using Live.Wallet.Payouts;

namespace Live.Wallet.Outbox;

[JsonSerializable(typeof(PayoutRequested))]
internal sealed partial class WalletJsonContext : JsonSerializerContext
{
}
```

`src/Live.Wallet/Withdrawals/RequestWithdrawalCommand.cs`:

```csharp
namespace Live.Wallet.Withdrawals;

public sealed record RequestWithdrawalCommand
{
    public required Guid UserId { get; init; }

    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
```

`src/Live.Wallet/Withdrawals/RequestWithdrawalHandler.cs`:

```csharp
using System.Text.Json;
using Live.Wallet.Ledger;
using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Practice.Common;

namespace Live.Wallet.Withdrawals;

/// <summary>
/// Holds the funds and records the withdrawal, its bank details and a PayoutRequested outbox event in one SaveChanges,
/// so the payout is queued exactly when the hold commits.
/// </summary>
public sealed class RequestWithdrawalHandler
{
    private const int NubanLength = 10;

    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public RequestWithdrawalHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public async Task<Result<IdempotentResponse>> HandleAsync(RequestWithdrawalCommand command, CancellationToken cancellationToken)
    {
        if (Validate(command) is { } invalid)
        {
            return invalid;
        }

        var requestHash = RequestHash.Compute(
            nameof(TransactionType.Withdrawal), command.UserId, command.Amount, command.BankCode, command.AccountNumber, command.AccountName);

        return await ConcurrencyRetry.ExecuteAsync<IdempotentResponse>(_db, async ct =>
        {
            // Checked inside the retry: a racing identical request may have committed since the last attempt.
            var replay = await Idempotency.FindReplayAsync(_db, command.ReferenceId, requestHash, ct).ConfigureAwait(false);
            if (replay is not null)
            {
                return replay;
            }

            var wallet = await WalletAccounts.FindAsync(_db, command.UserId, ct).ConfigureAwait(false);
            if (wallet is null)
            {
                return WalletErrors.WalletNotFound;
            }

            var hold = wallet.PlaceHold(command.Amount);
            if (hold.IsFailure)
            {
                return hold.Error;
            }

            var now = _clock.UtcNow;
            var transaction = LedgerTransaction.StartWithdrawal(wallet.Id, command.Amount, command.ReferenceId, requestHash, now);
            _db.Transactions.Add(transaction);
            _db.Withdrawals.Add(WithdrawalDetails.Create(transaction.Id, command.BankCode, command.AccountNumber, command.AccountName));
            var payload = JsonSerializer.Serialize(new PayoutRequested { TransactionId = transaction.Id }, WalletJsonContext.Default.PayoutRequested);
            _db.OutboxMessages.Add(OutboxMessage.Create(PayoutRequested.MessageType, payload, now));
            return new IdempotentResponse { Transaction = TransactionResponse.From(transaction), Replayed = false };
        }, cancellationToken).ConfigureAwait(false);
    }

    private static Error? Validate(RequestWithdrawalCommand command)
    {
        if (command.Amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (string.IsNullOrWhiteSpace(command.ReferenceId))
        {
            return WalletErrors.Validation("ReferenceId", "ReferenceId is required.");
        }

        if (string.IsNullOrWhiteSpace(command.BankCode))
        {
            return WalletErrors.Validation("BankCode", "BankCode is required.");
        }

        if (command.AccountNumber.Length != NubanLength || !command.AccountNumber.All(char.IsAsciiDigit))
        {
            return WalletErrors.Validation("AccountNumber", "AccountNumber must be a 10-digit NUBAN.");
        }

        if (string.IsNullOrWhiteSpace(command.AccountName))
        {
            return WalletErrors.Validation("AccountName", "AccountName is required.");
        }

        return null;
    }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~RequestWithdrawalHandlerTests"`
Expected: PASS (12 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet tests/Live.Tests/Wallet
git commit -m "feat(wallet): add withdrawal request with hold and transactional outbox" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 9: Settle and release withdrawals

**Files:**
- Create: `src/Live.Wallet/Withdrawals/SettleWithdrawalHandler.cs`, `ReleaseWithdrawalHandler.cs`
- Create (tests): `tests/Live.Tests/Wallet/WalletScenarios.cs`
- Test: `tests/Live.Tests/Wallet/Withdrawals/SettleAndReleaseTests.cs`

**Interfaces:**
- Consumes: `ConcurrencyRetry`, `LedgerPosting`, `Account.ReleaseHold`
- Produces:
  - `SettleWithdrawalHandler(WalletDbContext, IClock).HandleAsync(Guid transactionId, ct) → Result<TransactionResponse>`
  - `ReleaseWithdrawalHandler(WalletDbContext, IClock, ILogger<ReleaseWithdrawalHandler>).HandleAsync(Guid transactionId, string reason, ct) → Result<TransactionResponse>`
  - Both are no-ops on a final transaction.
  - Test helper: `WalletScenarios.PendingWithdrawalAsync(TestDb, FakeClock, Guid userId, long balance, long amount, string referenceId = "wd-1") → Task<Guid>`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/WalletScenarios.cs`:

```csharp
using Live.Wallet.Withdrawals;
using Practice.Common;

namespace Live.Tests.Wallet;

internal static class WalletScenarios
{
    /// <summary>A funded wallet with one held, Pending withdrawal. Returns the withdrawal's transaction id.</summary>
    public static async Task<Guid> PendingWithdrawalAsync(
        TestDb testDb, FakeClock clock, Guid userId, long balance, long amount, string referenceId = "wd-1")
    {
        await testDb.SeedWalletAsync(userId, balance, clock.UtcNow);
        await using var db = testDb.CreateContext();
        var result = await new RequestWithdrawalHandler(db, clock)
            .HandleAsync(TestCommands.Withdrawal(userId, amount, referenceId), CancellationToken.None);
        return result.Value.Transaction.Id;
    }
}
```

`tests/Live.Tests/Wallet/Withdrawals/SettleAndReleaseTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Practice.Common;

namespace Live.Tests.Wallet.Withdrawals;

public sealed class SettleAndReleaseTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Settle_debits_the_wallet_from_the_hold_and_credits_settlement()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();

        var result = await Settle(db).HandleAsync(transactionId, CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.Balance.Should().Be(6_000);
        wallet.AvailableBalance.Should().Be(6_000);
        (await _testDb.GetSettlementAsync()).Balance.Should().Be(-6_000);
        var entries = await _testDb.QueryAsync(q => q.LedgerEntries.Where(e => e.TransactionId == transactionId).ToListAsync());
        entries.Should().HaveCount(2);
        entries.Should().ContainSingle(e => e.Direction == EntryDirection.Debit && e.AccountId == wallet.Id && e.BalanceAfter == 6_000);
        (await _testDb.QueryAsync(q => q.Accounts.SumAsync(a => a.Balance))).Should().Be(0);
    }

    [Fact]
    public async Task Settling_twice_posts_once()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();

        await Settle(db).HandleAsync(transactionId, CancellationToken.None);
        await Settle(db).HandleAsync(transactionId, CancellationToken.None);

        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(6_000);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == transactionId))).Should().Be(2);
    }

    [Fact]
    public async Task Release_restores_the_available_balance_without_entries()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();

        var result = await Release(db).HandleAsync(transactionId, "test", CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.AvailableBalance.Should().Be(10_000);
        wallet.Balance.Should().Be(10_000);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == transactionId))).Should().Be(0);
    }

    [Fact]
    public async Task Release_after_settle_changes_nothing()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        await Settle(db).HandleAsync(transactionId, CancellationToken.None);

        var result = await Release(db).HandleAsync(transactionId, "late failure", CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.Balance.Should().Be(6_000);
        wallet.AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task Settle_after_release_changes_nothing()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        await Release(db).HandleAsync(transactionId, "test", CancellationToken.None);

        var result = await Settle(db).HandleAsync(transactionId, CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(10_000);
    }

    [Fact]
    public async Task Settling_an_unknown_withdrawal_returns_not_found()
    {
        await using var db = _testDb.CreateContext();

        (await Settle(db).HandleAsync(Guid.NewGuid(), CancellationToken.None)).Error.Code.Should().Be("Transaction.NotFound");
        (await Release(db).HandleAsync(Guid.NewGuid(), "test", CancellationToken.None)).Error.Code.Should().Be("Transaction.NotFound");
    }

    [Fact]
    public async Task Settle_and_release_racing_apply_exactly_one()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        var releaseFirst = new ConcurrentWriteInterceptor(times: 1, async () =>
        {
            await using var other = _testDb.CreateContext();
            await Release(other).HandleAsync(transactionId, "webhook said failed", CancellationToken.None);
        });
        await using var db = _testDb.CreateContext(releaseFirst);

        var result = await Settle(db).HandleAsync(transactionId, CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        var wallet = await _testDb.GetWalletAsync(_userId);
        wallet.Balance.Should().Be(10_000);
        wallet.AvailableBalance.Should().Be(10_000);
        (await _testDb.QueryAsync(q => q.LedgerEntries.CountAsync(e => e.TransactionId == transactionId))).Should().Be(0);
    }

    private SettleWithdrawalHandler Settle(WalletDbContext db) => new(db, _clock);

    private ReleaseWithdrawalHandler Release(WalletDbContext db) => new(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance);
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~SettleAndReleaseTests"`
Expected: build FAILS with `CS0246` for `SettleWithdrawalHandler`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Withdrawals/SettleWithdrawalHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Withdrawals;

/// <summary>The provider paid out: post the held amount from the wallet to settlement. Called by the webhook and the sweeper.</summary>
public sealed class SettleWithdrawalHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public SettleWithdrawalHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public Task<Result<TransactionResponse>> HandleAsync(Guid transactionId, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await _db.Transactions
                .SingleOrDefaultAsync(t => t.Id == transactionId && t.Type == TransactionType.Withdrawal, ct)
                .ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (transaction.IsFinal)
            {
                return TransactionResponse.From(transaction);
            }

            var now = _clock.UtcNow;
            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var settlement = await _db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId, ct).ConfigureAwait(false);
            var posting = LedgerPosting.Post(transaction, wallet, settlement, debitFromHold: true, now);
            if (posting.IsFailure)
            {
                return posting.Error;
            }

            _db.LedgerEntries.AddRange(posting.Value);
            transaction.MarkSucceeded(now);
            return TransactionResponse.From(transaction);
        }, cancellationToken);
}
```

`src/Live.Wallet/Withdrawals/ReleaseWithdrawalHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Practice.Common;

namespace Live.Wallet.Withdrawals;

/// <summary>The payout definitely failed: give the held amount back to AvailableBalance. No ledger entries, because no money moved.</summary>
public sealed partial class ReleaseWithdrawalHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;
    private readonly ILogger<ReleaseWithdrawalHandler> _logger;

    public ReleaseWithdrawalHandler(WalletDbContext db, IClock clock, ILogger<ReleaseWithdrawalHandler> logger)
    {
        _db = db;
        _clock = clock;
        _logger = logger;
    }

    public Task<Result<TransactionResponse>> HandleAsync(Guid transactionId, string reason, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await _db.Transactions
                .SingleOrDefaultAsync(t => t.Id == transactionId && t.Type == TransactionType.Withdrawal, ct)
                .ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (transaction.IsFinal)
            {
                return TransactionResponse.From(transaction);
            }

            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var released = wallet.ReleaseHold(transaction.Amount);
            if (released.IsFailure)
            {
                return released.Error;
            }

            transaction.MarkFailed(_clock.UtcNow);
            LogReleased(_logger, transaction.Id, transaction.Amount, reason);
            return TransactionResponse.From(transaction);
        }, cancellationToken);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Released withdrawal {TransactionId} hold of {Amount} kobo: {Reason}")]
    private static partial void LogReleased(ILogger logger, Guid transactionId, long amount, string reason);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~SettleAndReleaseTests"`
Expected: PASS (7 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Withdrawals tests/Live.Tests/Wallet
git commit -m "feat(wallet): add idempotent withdrawal settle and release" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 10: Provider webhook handler

**Files:**
- Create: `src/Live.Wallet/Webhooks/ProviderWebhook.cs`, `ProviderWebhookEvents.cs`, `ProviderWebhookHandler.cs`
- Test: `tests/Live.Tests/Wallet/Webhooks/ProviderWebhookHandlerTests.cs`

**Interfaces:**
- Consumes: `CompleteTopUpHandler`, `SettleWithdrawalHandler`, `ReleaseWithdrawalHandler`
- Produces:
  - `ProviderWebhook { Event, ProviderReferenceId, Reference?, Amount }`
  - `ProviderWebhookEvents.ChargeSuccess/ChargeFailed/TransferSuccess/TransferFailed`
  - `ProviderWebhookHandler(WalletDbContext, CompleteTopUpHandler, SettleWithdrawalHandler, ReleaseWithdrawalHandler).HandleAsync(ProviderWebhook, ct) → Result<TransactionResponse>`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Webhooks/ProviderWebhookHandlerTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.TopUps;
using Live.Wallet.Webhooks;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Practice.Common;

namespace Live.Tests.Wallet.Webhooks;

public sealed class ProviderWebhookHandlerTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly FakePaymentProvider _provider = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Charge_success_credits_the_wallet()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.ChargeSuccess, providerReferenceId, 5_000), CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(5_000);
    }

    [Fact]
    public async Task A_duplicate_charge_success_credits_once()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();
        var webhook = Webhook(ProviderWebhookEvents.ChargeSuccess, providerReferenceId, 5_000);

        await Webhooks(db).HandleAsync(webhook, CancellationToken.None);
        await Webhooks(db).HandleAsync(webhook, CancellationToken.None);

        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(5_000);
    }

    [Fact]
    public async Task Charge_failed_fails_the_top_up()
    {
        var providerReferenceId = await StartTopUpAsync(5_000);
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.ChargeFailed, providerReferenceId, 5_000), CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(0);
    }

    [Fact]
    public async Task Transfer_success_settles_the_withdrawal_found_by_provider_reference()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await AssignProviderReferenceAsync(transactionId, "trf_abc");
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.TransferSuccess, "trf_abc", 4_000), CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(6_000);
    }

    [Fact]
    public async Task Transfer_success_before_the_provider_reference_is_stored_matches_our_reference()
    {
        await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000, referenceId: "wd-early");
        await using var db = _testDb.CreateContext();
        var webhook = Webhook(ProviderWebhookEvents.TransferSuccess, "trf_not_yet_stored", 4_000) with { Reference = "wd-early" };

        var result = await Webhooks(db).HandleAsync(webhook, CancellationToken.None);

        result.Value.Status.Should().Be("Succeeded");
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(6_000);
    }

    [Fact]
    public async Task Transfer_failed_releases_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await AssignProviderReferenceAsync(transactionId, "trf_abc");
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.TransferFailed, "trf_abc", 4_000), CancellationToken.None);

        result.Value.Status.Should().Be("Failed");
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
    }

    [Fact]
    public async Task An_unknown_event_is_rejected()
    {
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook("refund.created", "x", 1), CancellationToken.None);

        result.Error.Code.Should().Be("Validation.Event");
    }

    [Fact]
    public async Task An_unknown_transfer_reference_returns_not_found()
    {
        await using var db = _testDb.CreateContext();

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.TransferSuccess, "trf_missing", 1), CancellationToken.None);

        result.Error.Code.Should().Be("Transaction.NotFound");
    }

    private ProviderWebhookHandler Webhooks(WalletDbContext db) =>
        new(
            db,
            new CompleteTopUpHandler(db, _clock, NullLogger<CompleteTopUpHandler>.Instance),
            new SettleWithdrawalHandler(db, _clock),
            new ReleaseWithdrawalHandler(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance));

    private static ProviderWebhook Webhook(string eventName, string providerReferenceId, long amount) =>
        new() { Event = eventName, ProviderReferenceId = providerReferenceId, Amount = amount };

    private async Task<string> StartTopUpAsync(long amount)
    {
        await using var db = _testDb.CreateContext();
        var started = await new InitiateTopUpHandler(db, _provider, _clock).HandleAsync(
            new InitiateTopUpCommand { UserId = _userId, ReferenceId = "topup-1", Amount = amount }, CancellationToken.None);
        return started.Value.Transaction.ProviderReferenceId
            ?? throw new InvalidOperationException("A started top-up always carries a provider reference.");
    }

    private async Task AssignProviderReferenceAsync(Guid transactionId, string providerReferenceId)
    {
        await using var db = _testDb.CreateContext();
        var transaction = await db.Transactions.SingleAsync(t => t.Id == transactionId);
        transaction.AssignProviderReference(providerReferenceId);
        await db.SaveChangesAsync();
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~ProviderWebhookHandlerTests"`
Expected: build FAILS with `CS0246` for `ProviderWebhookHandler`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Webhooks/ProviderWebhookEvents.cs`:

```csharp
namespace Live.Wallet.Webhooks;

public static class ProviderWebhookEvents
{
    public const string ChargeSuccess = "charge.success";
    public const string ChargeFailed = "charge.failed";
    public const string TransferSuccess = "transfer.success";
    public const string TransferFailed = "transfer.failed";
}
```

`src/Live.Wallet/Webhooks/ProviderWebhook.cs`:

```csharp
namespace Live.Wallet.Webhooks;

public sealed record ProviderWebhook
{
    public required string Event { get; init; }

    public required string ProviderReferenceId { get; init; }

    /// <summary>Our ReferenceId when the provider echoes it. Lets a transfer webhook match before the provider reference is stored.</summary>
    public string? Reference { get; init; }

    public long Amount { get; init; }
}
```

`src/Live.Wallet/Webhooks/ProviderWebhookHandler.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.TopUps;
using Live.Wallet.Wallets;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Webhooks;

/// <summary>Routes a provider callback to the top-up or withdrawal handler. Every route is idempotent.</summary>
public sealed class ProviderWebhookHandler
{
    private readonly WalletDbContext _db;
    private readonly CompleteTopUpHandler _topUps;
    private readonly SettleWithdrawalHandler _settle;
    private readonly ReleaseWithdrawalHandler _release;

    public ProviderWebhookHandler(
        WalletDbContext db,
        CompleteTopUpHandler topUps,
        SettleWithdrawalHandler settle,
        ReleaseWithdrawalHandler release)
    {
        _db = db;
        _topUps = topUps;
        _settle = settle;
        _release = release;
    }

    public async Task<Result<TransactionResponse>> HandleAsync(ProviderWebhook webhook, CancellationToken cancellationToken)
    {
        switch (webhook.Event)
        {
            case ProviderWebhookEvents.ChargeSuccess:
                return await _topUps.SucceedAsync(webhook.ProviderReferenceId, webhook.Amount, cancellationToken).ConfigureAwait(false);
            case ProviderWebhookEvents.ChargeFailed:
                return await _topUps.FailAsync(webhook.ProviderReferenceId, cancellationToken).ConfigureAwait(false);
            case ProviderWebhookEvents.TransferSuccess:
            case ProviderWebhookEvents.TransferFailed:
                var transactionId = await FindWithdrawalIdAsync(webhook, cancellationToken).ConfigureAwait(false);
                if (transactionId is not { } id)
                {
                    return WalletErrors.TransactionNotFound;
                }

                return webhook.Event == ProviderWebhookEvents.TransferSuccess
                    ? await _settle.HandleAsync(id, cancellationToken).ConfigureAwait(false)
                    : await _release.HandleAsync(id, "Provider reported transfer.failed.", cancellationToken).ConfigureAwait(false);
            default:
                return WalletErrors.UnknownWebhookEvent;
        }
    }

    private Task<Guid?> FindWithdrawalIdAsync(ProviderWebhook webhook, CancellationToken cancellationToken) =>
        _db.Transactions.AsNoTracking()
            .Where(t => t.Type == TransactionType.Withdrawal
                && (t.ProviderReferenceId == webhook.ProviderReferenceId
                    || (webhook.Reference != null && t.ReferenceId == webhook.Reference)))
            .Select(t => (Guid?)t.Id)
            .FirstOrDefaultAsync(cancellationToken);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~ProviderWebhookHandlerTests"`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Webhooks tests/Live.Tests/Wallet/Webhooks
git commit -m "feat(wallet): add provider webhook routing for charges and transfers" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 11: Outbox relay

**Files:**
- Create: `src/Live.Wallet/WalletOptions.cs`, `src/Live.Wallet/Outbox/IMessagePublisher.cs`, `OutboxRelay.cs`
- Create (tests): `tests/Live.Tests/Wallet/RecordingPublisher.cs`
- Test: `tests/Live.Tests/Wallet/Outbox/OutboxRelayTests.cs`

**Interfaces:**
- Consumes: `OutboxMessage`, `WalletDbContext`, `IClock`
- Produces:
  - `WalletOptions { SectionName = "Wallet", OutboxPollInterval, OutboxBatchSize, SweeperInterval, PendingThreshold }`
  - `IMessagePublisher.PublishAsync(string messageType, string payload, Guid messageId, CancellationToken) → Task`
  - `OutboxRelay(WalletDbContext, IMessagePublisher, IClock, IOptions<WalletOptions>, ILogger<OutboxRelay>).RelayPendingAsync(ct) → Task<int>` (the number published)
  - Test helper: `RecordingPublisher { Published, FailWhen }`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/RecordingPublisher.cs`:

```csharp
using Live.Wallet.Outbox;

namespace Live.Tests.Wallet;

internal sealed class RecordingPublisher : IMessagePublisher
{
    public List<(string Type, string Payload, Guid MessageId)> Published { get; } = [];

    public Func<Guid, bool> FailWhen { get; set; } = _ => false;

    public Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken)
    {
        if (FailWhen(messageId))
        {
            throw new InvalidOperationException("broker unavailable");
        }

        Published.Add((messageType, payload, messageId));
        return Task.CompletedTask;
    }
}
```

`tests/Live.Tests/Wallet/Outbox/OutboxRelayTests.cs`:

```csharp
using System.Text.Json;
using FluentAssertions;
using Live.Wallet;
using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Practice.Common;

namespace Live.Tests.Wallet.Outbox;

public sealed class OutboxRelayTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly RecordingPublisher _publisher = new();

    [Fact]
    public async Task Publishes_pending_messages_oldest_first_and_marks_them_processed()
    {
        var first = await AddMessageAsync();
        var second = await AddMessageAsync();
        await using var db = _testDb.CreateContext();

        var published = await Relay(db).RelayPendingAsync(CancellationToken.None);
        var publishedAgain = await Relay(db).RelayPendingAsync(CancellationToken.None);

        published.Should().Be(2);
        publishedAgain.Should().Be(0);
        _publisher.Published.Select(p => p.MessageId).Should().Equal(first, second);
        (await _testDb.QueryAsync(q => q.OutboxMessages.AllAsync(m => m.ProcessedAt != null))).Should().BeTrue();
    }

    [Fact]
    public async Task A_failed_publish_is_recorded_and_retried_on_the_next_run()
    {
        var failing = await AddMessageAsync();
        await AddMessageAsync();
        _publisher.FailWhen = id => id == failing;
        await using var db = _testDb.CreateContext();

        (await Relay(db).RelayPendingAsync(CancellationToken.None)).Should().Be(1);

        var failed = await _testDb.QueryAsync(q => q.OutboxMessages.SingleAsync(m => m.Id == failing));
        failed.ProcessedAt.Should().BeNull();
        failed.Attempts.Should().Be(1);
        failed.LastError.Should().Be("broker unavailable");

        _publisher.FailWhen = _ => false;
        (await Relay(db).RelayPendingAsync(CancellationToken.None)).Should().Be(1);
        (await _testDb.QueryAsync(q => q.OutboxMessages.SingleAsync(m => m.Id == failing))).ProcessedAt.Should().NotBeNull();
    }

    [Fact]
    public async Task Publishes_at_most_one_batch_per_run()
    {
        await AddMessageAsync();
        await AddMessageAsync();
        await AddMessageAsync();
        await using var db = _testDb.CreateContext();

        (await Relay(db, batchSize: 2).RelayPendingAsync(CancellationToken.None)).Should().Be(2);
        (await Relay(db, batchSize: 2).RelayPendingAsync(CancellationToken.None)).Should().Be(1);
    }

    [Fact]
    public async Task A_withdrawal_publishes_a_payout_request_for_its_transaction()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, Guid.NewGuid(), 10_000, 4_000);
        await using var db = _testDb.CreateContext();

        await Relay(db).RelayPendingAsync(CancellationToken.None);

        var (type, payload, _) = _publisher.Published.Should().ContainSingle().Subject;
        type.Should().Be(PayoutRequested.MessageType);
        JsonSerializer.Deserialize(payload, WalletJsonContext.Default.PayoutRequested)?.TransactionId.Should().Be(transactionId);
    }

    private OutboxRelay Relay(WalletDbContext db, int batchSize = 50) =>
        new(db, _publisher, _clock, Options.Create(new WalletOptions { OutboxBatchSize = batchSize }), NullLogger<OutboxRelay>.Instance);

    private async Task<Guid> AddMessageAsync()
    {
        await using var db = _testDb.CreateContext();
        var message = OutboxMessage.Create("test.message", "{}", _clock.UtcNow);
        db.OutboxMessages.Add(message);
        await db.SaveChangesAsync();
        _clock.Advance(TimeSpan.FromSeconds(1));
        return message.Id;
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~OutboxRelayTests"`
Expected: build FAILS with `CS0246` for `IMessagePublisher`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/WalletOptions.cs`:

```csharp
namespace Live.Wallet;

public sealed class WalletOptions
{
    public const string SectionName = "Wallet";

    public TimeSpan OutboxPollInterval { get; set; } = TimeSpan.FromSeconds(1);

    public int OutboxBatchSize { get; set; } = 50;

    public TimeSpan SweeperInterval { get; set; } = TimeSpan.FromMinutes(5);

    /// <summary>How long a withdrawal may stay Pending before the sweeper asks the provider about it.</summary>
    public TimeSpan PendingThreshold { get; set; } = TimeSpan.FromMinutes(15);
}
```

`src/Live.Wallet/Outbox/IMessagePublisher.cs`:

```csharp
namespace Live.Wallet.Outbox;

/// <summary>Sends an outbox message to the broker. Completes only once the broker has accepted it, and throws otherwise.</summary>
public interface IMessagePublisher
{
    Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken);
}
```

`src/Live.Wallet/Outbox/OutboxRelay.cs`:

```csharp
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;
using Practice.Common;

namespace Live.Wallet.Outbox;

/// <summary>
/// Publishes unprocessed outbox messages oldest first and marks each processed after the broker accepts it.
/// A crash between publish and save re-publishes the message, so delivery is at-least-once and consumers must be idempotent.
/// </summary>
public sealed partial class OutboxRelay
{
    private readonly WalletDbContext _db;
    private readonly IMessagePublisher _publisher;
    private readonly IClock _clock;
    private readonly WalletOptions _options;
    private readonly ILogger<OutboxRelay> _logger;

    public OutboxRelay(
        WalletDbContext db,
        IMessagePublisher publisher,
        IClock clock,
        IOptions<WalletOptions> options,
        ILogger<OutboxRelay> logger)
    {
        _db = db;
        _publisher = publisher;
        _clock = clock;
        _options = options.Value;
        _logger = logger;
    }

    public async Task<int> RelayPendingAsync(CancellationToken cancellationToken)
    {
        var batch = await _db.OutboxMessages
            .Where(m => m.ProcessedAt == null)
            .OrderBy(m => m.OccurredAt)
            .ThenBy(m => m.Id)
            .Take(_options.OutboxBatchSize)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);

        var published = 0;
        foreach (var message in batch)
        {
            try
            {
                await _publisher.PublishAsync(message.Type, message.Payload, message.Id, cancellationToken).ConfigureAwait(false);
                message.MarkProcessed(_clock.UtcNow);
                published++;
            }
            catch (Exception exception) when (exception is not OperationCanceledException)
            {
                message.RecordFailure(exception.Message);
                LogPublishFailed(_logger, exception, message.Id, message.Attempts);
            }

            await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        }

        return published;
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Publishing outbox message {MessageId} failed (attempt {Attempts})")]
    private static partial void LogPublishFailed(ILogger logger, Exception exception, Guid messageId, int attempts);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~OutboxRelayTests"`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet tests/Live.Tests/Wallet
git commit -m "feat(wallet): add at-least-once outbox relay" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 12: Payout processor

**Files:**
- Create: `src/Live.Wallet/Payouts/PayoutProcessingOutcome.cs`, `PayoutProcessor.cs`
- Test: `tests/Live.Tests/Wallet/Payouts/PayoutProcessorTests.cs`

**Interfaces:**
- Consumes: `IPaymentProvider`, `ReleaseWithdrawalHandler`, `PayoutRequested`, `WithdrawalDetails`
- Produces:
  - `PayoutProcessingOutcome { Initiated, Rejected, Skipped }`
  - `PayoutProcessor(WalletDbContext, IPaymentProvider, ReleaseWithdrawalHandler, ILogger<PayoutProcessor>).ProcessAsync(PayoutRequested, ct) → Task<Result<PayoutProcessingOutcome>>`
  - Transient provider exceptions propagate, so the consumer can requeue the message.

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Payouts/PayoutProcessorTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Withdrawals;
using Microsoft.Extensions.Logging.Abstractions;
using Practice.Common;

namespace Live.Tests.Wallet.Payouts;

public sealed class PayoutProcessorTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly FakePaymentProvider _provider = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task An_accepted_payout_stores_the_provider_reference_and_stays_pending()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        PayoutInstruction? sent = null;
        _provider.PayoutBehaviour = instruction =>
        {
            sent = instruction;
            return PayoutInitiation.Accepted("trf_accepted");
        };
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Initiated);
        sent.Should().BeEquivalentTo(new PayoutInstruction
        {
            ReferenceId = "wd-1",
            Amount = 4_000,
            BankCode = "058",
            AccountNumber = "0123456789",
            AccountName = "Ada Lovelace",
        });
        var transaction = await _testDb.GetTransactionAsync(transactionId);
        transaction.ProviderReferenceId.Should().Be("trf_accepted");
        transaction.IsFinal.Should().BeFalse();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task A_rejected_payout_releases_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _provider.PayoutBehaviour = _ => PayoutInitiation.Rejected("Invalid account");
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Rejected);
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeTrue();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
    }

    [Fact]
    public async Task A_transient_provider_error_propagates_and_keeps_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _provider.PayoutBehaviour = _ => throw new HttpRequestException("provider timeout");
        await using var db = _testDb.CreateContext();

        var process = () => Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        await process.Should().ThrowAsync<HttpRequestException>();
        var transaction = await _testDb.GetTransactionAsync(transactionId);
        transaction.IsFinal.Should().BeFalse();
        transaction.ProviderReferenceId.Should().BeNull();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task Redelivered_message_after_acceptance_does_not_pay_twice()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        var message = new PayoutRequested { TransactionId = transactionId };

        await Processor(db).ProcessAsync(message, CancellationToken.None);
        var redelivery = await Processor(db).ProcessAsync(message, CancellationToken.None);

        redelivery.Value.Should().Be(PayoutProcessingOutcome.Skipped);
        _provider.PayoutAttempts.Should().Be(1);
    }

    [Fact]
    public async Task A_message_for_a_final_withdrawal_is_skipped()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        await Release(db).HandleAsync(transactionId, "test", CancellationToken.None);

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Skipped);
        _provider.PayoutAttempts.Should().Be(0);
    }

    [Fact]
    public async Task A_message_for_an_unknown_transaction_is_skipped()
    {
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = Guid.NewGuid() }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Skipped);
    }

    private PayoutProcessor Processor(WalletDbContext db) =>
        new(db, _provider, Release(db), NullLogger<PayoutProcessor>.Instance);

    private ReleaseWithdrawalHandler Release(WalletDbContext db) =>
        new(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance);
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~PayoutProcessorTests"`
Expected: build FAILS with `CS0246` for `PayoutProcessor`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Payouts/PayoutProcessingOutcome.cs`:

```csharp
namespace Live.Wallet.Payouts;

public enum PayoutProcessingOutcome
{
    Initiated,
    Rejected,
    Skipped,
}
```

`src/Live.Wallet/Payouts/PayoutProcessor.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Practice.Common;

namespace Live.Wallet.Payouts;

/// <summary>
/// Sends one held withdrawal to the provider. Acceptance only records the provider reference: the webhook or the sweeper settles it later.
/// A definite rejection releases the hold. Transient faults throw so the message is redelivered.
/// </summary>
public sealed partial class PayoutProcessor
{
    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly ReleaseWithdrawalHandler _release;
    private readonly ILogger<PayoutProcessor> _logger;

    public PayoutProcessor(
        WalletDbContext db,
        IPaymentProvider provider,
        ReleaseWithdrawalHandler release,
        ILogger<PayoutProcessor> logger)
    {
        _db = db;
        _provider = provider;
        _release = release;
        _logger = logger;
    }

    public async Task<Result<PayoutProcessingOutcome>> ProcessAsync(PayoutRequested message, CancellationToken cancellationToken)
    {
        var transaction = await _db.Transactions
            .SingleOrDefaultAsync(t => t.Id == message.TransactionId && t.Type == TransactionType.Withdrawal, cancellationToken)
            .ConfigureAwait(false);
        if (transaction is null)
        {
            LogUnknownTransaction(_logger, message.TransactionId);
            return PayoutProcessingOutcome.Skipped;
        }

        // A provider reference means an earlier delivery already got this payout accepted.
        if (transaction.IsFinal || transaction.ProviderReferenceId is not null)
        {
            return PayoutProcessingOutcome.Skipped;
        }

        var details = await _db.Withdrawals.AsNoTracking()
            .SingleAsync(w => w.TransactionId == transaction.Id, cancellationToken)
            .ConfigureAwait(false);
        var instruction = new PayoutInstruction
        {
            ReferenceId = transaction.ReferenceId,
            Amount = transaction.Amount,
            BankCode = details.BankCode,
            AccountNumber = details.AccountNumber,
            AccountName = details.AccountName,
        };
        var initiation = await _provider.InitiatePayoutAsync(instruction, cancellationToken).ConfigureAwait(false);

        if (initiation is { Outcome: PayoutOutcome.Accepted, ProviderReferenceId: { } providerReferenceId })
        {
            transaction.AssignProviderReference(providerReferenceId);
            await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
            return PayoutProcessingOutcome.Initiated;
        }

        var released = await _release
            .HandleAsync(transaction.Id, initiation.FailureReason ?? "Provider rejected the payout.", cancellationToken)
            .ConfigureAwait(false);
        if (released.IsFailure)
        {
            return released.Error;
        }

        return PayoutProcessingOutcome.Rejected;
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout requested for unknown withdrawal {TransactionId}; skipping")]
    private static partial void LogUnknownTransaction(ILogger logger, Guid transactionId);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~PayoutProcessorTests"`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Payouts tests/Live.Tests/Wallet/Payouts
git commit -m "feat(wallet): add idempotent payout processor" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 13: Reconciliation (pending-payout sweeper + invariant checker)

**Files:**
- Create: `src/Live.Wallet/Reconciliation/SweepSummary.cs`, `PendingPayoutSweeper.cs`, `InvariantViolation.cs`, `LedgerInvariantChecker.cs`
- Test: `tests/Live.Tests/Wallet/Reconciliation/PendingPayoutSweeperTests.cs`, `LedgerInvariantCheckerTests.cs`

**Interfaces:**
- Consumes: `IPaymentProvider.GetPayoutStatusAsync`, `SettleWithdrawalHandler`, `ReleaseWithdrawalHandler`, `WalletOptions.PendingThreshold`
- Produces:
  - `SweepSummary { Settled, Released, StillPending, Errors }`
  - `PendingPayoutSweeper(WalletDbContext, IPaymentProvider, SettleWithdrawalHandler, ReleaseWithdrawalHandler, IClock, IOptions<WalletOptions>, ILogger<PendingPayoutSweeper>).SweepAsync(ct) → Task<SweepSummary>`
  - `InvariantViolation { Invariant, Detail }`
  - `LedgerInvariantChecker(WalletDbContext, ILogger<LedgerInvariantChecker>).CheckAsync(ct) → Task<IReadOnlyList<InvariantViolation>>` (invariant codes `I1`, `I3`, `I4`, `I6`)

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Wallet/Reconciliation/PendingPayoutSweeperTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Reconciliation;
using Live.Wallet.Withdrawals;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Practice.Common;

namespace Live.Tests.Wallet.Reconciliation;

public sealed class PendingPayoutSweeperTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly FakePaymentProvider _provider = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task Settles_a_stale_withdrawal_the_provider_reports_succeeded()
    {
        var transactionId = await SubmittedWithdrawalAsync();
        _provider.SetPayoutStatus("wd-1", PayoutStatus.Succeeded);
        _clock.Advance(TimeSpan.FromMinutes(16));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.Settled.Should().Be(1);
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeTrue();
        (await _testDb.GetWalletAsync(_userId)).Balance.Should().Be(6_000);
    }

    [Fact]
    public async Task Releases_a_stale_withdrawal_the_provider_reports_failed()
    {
        await SubmittedWithdrawalAsync();
        _provider.SetPayoutStatus("wd-1", PayoutStatus.Failed);
        _clock.Advance(TimeSpan.FromMinutes(16));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.Released.Should().Be(1);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
    }

    [Fact]
    public async Task Ignores_withdrawals_younger_than_the_threshold()
    {
        var transactionId = await SubmittedWithdrawalAsync();
        _provider.SetPayoutStatus("wd-1", PayoutStatus.Succeeded);
        _clock.Advance(TimeSpan.FromMinutes(10));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.Should().BeEquivalentTo(new SweepSummary { Settled = 0, Released = 0, StillPending = 0, Errors = 0 });
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeFalse();
    }

    [Fact]
    public async Task Keeps_a_payout_the_provider_has_never_seen_on_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _clock.Advance(TimeSpan.FromMinutes(16));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.StillPending.Should().Be(1);
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeFalse();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task Leaves_payouts_the_provider_still_has_pending()
    {
        await SubmittedWithdrawalAsync();
        _clock.Advance(TimeSpan.FromMinutes(16));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.StillPending.Should().Be(1);
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    private PendingPayoutSweeper Sweeper(WalletDbContext db) =>
        new(
            db,
            _provider,
            new SettleWithdrawalHandler(db, _clock),
            new ReleaseWithdrawalHandler(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance),
            _clock,
            Options.Create(new WalletOptions { PendingThreshold = TimeSpan.FromMinutes(15) }),
            NullLogger<PendingPayoutSweeper>.Instance);

    /// <summary>A Pending withdrawal the provider has accepted (so it knows the payout).</summary>
    private async Task<Guid> SubmittedWithdrawalAsync()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        var processor = new PayoutProcessor(
            db, _provider, new ReleaseWithdrawalHandler(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance), NullLogger<PayoutProcessor>.Instance);
        await processor.ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);
        return transactionId;
    }
}
```

`tests/Live.Tests/Wallet/Reconciliation/LedgerInvariantCheckerTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Reconciliation;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Practice.Common;

namespace Live.Tests.Wallet.Reconciliation;

public sealed class LedgerInvariantCheckerTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task A_healthy_ledger_has_no_violations()
    {
        var settledId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using (var db = _testDb.CreateContext())
        {
            await new SettleWithdrawalHandler(db, _clock).HandleAsync(settledId, CancellationToken.None);
            await new RequestWithdrawalHandler(db, _clock).HandleAsync(TestCommands.Withdrawal(_userId, 1_000, "wd-2"), CancellationToken.None);
        }

        (await CheckAsync()).Should().BeEmpty();
    }

    [Fact]
    public async Task Detects_an_unbalanced_transaction_and_the_account_drift_it_causes()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using (var db = _testDb.CreateContext())
        {
            db.LedgerEntries.Add(LedgerEntry.Create(Guid.NewGuid(), walletId, EntryDirection.Credit, 50, "NGN", 10_000, 10_050, TestDb.Start));
            await db.SaveChangesAsync();
        }

        (await CheckAsync()).Select(v => v.Invariant).Should().Contain("I1").And.Contain("I3");
    }

    [Fact]
    public async Task Detects_a_balance_edited_outside_the_ledger()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using (var db = _testDb.CreateContext())
        {
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId);
            db.Entry(wallet).Property(a => a.Balance).CurrentValue = 20_000;
            await db.SaveChangesAsync();
        }

        (await CheckAsync()).Select(v => v.Invariant).Should().Contain("I3").And.Contain("I6");
    }

    [Fact]
    public async Task Detects_available_balance_above_balance()
    {
        var walletId = await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using (var db = _testDb.CreateContext())
        {
            var wallet = await db.Accounts.SingleAsync(a => a.Id == walletId);
            db.Entry(wallet).Property(a => a.AvailableBalance).CurrentValue = 10_001;
            await db.SaveChangesAsync();
        }

        (await CheckAsync()).Select(v => v.Invariant).Should().Equal("I4");
    }

    private async Task<IReadOnlyList<InvariantViolation>> CheckAsync()
    {
        await using var db = _testDb.CreateContext();
        return await new LedgerInvariantChecker(db, NullLogger<LedgerInvariantChecker>.Instance).CheckAsync(CancellationToken.None);
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet.Reconciliation"`
Expected: build FAILS with `CS0246` for `PendingPayoutSweeper`.

- [ ] **Step 3: Implement**

`src/Live.Wallet/Reconciliation/SweepSummary.cs`:

```csharp
namespace Live.Wallet.Reconciliation;

public sealed record SweepSummary
{
    public required int Settled { get; init; }

    public required int Released { get; init; }

    public required int StillPending { get; init; }

    public required int Errors { get; init; }
}
```

`src/Live.Wallet/Reconciliation/PendingPayoutSweeper.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;
using Practice.Common;

namespace Live.Wallet.Reconciliation;

/// <summary>
/// Asks the provider about withdrawals still Pending after the threshold, and settles or releases them through the webhook handlers.
/// A payout the provider has never seen stays held: a delayed outbox message may still submit it, and releasing could pay out and refund.
/// </summary>
public sealed partial class PendingPayoutSweeper
{
    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly SettleWithdrawalHandler _settle;
    private readonly ReleaseWithdrawalHandler _release;
    private readonly IClock _clock;
    private readonly WalletOptions _options;
    private readonly ILogger<PendingPayoutSweeper> _logger;

    public PendingPayoutSweeper(
        WalletDbContext db,
        IPaymentProvider provider,
        SettleWithdrawalHandler settle,
        ReleaseWithdrawalHandler release,
        IClock clock,
        IOptions<WalletOptions> options,
        ILogger<PendingPayoutSweeper> logger)
    {
        _db = db;
        _provider = provider;
        _settle = settle;
        _release = release;
        _clock = clock;
        _options = options.Value;
        _logger = logger;
    }

    public async Task<SweepSummary> SweepAsync(CancellationToken cancellationToken)
    {
        var cutoff = _clock.UtcNow - _options.PendingThreshold;
        var stale = await _db.Transactions.AsNoTracking()
            .Where(t => t.Type == TransactionType.Withdrawal && t.Status == TransactionStatus.Pending && t.CreatedAt <= cutoff)
            .OrderBy(t => t.CreatedAt)
            .Select(t => new { t.Id, t.ReferenceId })
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);

        int settled = 0, released = 0, stillPending = 0, errors = 0;
        foreach (var withdrawal in stale)
        {
            PayoutStatus status;
            try
            {
                status = await _provider.GetPayoutStatusAsync(withdrawal.ReferenceId, cancellationToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (exception is not OperationCanceledException)
            {
                errors++;
                LogStatusQueryFailed(_logger, exception, withdrawal.Id);
                continue;
            }

            switch (status)
            {
                case PayoutStatus.Succeeded:
                    var settledResult = await _settle.HandleAsync(withdrawal.Id, cancellationToken).ConfigureAwait(false);
                    if (settledResult.IsSuccess) { settled++; } else { errors++; }
                    break;
                case PayoutStatus.Failed:
                    var releasedResult = await _release
                        .HandleAsync(withdrawal.Id, "Provider reported the payout failed during reconciliation.", cancellationToken)
                        .ConfigureAwait(false);
                    if (releasedResult.IsSuccess) { released++; } else { errors++; }
                    break;
                case PayoutStatus.NotFound:
                    LogPayoutUnknownToProvider(_logger, withdrawal.Id);
                    stillPending++;
                    break;
                default:
                    stillPending++;
                    break;
            }
        }

        return new SweepSummary { Settled = settled, Released = released, StillPending = stillPending, Errors = errors };
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Querying the provider for withdrawal {TransactionId} failed")]
    private static partial void LogStatusQueryFailed(ILogger logger, Exception exception, Guid transactionId);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Withdrawal {TransactionId} is past the pending threshold but unknown to the provider; keeping the hold for manual review")]
    private static partial void LogPayoutUnknownToProvider(ILogger logger, Guid transactionId);
}
```

`src/Live.Wallet/Reconciliation/InvariantViolation.cs`:

```csharp
namespace Live.Wallet.Reconciliation;

public sealed record InvariantViolation
{
    /// <summary>The spec's invariant id: I1 balanced, I3 account matches entries, I4 non-negative wallet, I6 global zero-sum.</summary>
    public required string Invariant { get; init; }

    public required string Detail { get; init; }
}
```

`src/Live.Wallet/Reconciliation/LedgerInvariantChecker.cs`:

```csharp
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;

namespace Live.Wallet.Reconciliation;

/// <summary>Checks the ledger invariants and logs every violation. It never corrects anything: drift needs a human.</summary>
public sealed partial class LedgerInvariantChecker
{
    private readonly WalletDbContext _db;
    private readonly ILogger<LedgerInvariantChecker> _logger;

    public LedgerInvariantChecker(WalletDbContext db, ILogger<LedgerInvariantChecker> logger)
    {
        _db = db;
        _logger = logger;
    }

    public async Task<IReadOnlyList<InvariantViolation>> CheckAsync(CancellationToken cancellationToken)
    {
        List<InvariantViolation> violations = [];

        var unbalanced = await _db.LedgerEntries
            .GroupBy(e => e.TransactionId)
            .Select(g => new
            {
                TransactionId = g.Key,
                Count = g.Count(),
                DebitCount = g.Count(e => e.Direction == EntryDirection.Debit),
                Debits = g.Where(e => e.Direction == EntryDirection.Debit).Sum(e => e.Amount),
                Credits = g.Where(e => e.Direction == EntryDirection.Credit).Sum(e => e.Amount),
            })
            .Where(x => x.Count != 2 || x.DebitCount != 1 || x.Debits != x.Credits)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(unbalanced.Select(x => new InvariantViolation
        {
            Invariant = "I1",
            Detail = $"Transaction {x.TransactionId} has {x.Count} entries (debits {x.Debits}, credits {x.Credits}).",
        }));

        var drifted = await _db.Accounts
            .Select(a => new
            {
                a.Id,
                a.Balance,
                Net = _db.LedgerEntries.Where(e => e.AccountId == a.Id)
                    .Sum(e => e.Direction == EntryDirection.Credit ? e.Amount : -e.Amount),
            })
            .Where(x => x.Balance != x.Net)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(drifted.Select(x => new InvariantViolation
        {
            Invariant = "I3",
            Detail = $"Account {x.Id} balance {x.Balance} differs from its entries' net {x.Net}.",
        }));

        var invalidWallets = await _db.Accounts
            .Where(a => a.Type == AccountType.User && (a.Balance < 0 || a.AvailableBalance < 0 || a.AvailableBalance > a.Balance))
            .Select(a => new { a.Id, a.Balance, a.AvailableBalance })
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(invalidWallets.Select(x => new InvariantViolation
        {
            Invariant = "I4",
            Detail = $"Wallet {x.Id} has balance {x.Balance} and available {x.AvailableBalance}.",
        }));

        var total = await _db.Accounts.SumAsync(a => a.Balance, cancellationToken).ConfigureAwait(false);
        if (total != 0)
        {
            violations.Add(new InvariantViolation { Invariant = "I6", Detail = $"Account balances sum to {total}, not 0." });
        }

        foreach (var violation in violations)
        {
            LogViolation(_logger, violation.Invariant, violation.Detail);
        }

        return violations;
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Ledger invariant {Invariant} violated: {Detail}")]
    private static partial void LogViolation(ILogger logger, string invariant, string detail);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Wallet.Reconciliation"`
Expected: PASS (9 tests).

- [ ] **Step 5: Commit**

```bash
git add src/Live.Wallet/Reconciliation tests/Live.Tests/Wallet/Reconciliation
git commit -m "feat(wallet): add pending payout sweeper and ledger invariant checker" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 14: RabbitMQ adapters, background workers, DI registration

**Files:**
- Create: `src/Live.Wallet/Payouts/PayoutQueueTopology.cs`, `RabbitMqPayoutConsumer.cs`
- Create: `src/Live.Wallet/Outbox/RabbitMqMessagePublisher.cs`, `OutboxRelayService.cs`
- Create: `src/Live.Wallet/Reconciliation/ReconciliationService.cs`
- Create: `src/Live.Wallet/ServiceCollectionExtensions.cs`
- Test: `tests/Live.Tests/Wallet/ServiceRegistrationTests.cs`

**Interfaces:**
- Consumes: every handler and worker class from Tasks 5–13, and `RabbitMQ.Client.IConnection` (registered by `AddRabbitMQClient` in Task 15)
- Produces:
  - `services.AddWallet(IConfiguration)`: registers options, `IClock`, the fake provider, the publisher, all handlers and workers as scoped
  - `services.AddWalletBackgroundWorkers()`: registers 3 hosted services
  - `PayoutQueueTopology.QueueName/DeadLetterExchange/DeadLetterQueue/DeliveryLimit` and `DeclareAsync(IChannel, ct)`

- [ ] **Step 1: Write the failing test**

`tests/Live.Tests/Wallet/ServiceRegistrationTests.cs`:

```csharp
using FluentAssertions;
using Live.Wallet;
using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Reconciliation;
using Live.Wallet.TopUps;
using Live.Wallet.Wallets;
using Live.Wallet.Webhooks;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Options;
using NSubstitute;
using RabbitMQ.Client;

namespace Live.Tests.Wallet;

public sealed class ServiceRegistrationTests
{
    [Fact]
    public async Task Every_wallet_service_resolves_with_valid_lifetimes()
    {
        var configuration = new ConfigurationBuilder()
            .AddInMemoryCollection(new Dictionary<string, string?> { ["Wallet:PendingThreshold"] = "00:30:00" })
            .Build();
        var services = new ServiceCollection();
        services.AddLogging();
        services.AddDbContext<WalletDbContext>(options => options.UseInMemoryDatabase(Guid.NewGuid().ToString()));
        services.AddSingleton(Substitute.For<IConnection>());

        services.AddWallet(configuration);
        services.AddWalletBackgroundWorkers();

        // Async disposal: RabbitMqMessagePublisher is IAsyncDisposable only, and sync Dispose of the provider would throw.
        await using var provider = services.BuildServiceProvider(new ServiceProviderOptions { ValidateOnBuild = true, ValidateScopes = true });
        await using var scope = provider.CreateAsyncScope();
        Type[] scoped =
        [
            typeof(CreateWalletHandler), typeof(GetWalletHandler), typeof(ListTransactionsHandler),
            typeof(InitiateTopUpHandler), typeof(CompleteTopUpHandler), typeof(RequestWithdrawalHandler),
            typeof(SettleWithdrawalHandler), typeof(ReleaseWithdrawalHandler), typeof(ProviderWebhookHandler),
            typeof(OutboxRelay), typeof(PayoutProcessor), typeof(PendingPayoutSweeper), typeof(LedgerInvariantChecker),
        ];
        foreach (var type in scoped)
        {
            scope.ServiceProvider.GetRequiredService(type).Should().NotBeNull(type.Name);
        }

        provider.GetRequiredService<IPaymentProvider>().Should().BeSameAs(provider.GetRequiredService<FakePaymentProvider>());
        provider.GetRequiredService<IMessagePublisher>().Should().BeOfType<RabbitMqMessagePublisher>();
        provider.GetRequiredService<IOptions<WalletOptions>>().Value.PendingThreshold.Should().Be(TimeSpan.FromMinutes(30));
        provider.GetServices<IHostedService>().Should().HaveCount(3);
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~ServiceRegistrationTests"`
Expected: build FAILS with `CS1061: 'ServiceCollection' does not contain a definition for 'AddWallet'`.

- [ ] **Step 3: Implement the RabbitMQ adapters**

`src/Live.Wallet/Payouts/PayoutQueueTopology.cs`:

```csharp
using RabbitMQ.Client;

namespace Live.Wallet.Payouts;

/// <summary>
/// A durable quorum queue. RabbitMQ counts redeliveries and dead-letters a message after DeliveryLimit attempts.
/// Declaring is idempotent; the publisher and the consumer both declare before use.
/// </summary>
public static class PayoutQueueTopology
{
    public const string QueueName = "wallet.payouts";
    public const string DeadLetterExchange = "wallet.payouts.dlx";
    public const string DeadLetterQueue = "wallet.payouts.dead";
    public const int DeliveryLimit = 5;

    public static async Task DeclareAsync(IChannel channel, CancellationToken cancellationToken)
    {
        await channel.ExchangeDeclareAsync(DeadLetterExchange, ExchangeType.Fanout, durable: true, autoDelete: false, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueDeclareAsync(DeadLetterQueue, durable: true, exclusive: false, autoDelete: false, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueBindAsync(DeadLetterQueue, DeadLetterExchange, routingKey: string.Empty, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueDeclareAsync(
            QueueName,
            durable: true,
            exclusive: false,
            autoDelete: false,
            arguments: new Dictionary<string, object?>
            {
                ["x-queue-type"] = "quorum",
                ["x-delivery-limit"] = DeliveryLimit,
                ["x-dead-letter-exchange"] = DeadLetterExchange,
            },
            cancellationToken: cancellationToken).ConfigureAwait(false);
    }
}
```

`src/Live.Wallet/Outbox/RabbitMqMessagePublisher.cs`:

```csharp
using System.Text;
using Live.Wallet.Payouts;
using RabbitMQ.Client;

namespace Live.Wallet.Outbox;

/// <summary>Publishes outbox messages with publisher confirms, so a publish completes only after the broker has the message.</summary>
public sealed class RabbitMqMessagePublisher : IMessagePublisher, IAsyncDisposable
{
    private readonly IConnection _connection;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private IChannel? _channel;

    public RabbitMqMessagePublisher(IConnection connection)
    {
        _connection = connection;
    }

    public async Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken)
    {
        if (messageType != PayoutRequested.MessageType)
        {
            throw new InvalidOperationException($"No queue is configured for message type '{messageType}'.");
        }

        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            var channel = await GetChannelAsync(cancellationToken).ConfigureAwait(false);
            var properties = new BasicProperties
            {
                Persistent = true,
                MessageId = messageId.ToString(),
                Type = messageType,
                ContentType = "application/json",
            };
            await channel.BasicPublishAsync(
                exchange: string.Empty,
                routingKey: PayoutQueueTopology.QueueName,
                mandatory: true,
                basicProperties: properties,
                body: Encoding.UTF8.GetBytes(payload),
                cancellationToken: cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _gate.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (_channel is not null)
        {
            await _channel.DisposeAsync().ConfigureAwait(false);
        }

        _gate.Dispose();
    }

    private async Task<IChannel> GetChannelAsync(CancellationToken cancellationToken)
    {
        if (_channel is { IsOpen: true } open)
        {
            return open;
        }

        if (_channel is not null)
        {
            await _channel.DisposeAsync().ConfigureAwait(false);
        }

        var options = new CreateChannelOptions(publisherConfirmationsEnabled: true, publisherConfirmationTrackingEnabled: true);
        var channel = await _connection.CreateChannelAsync(options, cancellationToken).ConfigureAwait(false);
        await PayoutQueueTopology.DeclareAsync(channel, cancellationToken).ConfigureAwait(false);
        _channel = channel;
        return channel;
    }
}
```

`src/Live.Wallet/Payouts/RabbitMqPayoutConsumer.cs`:

```csharp
using System.Text.Json;
using Live.Wallet.Outbox;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using RabbitMQ.Client;
using RabbitMQ.Client.Events;

namespace Live.Wallet.Payouts;

/// <summary>
/// Feeds wallet.payouts messages to PayoutProcessor. It acks on success; on failure it nacks with requeue,
/// so the quorum queue dead-letters after the delivery limit. Unreadable messages go straight to the dead-letter queue.
/// </summary>
public sealed partial class RabbitMqPayoutConsumer : BackgroundService
{
    private static readonly TimeSpan RetryDelay = TimeSpan.FromSeconds(2);

    private readonly IConnection _connection;
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly ILogger<RabbitMqPayoutConsumer> _logger;

    public RabbitMqPayoutConsumer(IConnection connection, IServiceScopeFactory scopeFactory, ILogger<RabbitMqPayoutConsumer> logger)
    {
        _connection = connection;
        _scopeFactory = scopeFactory;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        await using var channel = await _connection.CreateChannelAsync(cancellationToken: stoppingToken).ConfigureAwait(false);
        await PayoutQueueTopology.DeclareAsync(channel, stoppingToken).ConfigureAwait(false);
        await channel.BasicQosAsync(prefetchSize: 0, prefetchCount: 10, global: false, cancellationToken: stoppingToken).ConfigureAwait(false);
        var consumer = new AsyncEventingBasicConsumer(channel);
        consumer.ReceivedAsync += (_, delivery) => HandleDeliveryAsync(channel, delivery, stoppingToken);
        await channel.BasicConsumeAsync(PayoutQueueTopology.QueueName, autoAck: false, consumer: consumer, cancellationToken: stoppingToken).ConfigureAwait(false);
        await Task.Delay(Timeout.Infinite, stoppingToken).ConfigureAwait(false);
    }

    private async Task HandleDeliveryAsync(IChannel channel, BasicDeliverEventArgs delivery, CancellationToken cancellationToken)
    {
        PayoutRequested? message;
        try
        {
            message = JsonSerializer.Deserialize(delivery.Body.Span, WalletJsonContext.Default.PayoutRequested);
        }
        catch (JsonException exception)
        {
            LogUnreadableMessage(_logger, exception, delivery.BasicProperties.MessageId);
            message = null;
        }

        if (message is null)
        {
            await channel.BasicNackAsync(delivery.DeliveryTag, multiple: false, requeue: false, cancellationToken: cancellationToken).ConfigureAwait(false);
            return;
        }

        try
        {
            await using var scope = _scopeFactory.CreateAsyncScope();
            var processor = scope.ServiceProvider.GetRequiredService<PayoutProcessor>();
            var result = await processor.ProcessAsync(message, cancellationToken).ConfigureAwait(false);
            if (result.IsSuccess)
            {
                await channel.BasicAckAsync(delivery.DeliveryTag, multiple: false, cancellationToken: cancellationToken).ConfigureAwait(false);
                return;
            }

            LogProcessingFailed(_logger, message.TransactionId, result.Error.Code);
        }
        catch (Exception exception) when (exception is not OperationCanceledException)
        {
            LogTransientFailure(_logger, exception, message.TransactionId);
        }

        // Short pause so a struggling provider isn't hammered; the quorum queue's delivery limit caps the retries.
        await Task.Delay(RetryDelay, cancellationToken).ConfigureAwait(false);
        await channel.BasicNackAsync(delivery.DeliveryTag, multiple: false, requeue: true, cancellationToken: cancellationToken).ConfigureAwait(false);
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Dead-lettering unreadable payout message {MessageId}")]
    private static partial void LogUnreadableMessage(ILogger logger, Exception exception, string? messageId);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout for withdrawal {TransactionId} failed with {ErrorCode}; requeueing")]
    private static partial void LogProcessingFailed(ILogger logger, Guid transactionId, string errorCode);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout for withdrawal {TransactionId} hit a transient error; requeueing")]
    private static partial void LogTransientFailure(ILogger logger, Exception exception, Guid transactionId);
}
```

- [ ] **Step 4: Implement the polling services**

`src/Live.Wallet/Outbox/OutboxRelayService.cs`:

```csharp
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

namespace Live.Wallet.Outbox;

public sealed partial class OutboxRelayService : BackgroundService
{
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly WalletOptions _options;
    private readonly ILogger<OutboxRelayService> _logger;

    public OutboxRelayService(IServiceScopeFactory scopeFactory, IOptions<WalletOptions> options, ILogger<OutboxRelayService> logger)
    {
        _scopeFactory = scopeFactory;
        _options = options.Value;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        using var timer = new PeriodicTimer(_options.OutboxPollInterval);
        while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
        {
            try
            {
                await using var scope = _scopeFactory.CreateAsyncScope();
                await scope.ServiceProvider.GetRequiredService<OutboxRelay>().RelayPendingAsync(stoppingToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (exception is not OperationCanceledException)
            {
                LogRelayFailed(_logger, exception);
            }
        }
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Outbox relay run failed")]
    private static partial void LogRelayFailed(ILogger logger, Exception exception);
}
```

`src/Live.Wallet/Reconciliation/ReconciliationService.cs`:

```csharp
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

namespace Live.Wallet.Reconciliation;

public sealed partial class ReconciliationService : BackgroundService
{
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly WalletOptions _options;
    private readonly ILogger<ReconciliationService> _logger;

    public ReconciliationService(IServiceScopeFactory scopeFactory, IOptions<WalletOptions> options, ILogger<ReconciliationService> logger)
    {
        _scopeFactory = scopeFactory;
        _options = options.Value;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        using var timer = new PeriodicTimer(_options.SweeperInterval);
        while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
        {
            try
            {
                await using var scope = _scopeFactory.CreateAsyncScope();
                var summary = await scope.ServiceProvider.GetRequiredService<PendingPayoutSweeper>().SweepAsync(stoppingToken).ConfigureAwait(false);
                LogSweepCompleted(_logger, summary.Settled, summary.Released, summary.StillPending, summary.Errors);
                await scope.ServiceProvider.GetRequiredService<LedgerInvariantChecker>().CheckAsync(stoppingToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (exception is not OperationCanceledException)
            {
                LogReconciliationFailed(_logger, exception);
            }
        }
    }

    [LoggerMessage(Level = LogLevel.Information, Message = "Payout sweep: {Settled} settled, {Released} released, {StillPending} still pending, {Errors} errors")]
    private static partial void LogSweepCompleted(ILogger logger, int settled, int released, int stillPending, int errors);

    [LoggerMessage(Level = LogLevel.Error, Message = "Reconciliation run failed")]
    private static partial void LogReconciliationFailed(ILogger logger, Exception exception);
}
```

- [ ] **Step 5: Implement the DI registration**

`src/Live.Wallet/ServiceCollectionExtensions.cs`:

```csharp
using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Providers;
using Live.Wallet.Reconciliation;
using Live.Wallet.TopUps;
using Live.Wallet.Wallets;
using Live.Wallet.Webhooks;
using Live.Wallet.Withdrawals;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.DependencyInjection.Extensions;
using Practice.Common;

namespace Live.Wallet;

public static class ServiceCollectionExtensions
{
    /// <summary>Registers the wallet's handlers and adapters. The host must also register WalletDbContext and a RabbitMQ IConnection.</summary>
    public static IServiceCollection AddWallet(this IServiceCollection services, IConfiguration configuration)
    {
        services.AddOptions<WalletOptions>().Bind(configuration.GetSection(WalletOptions.SectionName));
        services.TryAddSingleton<IClock, SystemClock>();
        services.AddSingleton<FakePaymentProvider>();
        services.AddSingleton<IPaymentProvider>(sp => sp.GetRequiredService<FakePaymentProvider>());
        services.AddSingleton<IMessagePublisher, RabbitMqMessagePublisher>();

        services.AddScoped<CreateWalletHandler>();
        services.AddScoped<GetWalletHandler>();
        services.AddScoped<ListTransactionsHandler>();
        services.AddScoped<InitiateTopUpHandler>();
        services.AddScoped<CompleteTopUpHandler>();
        services.AddScoped<RequestWithdrawalHandler>();
        services.AddScoped<SettleWithdrawalHandler>();
        services.AddScoped<ReleaseWithdrawalHandler>();
        services.AddScoped<ProviderWebhookHandler>();
        services.AddScoped<OutboxRelay>();
        services.AddScoped<PayoutProcessor>();
        services.AddScoped<PendingPayoutSweeper>();
        services.AddScoped<LedgerInvariantChecker>();
        return services;
    }

    public static IServiceCollection AddWalletBackgroundWorkers(this IServiceCollection services)
    {
        services.AddHostedService<OutboxRelayService>();
        services.AddHostedService<RabbitMqPayoutConsumer>();
        services.AddHostedService<ReconciliationService>();
        return services;
    }
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~ServiceRegistrationTests"`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/Live.Wallet tests/Live.Tests/Wallet/ServiceRegistrationTests.cs
git commit -m "feat(wallet): add RabbitMQ adapters, background workers and DI registration" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 15: Wallet API (controllers, Program wiring, integration tests)

**Files:**
- Modify: `src/Live.Api/Live.Api.csproj`, `src/Live.Api/Program.cs`
- Create: `src/Live.Api/Controllers/ProblemResults.cs`, `WalletsController.cs`, `TopUpRequest.cs`, `TopUpsController.cs`, `WithdrawalRequest.cs`, `WithdrawalsController.cs`, `ProviderWebhooksController.cs`, `DevProviderController.cs`
- Test: `tests/Live.Tests/Api/WalletApiFactory.cs`, `WalletApiTests.cs`, `ProblemResultsTests.cs`

**Interfaces:**
- Consumes: `AddWallet`, `AddWalletBackgroundWorkers`, every handler, `FakePaymentProvider`
- Produces (HTTP; bodies are camelCase JSON):
  - `POST /users/{userId}/wallet` → 201 or 200 `WalletResponse`
  - `GET /users/{userId}/wallet` → 200 `WalletResponse`
  - `GET /users/{userId}/wallet/transactions` → 200 `TransactionResponse[]`
  - `POST /users/{userId}/top-ups` `{referenceId, amount}` → 201 (new) or 200 (replay) `TransactionResponse`
  - `POST /users/{userId}/withdrawals` `{referenceId, amount, bankCode, accountNumber, accountName}` → 202 (new) or 200 (replay)
  - `POST /webhooks/provider` `{event, providerReferenceId, reference?, amount}` → 200 `TransactionResponse`
  - `POST /dev/provider/{providerReferenceId}/complete?outcome=success|failed` → 200 (Development only; 404 elsewhere)
  - Errors → ProblemDetails with `title` = error code
  - `ProblemResults.StatusCodeFor(Error) → int`

- [ ] **Step 1: Write the failing tests**

`tests/Live.Tests/Api/WalletApiFactory.cs`:

```csharp
using Live.Wallet;
using Live.Wallet.Persistence;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Mvc.Testing;
using Microsoft.AspNetCore.TestHost;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Storage;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;

namespace Live.Tests.Api;

/// <summary>Runs the real API pipeline on an InMemory WalletDbContext, with the RabbitMQ workers removed.</summary>
public sealed class WalletApiFactory : WebApplicationFactory<Program>
{
    private readonly string _databaseName = Guid.NewGuid().ToString();
    private readonly InMemoryDatabaseRoot _root = new();

    protected override void ConfigureWebHost(IWebHostBuilder builder)
    {
        builder.UseEnvironment("Testing");
        // Aspire's client registrations need connection strings to exist; nothing connects, because the DbContext is replaced and no worker runs.
        builder.UseSetting("ConnectionStrings:appdb", "Host=localhost;Database=wallet_api_tests");
        builder.UseSetting("ConnectionStrings:messaging", "amqp://localhost:5672");
        builder.ConfigureTestServices(services =>
        {
            var walletDbRegistrations = services
                .Where(d => d.ServiceType == typeof(WalletDbContext) || d.ServiceType.GenericTypeArguments.Contains(typeof(WalletDbContext)))
                .ToList();
            foreach (var descriptor in walletDbRegistrations)
            {
                services.Remove(descriptor);
            }

            services.AddDbContext<WalletDbContext>(options => options.UseInMemoryDatabase(_databaseName, _root));

            var walletWorkers = services
                .Where(d => d.ServiceType == typeof(IHostedService) && d.ImplementationType?.Assembly == typeof(WalletOptions).Assembly)
                .ToList();
            foreach (var descriptor in walletWorkers)
            {
                services.Remove(descriptor);
            }
        });
    }

    protected override IHost CreateHost(IHostBuilder builder)
    {
        var host = base.CreateHost(builder);
        // EnsureCreated applies the HasData seed (the ProviderSettlement account) to the InMemory store.
        using var scope = host.Services.CreateScope();
        scope.ServiceProvider.GetRequiredService<WalletDbContext>().Database.EnsureCreated();
        return host;
    }
}
```

`tests/Live.Tests/Api/WalletApiTests.cs`:

```csharp
using System.Net;
using System.Net.Http.Json;
using FluentAssertions;
using Live.Wallet.Wallets;
using Microsoft.AspNetCore.Mvc;

namespace Live.Tests.Api;

public sealed class WalletApiTests : IClassFixture<WalletApiFactory>
{
    private readonly HttpClient _client;

    public WalletApiTests(WalletApiFactory factory)
    {
        _client = factory.CreateClient();
    }

    [Fact]
    public async Task Creating_a_wallet_returns_201_then_200()
    {
        var userId = Guid.NewGuid();

        var created = await _client.PostAsync($"/users/{userId}/wallet", content: null);
        var existing = await _client.PostAsync($"/users/{userId}/wallet", content: null);

        created.StatusCode.Should().Be(HttpStatusCode.Created);
        existing.StatusCode.Should().Be(HttpStatusCode.OK);
        (await ReadAsync<WalletResponse>(existing)).Currency.Should().Be("NGN");
    }

    [Fact]
    public async Task An_unknown_wallet_returns_a_404_problem()
    {
        var response = await _client.GetAsync($"/users/{Guid.NewGuid()}/wallet");

        response.StatusCode.Should().Be(HttpStatusCode.NotFound);
        response.Content.Headers.ContentType?.MediaType.Should().Be("application/problem+json");
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Wallet.NotFound");
    }

    [Fact]
    public async Task A_top_up_confirmed_by_webhook_credits_the_wallet()
    {
        var userId = Guid.NewGuid();

        await FundAsync(userId, 5_000);

        var wallet = await ReadAsync<WalletResponse>(await _client.GetAsync($"/users/{userId}/wallet"));
        wallet.Balance.Should().Be(5_000);
        wallet.AvailableBalance.Should().Be(5_000);
        var history = await ReadAsync<TransactionResponse[]>(await _client.GetAsync($"/users/{userId}/wallet/transactions"));
        history.Should().ContainSingle(t => t.Type == "TopUp" && t.Status == "Succeeded");
    }

    [Fact]
    public async Task A_withdrawal_returns_202_and_an_identical_retry_returns_200()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 10_000);
        var body = Withdrawal("wd-retry", 4_000);

        var first = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", body);
        var retry = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", body);

        first.StatusCode.Should().Be(HttpStatusCode.Accepted);
        retry.StatusCode.Should().Be(HttpStatusCode.OK);
        (await ReadAsync<TransactionResponse>(retry)).Id.Should().Be((await ReadAsync<TransactionResponse>(first)).Id);
        (await ReadAsync<WalletResponse>(await _client.GetAsync($"/users/{userId}/wallet"))).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task A_withdrawal_beyond_the_available_balance_returns_422()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 1_000);

        var response = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-big", 1_001));

        response.StatusCode.Should().Be(HttpStatusCode.UnprocessableEntity);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Wallet.InsufficientFunds");
    }

    [Fact]
    public async Task Reusing_a_reference_with_a_different_body_returns_409()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 10_000);
        await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-reused", 1_000));

        var response = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-reused", 2_000));

        response.StatusCode.Should().Be(HttpStatusCode.Conflict);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Transaction.DuplicateReference");
    }

    [Fact]
    public async Task A_non_positive_amount_returns_400()
    {
        var response = await _client.PostAsJsonAsync($"/users/{Guid.NewGuid()}/top-ups", new { referenceId = "zero", amount = 0 });

        response.StatusCode.Should().Be(HttpStatusCode.BadRequest);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Money.InvalidAmount");
    }

    [Fact]
    public async Task An_unknown_webhook_event_returns_400()
    {
        var response = await _client.PostAsJsonAsync("/webhooks/provider", new { @event = "refund.created", providerReferenceId = "x", amount = 1 });

        response.StatusCode.Should().Be(HttpStatusCode.BadRequest);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Validation.Event");
    }

    [Fact]
    public async Task The_dev_provider_endpoint_is_hidden_outside_development()
    {
        var response = await _client.PostAsync("/dev/provider/chg_anything/complete?outcome=success", content: null);

        response.StatusCode.Should().Be(HttpStatusCode.NotFound);
    }

    private static object Withdrawal(string referenceId, long amount) =>
        new { referenceId, amount, bankCode = "058", accountNumber = "0123456789", accountName = "Ada Lovelace" };

    private async Task FundAsync(Guid userId, long amount)
    {
        var started = await _client.PostAsJsonAsync($"/users/{userId}/top-ups", new { referenceId = $"topup-{Guid.NewGuid():N}", amount });
        started.StatusCode.Should().Be(HttpStatusCode.Created);
        var topUp = await ReadAsync<TransactionResponse>(started);
        var confirmed = await _client.PostAsJsonAsync("/webhooks/provider", new
        {
            @event = "charge.success",
            providerReferenceId = topUp.ProviderReferenceId,
            amount,
        });
        confirmed.StatusCode.Should().Be(HttpStatusCode.OK);
    }

    private static async Task<T> ReadAsync<T>(HttpResponseMessage response) =>
        await response.Content.ReadFromJsonAsync<T>()
        ?? throw new InvalidOperationException($"Expected a {typeof(T).Name} body.");
}
```

`tests/Live.Tests/Api/ProblemResultsTests.cs`:

```csharp
using FluentAssertions;
using Live.Api.Controllers;
using Practice.Common;

namespace Live.Tests.Api;

public sealed class ProblemResultsTests
{
    [Theory]
    [InlineData("Money.InvalidAmount", 400)]
    [InlineData("Money.UnsupportedCurrency", 400)]
    [InlineData("Validation.AccountNumber", 400)]
    [InlineData("Wallet.NotFound", 404)]
    [InlineData("Transaction.NotFound", 404)]
    [InlineData("Transaction.DuplicateReference", 409)]
    [InlineData("Concurrency.Exhausted", 409)]
    [InlineData("Ledger.HoldMissing", 409)]
    [InlineData("Wallet.InsufficientFunds", 422)]
    [InlineData("Something.Unexpected", 500)]
    public void Error_codes_map_to_http_statuses(string code, int expectedStatus)
    {
        ProblemResults.StatusCodeFor(Error.Create(code, "message")).Should().Be(expectedStatus);
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Api"`
Expected: build FAILS with `CS0234: The type or namespace name 'ProblemResults' does not exist in the namespace 'Live.Api.Controllers'`.

- [ ] **Step 3: Wire the API project**

In `src/Live.Api/Live.Api.csproj`, add to the package `ItemGroup`:

```xml
    <PackageReference Include="Aspire.Npgsql.EntityFrameworkCore.PostgreSQL" />
    <PackageReference Include="Aspire.RabbitMQ.Client" />
```

and to the project-reference `ItemGroup`:

```xml
    <ProjectReference Include="..\Live.Wallet\Live.Wallet.csproj" />
```

Replace `src/Live.Api/Program.cs` with:

```csharp
using Live.Wallet;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Scalar.AspNetCore;

var builder = WebApplication.CreateBuilder(args);

builder.AddServiceDefaults();
builder.AddNpgsqlDbContext<WalletDbContext>("appdb");
builder.AddRabbitMQClient("messaging");

builder.Services.AddWallet(builder.Configuration);
builder.Services.AddWalletBackgroundWorkers();
builder.Services.AddControllers();
builder.Services.AddProblemDetails();
builder.Services.AddOpenApi();

var app = builder.Build();

app.MapDefaultEndpoints();

if (app.Environment.IsDevelopment())
{
    app.MapOpenApi();
    app.MapScalarApiReference();

    await using var scope = app.Services.CreateAsyncScope();
    await scope.ServiceProvider.GetRequiredService<WalletDbContext>().Database.MigrateAsync();
}

app.MapControllers();

await app.RunAsync();
```

- [ ] **Step 4: Implement the controllers**

`src/Live.Api/Controllers/ProblemResults.cs`:

```csharp
using Microsoft.AspNetCore.Mvc;
using Practice.Common;

namespace Live.Api.Controllers;

/// <summary>Turns a handler's Error into RFC 7807 ProblemDetails. The title carries the stable error code.</summary>
public static class ProblemResults
{
    public static int StatusCodeFor(Error error) => error.Code switch
    {
        "Money.InvalidAmount" or "Money.UnsupportedCurrency" => StatusCodes.Status400BadRequest,
        "Wallet.NotFound" or "Transaction.NotFound" => StatusCodes.Status404NotFound,
        "Transaction.DuplicateReference" or "Concurrency.Exhausted" => StatusCodes.Status409Conflict,
        "Wallet.InsufficientFunds" => StatusCodes.Status422UnprocessableEntity,
        var code when code.StartsWith("Validation.", StringComparison.Ordinal) => StatusCodes.Status400BadRequest,
        var code when code.StartsWith("Ledger.", StringComparison.Ordinal) => StatusCodes.Status409Conflict,
        _ => StatusCodes.Status500InternalServerError,
    };

    public static ObjectResult ToProblem(this ControllerBase controller, Error error) =>
        controller.Problem(title: error.Code, detail: error.Message, statusCode: StatusCodeFor(error));
}
```

`src/Live.Api/Controllers/WalletsController.cs`:

```csharp
using Live.Wallet.Wallets;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/wallet")]
public sealed class WalletsController : ControllerBase
{
    private readonly CreateWalletHandler _create;
    private readonly GetWalletHandler _get;
    private readonly ListTransactionsHandler _list;

    public WalletsController(CreateWalletHandler create, GetWalletHandler get, ListTransactionsHandler list)
    {
        _create = create;
        _get = get;
        _list = list;
    }

    [HttpPost]
    public async Task<IActionResult> CreateAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _create.HandleAsync(userId, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Created
            ? Created($"/users/{userId}/wallet", result.Value.Wallet)
            : Ok(result.Value.Wallet);
    }

    [HttpGet]
    public async Task<IActionResult> GetAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _get.HandleAsync(userId, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }

    [HttpGet("transactions")]
    public async Task<IActionResult> ListTransactionsAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _list.HandleAsync(userId, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
```

`src/Live.Api/Controllers/TopUpRequest.cs`:

```csharp
namespace Live.Api.Controllers;

public sealed record TopUpRequest
{
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }
}
```

`src/Live.Api/Controllers/TopUpsController.cs`:

```csharp
using Live.Wallet.TopUps;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/top-ups")]
public sealed class TopUpsController : ControllerBase
{
    private readonly InitiateTopUpHandler _initiate;

    public TopUpsController(InitiateTopUpHandler initiate)
    {
        _initiate = initiate;
    }

    [HttpPost]
    public async Task<IActionResult> InitiateAsync(Guid userId, TopUpRequest request, CancellationToken cancellationToken)
    {
        var command = new InitiateTopUpCommand { UserId = userId, ReferenceId = request.ReferenceId, Amount = request.Amount };
        var result = await _initiate.HandleAsync(command, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Replayed
            ? Ok(result.Value.Transaction)
            : Created($"/users/{userId}/wallet/transactions", result.Value.Transaction);
    }
}
```

`src/Live.Api/Controllers/WithdrawalRequest.cs`:

```csharp
namespace Live.Api.Controllers;

public sealed record WithdrawalRequest
{
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
```

`src/Live.Api/Controllers/WithdrawalsController.cs`:

```csharp
using Live.Wallet.Withdrawals;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/withdrawals")]
public sealed class WithdrawalsController : ControllerBase
{
    private readonly RequestWithdrawalHandler _request;

    public WithdrawalsController(RequestWithdrawalHandler request)
    {
        _request = request;
    }

    [HttpPost]
    public async Task<IActionResult> RequestAsync(Guid userId, WithdrawalRequest request, CancellationToken cancellationToken)
    {
        var command = new RequestWithdrawalCommand
        {
            UserId = userId,
            ReferenceId = request.ReferenceId,
            Amount = request.Amount,
            BankCode = request.BankCode,
            AccountNumber = request.AccountNumber,
            AccountName = request.AccountName,
        };
        var result = await _request.HandleAsync(command, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Replayed ? Ok(result.Value.Transaction) : Accepted(result.Value.Transaction);
    }
}
```

`src/Live.Api/Controllers/ProviderWebhooksController.cs`:

```csharp
using Live.Wallet.Webhooks;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("webhooks/provider")]
public sealed class ProviderWebhooksController : ControllerBase
{
    private readonly ProviderWebhookHandler _webhooks;

    public ProviderWebhooksController(ProviderWebhookHandler webhooks)
    {
        _webhooks = webhooks;
    }

    [HttpPost]
    public async Task<IActionResult> ReceiveAsync(ProviderWebhook webhook, CancellationToken cancellationToken)
    {
        var result = await _webhooks.HandleAsync(webhook, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
```

`src/Live.Api/Controllers/DevProviderController.cs`:

```csharp
using Live.Wallet.Providers;
using Live.Wallet.Webhooks;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

/// <summary>Development only: plays the fake provider's webhook so the whole flow can be tried by hand.</summary>
[ApiController]
[Route("dev/provider")]
public sealed class DevProviderController : ControllerBase
{
    private readonly IHostEnvironment _environment;
    private readonly FakePaymentProvider _provider;
    private readonly ProviderWebhookHandler _webhooks;

    public DevProviderController(IHostEnvironment environment, FakePaymentProvider provider, ProviderWebhookHandler webhooks)
    {
        _environment = environment;
        _provider = provider;
        _webhooks = webhooks;
    }

    [HttpPost("{providerReferenceId}/complete")]
    public async Task<IActionResult> CompleteAsync(string providerReferenceId, [FromQuery] string outcome, CancellationToken cancellationToken)
    {
        if (!_environment.IsDevelopment())
        {
            return NotFound();
        }

        var succeeded = outcome switch
        {
            "success" => true,
            "failed" => false,
            _ => (bool?)null,
        };
        if (succeeded is not { } isSuccess)
        {
            return Problem(title: "Validation.Outcome", detail: "outcome must be 'success' or 'failed'.", statusCode: StatusCodes.Status400BadRequest);
        }

        var record = _provider.FindByProviderReference(providerReferenceId);
        if (record is null)
        {
            return NotFound();
        }

        if (record.IsPayout)
        {
            _provider.SetPayoutStatus(record.ReferenceId, isSuccess ? PayoutStatus.Succeeded : PayoutStatus.Failed);
        }

        var webhook = new ProviderWebhook
        {
            Event = (record.IsPayout, isSuccess) switch
            {
                (false, true) => ProviderWebhookEvents.ChargeSuccess,
                (false, false) => ProviderWebhookEvents.ChargeFailed,
                (true, true) => ProviderWebhookEvents.TransferSuccess,
                (true, false) => ProviderWebhookEvents.TransferFailed,
            },
            ProviderReferenceId = providerReferenceId,
            Reference = record.ReferenceId,
            Amount = record.Amount,
        };
        var result = await _webhooks.HandleAsync(webhook, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `dotnet test tests/Live.Tests --filter "FullyQualifiedName~Live.Tests.Api"`
Expected: PASS (19 tests).

If `WebApplicationFactory<Program>` fails to compile because `Program` is inaccessible, add `public partial class Program;` as the last line of `src/Live.Api/Program.cs`. (The .NET 10 SDK normally generates this, in which case don't add it.)

- [ ] **Step 6: Commit**

```bash
git add src/Live.Api tests/Live.Tests/Api
git commit -m "feat(wallet): expose wallet, top-up, withdrawal and webhook endpoints" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```

---

### Task 16: Full verification

**Files:** none new (fixes only, if a step fails)

- [ ] **Step 1: Restore, build, test, and check formatting exactly as AGENTS.md prescribes**

```bash
dotnet restore
dotnet build --no-restore
dotnet test --no-build --verbosity normal
dotnet format --verify-no-changes
```

Expected: the build finishes with 0 warnings and 0 errors; every test passes (the existing `FakeClockTests` plus all wallet tests); format exits 0. If format reports changes, run `dotnet format`, review the diff, and rerun the four commands.

- [ ] **Step 2: Manual end-to-end smoke test (needs Docker for Aspire's Postgres and RabbitMQ)**

Run: `aspire run` from the repo root, and wait until `live-api` is healthy in the dashboard. Then, with `API` set to the `live-api` http endpoint shown in the dashboard and `U=$(uuidgen)`:

```bash
curl -s -X POST $API/users/$U/top-ups -H 'content-type: application/json' -d '{"referenceId":"t1","amount":10000}'
# copy providerReferenceId (chg_...) from the response into CHG
curl -s -X POST "$API/dev/provider/$CHG/complete?outcome=success"
curl -s -X POST $API/users/$U/withdrawals -H 'content-type: application/json' \
  -d '{"referenceId":"w1","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada Lovelace"}'
sleep 3
curl -s $API/users/$U/wallet/transactions
# copy the withdrawal's providerReferenceId (trf_..., set by the payout worker) into TRF
curl -s -X POST "$API/dev/provider/$TRF/complete?outcome=success"
curl -s $API/users/$U/wallet
```

Expected: the final wallet shows `balance: 6000, availableBalance: 6000`. In the RabbitMQ management UI, `wallet.payouts` is empty and `wallet.payouts.dead` is empty. This step is optional if Docker isn't available; say so explicitly in the hand-off.

- [ ] **Step 3: Commit any fixes**

```bash
git status --short
# only if something changed:
git add -A src tests
git commit -m "chore(wallet): fix formatting and verification findings" --trailer "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" --trailer "Claude-Session: https://claude.ai/code/session_01PUVGj2oVpGwsdw2p3cG29b"
```
