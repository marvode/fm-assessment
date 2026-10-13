namespace Live.Wallet.Ledger;

/// <summary>One side of a posting. Append-only: entries are never updated or deleted.</summary>
public sealed class LedgerEntry
{
    private LedgerEntry()
    {
        Currency = WalletCurrency.Code;
    }

    public Guid Id { get; private set; }

    public Guid TransactionId { get; private set; }

    public Guid AccountId { get; private set; }

    public EntryDirection Direction { get; private set; }

    public long Amount { get; private set; }

    public string Currency { get; private set; }

    public long BalanceBefore { get; private set; }

    public long BalanceAfter { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    internal static LedgerEntry Create(
        Guid transactionId,
        Guid accountId,
        EntryDirection direction,
        long amount,
        string currency,
        long balanceBefore,
        long balanceAfter,
        DateTimeOffset now) =>
        new()
        {
            Id = Guid.CreateVersion7(now),
            TransactionId = transactionId,
            AccountId = accountId,
            Direction = direction,
            Amount = amount,
            Currency = currency,
            BalanceBefore = balanceBefore,
            BalanceAfter = balanceAfter,
            CreatedAt = now,
        };
}
