namespace Wallet.Ledger.Providers;

public sealed record PayoutInitiation
{
    public required PayoutOutcome Outcome { get; init; }

    public string? ProviderReferenceId { get; init; }

    public string? FailureReason { get; init; }

    public static PayoutInitiation Accepted(string providerReferenceId) =>
        new() { Outcome = PayoutOutcome.Accepted, ProviderReferenceId = providerReferenceId };

    public static PayoutInitiation Rejected(string reason) =>
        new() { Outcome = PayoutOutcome.Rejected, FailureReason = reason };
}
