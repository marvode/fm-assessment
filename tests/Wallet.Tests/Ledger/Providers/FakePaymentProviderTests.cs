using FluentAssertions;
using Wallet.Ledger.Providers;

namespace Wallet.Tests.Ledger.Providers;

public sealed class FakePaymentProviderTests
{
    private readonly FakePaymentProvider _provider = new();

    [Fact]
    public async Task A_charge_is_idempotent_per_reference()
    {
        var first = await _provider.InitiateChargeAsync("topup-1", 5_000, CancellationToken.None);
        var second = await _provider.InitiateChargeAsync("topup-1", 5_000, CancellationToken.None);

        first.ProviderReferenceId.Should().StartWith("chg_");
        second.ProviderReferenceId.Should().Be(first.ProviderReferenceId);
        var record = _provider.FindByProviderReference(first.ProviderReferenceId);
        record.Should().NotBeNull();
        record?.IsPayout.Should().BeFalse();
        record?.Amount.Should().Be(5_000);
        record?.ReferenceId.Should().Be("topup-1");
    }

    [Fact]
    public async Task A_payout_is_accepted_by_default_and_idempotent_per_reference()
    {
        var first = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);
        var second = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        first.Outcome.Should().Be(PayoutOutcome.Accepted);
        first.ProviderReferenceId.Should().StartWith("trf_");
        second.ProviderReferenceId.Should().Be(first.ProviderReferenceId);
        _provider.PayoutAttempts.Should().Be(2);
        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.Pending);
    }

    [Fact]
    public async Task A_scripted_rejection_is_returned_and_not_recorded()
    {
        _provider.PayoutBehaviour = _ => PayoutInitiation.Rejected("Invalid account");

        var result = await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        result.Outcome.Should().Be(PayoutOutcome.Rejected);
        result.FailureReason.Should().Be("Invalid account");
        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.NotFound);
    }

    [Fact]
    public async Task SetPayoutStatus_changes_the_reported_status()
    {
        await _provider.InitiatePayoutAsync(Instruction("wd-1"), CancellationToken.None);

        _provider.SetPayoutStatus("wd-1", PayoutStatus.Succeeded);

        (await _provider.GetPayoutStatusAsync("wd-1", CancellationToken.None)).Should().Be(PayoutStatus.Succeeded);
    }

    [Fact]
    public async Task An_unknown_payout_is_not_found()
    {
        (await _provider.GetPayoutStatusAsync("missing", CancellationToken.None)).Should().Be(PayoutStatus.NotFound);
    }

    private static PayoutInstruction Instruction(string referenceId) =>
        new() { ReferenceId = referenceId, Amount = 4_000, BankCode = "058", AccountNumber = "0123456789", AccountName = "Ada Lovelace" };
}
