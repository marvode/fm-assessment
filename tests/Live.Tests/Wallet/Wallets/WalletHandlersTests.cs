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
