using FluentAssertions;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Providers;
using Wallet.Ledger.TopUps;
using Wallet.Ledger.Webhooks;
using Wallet.Ledger.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging.Abstractions;
using Wallet.Common;

namespace Wallet.Tests.Ledger.Webhooks;

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
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        var webhook = Webhook(ProviderWebhookEvents.TransferSuccess, "trf_not_yet_stored", 4_000) with { Reference = ProviderKeys.Payout(transactionId) };

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

        var result = await Webhooks(db).HandleAsync(Webhook(ProviderWebhookEvents.TransferSuccess, "trf_missing", 1) with { Reference = "not-a-guid" }, CancellationToken.None);

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
