namespace Live.Wallet.Payouts;

/// <summary>Outbox event asking the payout worker to send a held withdrawal to the provider.</summary>
public sealed record PayoutRequested
{
    public const string MessageType = "wallet.payout-requested";

    public required Guid TransactionId { get; init; }
}
