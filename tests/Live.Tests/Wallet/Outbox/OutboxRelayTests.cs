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
    public async Task A_publish_timeout_is_recorded_rather_than_treated_as_shutdown()
    {
        var timingOut = await AddMessageAsync();
        await AddMessageAsync();
        _publisher.TimeOutWhen = id => id == timingOut;
        await using var db = _testDb.CreateContext();

        (await Relay(db).RelayPendingAsync(CancellationToken.None)).Should().Be(1);

        var failed = await _testDb.QueryAsync(q => q.OutboxMessages.SingleAsync(m => m.Id == timingOut));
        failed.ProcessedAt.Should().BeNull();
        failed.Attempts.Should().Be(1);
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
