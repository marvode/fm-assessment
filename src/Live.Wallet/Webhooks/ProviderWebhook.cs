namespace Live.Wallet.Webhooks;

public sealed record ProviderWebhook
{
    public required string Event { get; init; }

    public required string ProviderReferenceId { get; init; }

    /// <summary>
    /// The key we gave the provider, echoed back (for a payout, ProviderKeys.Payout: the transaction id).
    /// Lets a transfer webhook match before the payout worker has stored the provider reference.
    /// </summary>
    public string? Reference { get; init; }

    public long Amount { get; init; }
}
