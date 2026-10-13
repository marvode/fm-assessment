namespace Wallet.Ledger.Wallets;

/// <summary>The transaction a command produced. Replayed is true when an earlier identical request already produced it.</summary>
public sealed record IdempotentResponse
{
    public required TransactionResponse Transaction { get; init; }

    public required bool Replayed { get; init; }
}
