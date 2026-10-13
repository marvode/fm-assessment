using FluentAssertions;
using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Microsoft.EntityFrameworkCore;
using Wallet.Common;

namespace Wallet.Tests.Ledger.Persistence;

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
