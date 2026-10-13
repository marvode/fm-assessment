namespace Live.Wallet.Providers;

public sealed record ChargeInitiation
{
    public required string ProviderReferenceId { get; init; }
}
