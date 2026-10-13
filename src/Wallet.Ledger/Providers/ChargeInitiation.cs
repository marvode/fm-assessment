namespace Wallet.Ledger.Providers;

public sealed record ChargeInitiation
{
    public required string ProviderReferenceId { get; init; }
}
