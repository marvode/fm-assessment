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
