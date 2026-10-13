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
