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
