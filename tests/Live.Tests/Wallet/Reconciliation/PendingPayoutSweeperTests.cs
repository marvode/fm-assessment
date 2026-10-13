using FluentAssertions;
using Live.Wallet;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Reconciliation;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
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
        _provider.SetPayoutStatus(ProviderKeys.Payout(transactionId), PayoutStatus.Succeeded);
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
        var transactionId = await SubmittedWithdrawalAsync();
        _provider.SetPayoutStatus(ProviderKeys.Payout(transactionId), PayoutStatus.Failed);
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
        _provider.SetPayoutStatus(ProviderKeys.Payout(transactionId), PayoutStatus.Succeeded);
        _clock.Advance(TimeSpan.FromMinutes(10));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.Should().BeEquivalentTo(new SweepSummary { Settled = 0, Released = 0, Redriven = 0, StillPending = 0, Errors = 0 });
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeFalse();
    }

    [Fact]
    public async Task Keeps_a_payout_the_provider_has_never_seen_on_hold_and_requeues_it()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _clock.Advance(TimeSpan.FromMinutes(16));
        await using var db = _testDb.CreateContext();

        var summary = await Sweeper(db).SweepAsync(CancellationToken.None);

        summary.Redriven.Should().Be(1);
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeFalse();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
        var payoutRequests = await _testDb.QueryAsync(q => q.OutboxMessages.Where(m => m.Type == PayoutRequested.MessageType).ToListAsync());
        payoutRequests.Should().HaveCount(2, "the original request plus the sweeper's re-drive");
        payoutRequests.Should().OnlyContain(m => m.Payload.Contains(transactionId.ToString()));
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
