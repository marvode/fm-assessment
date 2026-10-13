namespace Live.Wallet.Providers;

/// <summary>What the fake provider remembers about a charge or payout.</summary>
public sealed class FakeProviderRecord
{
    public required bool IsPayout { get; init; }

    public required string ReferenceId { get; init; }

    public required string ProviderReferenceId { get; init; }

    public required long Amount { get; init; }

    public PayoutStatus Status { get; set; } = PayoutStatus.Pending;
}
