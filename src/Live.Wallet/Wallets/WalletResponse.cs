using Live.Wallet.Ledger;

namespace Live.Wallet.Wallets;

public sealed record WalletResponse
{
    public required Guid AccountId { get; init; }

    public required Guid UserId { get; init; }

    public required long Balance { get; init; }

    public required long AvailableBalance { get; init; }

    public required string Currency { get; init; }

    public static WalletResponse From(Account account) =>
        new()
        {
            AccountId = account.Id,
            UserId = account.UserId.GetValueOrDefault(),
            Balance = account.Balance,
            AvailableBalance = account.AvailableBalance,
            Currency = account.Currency,
        };
}
