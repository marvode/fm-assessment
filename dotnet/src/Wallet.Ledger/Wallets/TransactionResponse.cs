using Wallet.Ledger.Domain;

namespace Wallet.Ledger.Wallets;

public sealed record TransactionResponse
{
    public required Guid Id { get; init; }

    public required string Type { get; init; }

    public required string Status { get; init; }

    public required long Amount { get; init; }

    public required string Currency { get; init; }

    public required string ReferenceId { get; init; }

    public string? ProviderReferenceId { get; init; }

    public required DateTimeOffset CreatedAt { get; init; }

    public DateTimeOffset? CompletedAt { get; init; }

    public static TransactionResponse From(LedgerTransaction transaction) =>
        new()
        {
            Id = transaction.Id,
            Type = transaction.Type.ToString(),
            Status = transaction.Status.ToString(),
            Amount = transaction.Amount,
            Currency = WalletCurrency.Code,
            ReferenceId = transaction.ReferenceId,
            ProviderReferenceId = transaction.ProviderReferenceId,
            CreatedAt = transaction.CreatedAt,
            CompletedAt = transaction.CompletedAt,
        };
}
