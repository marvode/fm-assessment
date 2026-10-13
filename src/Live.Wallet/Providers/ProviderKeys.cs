namespace Live.Wallet.Providers;

/// <summary>
/// The idempotency keys we send the provider. Client referenceIds are only unique per wallet, so they are never sent raw:
/// a charge key adds the wallet id, a payout key is the withdrawal's transaction id. Both are stable across retries.
/// </summary>
public static class ProviderKeys
{
    public static string Charge(Guid accountId, string referenceId) => $"{accountId:N}-{referenceId}";

    public static string Payout(Guid transactionId) => transactionId.ToString("N");
}
