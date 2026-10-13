namespace Live.Wallet.Wallets;

public sealed record WalletCreation
{
    public required WalletResponse Wallet { get; init; }

    public required bool Created { get; init; }
}
