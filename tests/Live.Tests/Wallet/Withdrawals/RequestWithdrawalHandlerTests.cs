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

    [Theory]
    [InlineData("ReferenceId")]
    [InlineData("BankCode")]
    [InlineData("AccountName")]
    public async Task Oversized_fields_are_rejected(string field)
    {
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();
        var command = TestCommands.Withdrawal(_userId, 100);
        command = field switch
        {
            "ReferenceId" => command with { ReferenceId = new string('r', 129) },
            "BankCode" => command with { BankCode = new string('1', 17) },
            _ => command with { AccountName = new string('a', 129) },
        };

        var result = await Handler(db).HandleAsync(command, CancellationToken.None);

        result.Error.Code.Should().Be($"Validation.{field}");
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
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

    [Fact]
    public async Task Reference_ids_are_scoped_to_each_users_wallet()
    {
        var otherUserId = Guid.NewGuid();
        await _testDb.SeedWalletAsync(_userId, 10_000, TestDb.Start);
        await _testDb.SeedWalletAsync(otherUserId, 10_000, TestDb.Start);
        await using var db = _testDb.CreateContext();

        var mine = await Handler(db).HandleAsync(TestCommands.Withdrawal(_userId, 4_000), CancellationToken.None);
        var theirs = await Handler(db).HandleAsync(TestCommands.Withdrawal(otherUserId, 4_000), CancellationToken.None);

        theirs.Value.Replayed.Should().BeFalse();
        theirs.Value.Transaction.Id.Should().NotBe(mine.Value.Transaction.Id);
        (await _testDb.GetWalletAsync(otherUserId)).AvailableBalance.Should().Be(6_000);
    }

    private RequestWithdrawalHandler Handler(WalletDbContext db) => new(db, _clock);
}
