namespace Live.Wallet.Providers;

public sealed record PayoutInstruction
{
    /// <summary>Our ReferenceId; the provider's idempotency key for this payout.</summary>
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
