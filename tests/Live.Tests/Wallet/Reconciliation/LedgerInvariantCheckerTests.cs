using FluentAssertions;
using Live.Wallet.Ledger;
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
