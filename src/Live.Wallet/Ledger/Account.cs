using Practice.Common;

namespace Live.Wallet.Ledger;

/// <summary>
/// A balance holder. Balance is settled money and always equals the sum of the account's ledger entries.
/// AvailableBalance is Balance minus active withdrawal holds. Version is the optimistic-concurrency token.
/// </summary>
public sealed class Account
{
    public const string ProviderSettlementName = "ProviderSettlement";

    public static readonly Guid ProviderSettlementId = new("00000000-0000-0000-0000-000000000001");

    private Account()
    {
        Currency = WalletCurrency.Code;
    }

    public Guid Id { get; private set; }

    public Guid? UserId { get; private set; }

    public AccountType Type { get; private set; }

    public string? Name { get; private set; }

    public string Currency { get; private set; }

    public long Balance { get; private set; }

    public long AvailableBalance { get; private set; }

    public int Version { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    public static Account OpenUserWallet(Guid userId, DateTimeOffset now) =>
        new() { Id = Guid.CreateVersion7(now), UserId = userId, Type = AccountType.User, CreatedAt = now };

    public static Account OpenSystem(Guid id, string name, DateTimeOffset now) =>
        new() { Id = id, Name = name, Type = AccountType.System, CreatedAt = now };

    public Result<long> PlaceHold(long amount)
    {
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (AvailableBalance < amount)
        {
            return WalletErrors.InsufficientFunds;
        }

        AvailableBalance -= amount;
        Version++;
        return AvailableBalance;
    }

    public Result<long> ReleaseHold(long amount)
    {
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (AvailableBalance + amount > Balance)
        {
            return WalletErrors.HoldMissing;
        }

        AvailableBalance += amount;
        Version++;
        return AvailableBalance;
    }

    /// <summary>System accounts may go negative. A user wallet needs available funds, or, when fromHold is true, a hold covering the amount.</summary>
    internal Result<long> CanDebit(long amount, bool fromHold)
    {
        if (Type == AccountType.System)
        {
            return amount;
        }

        if (fromHold)
        {
            return Balance - amount >= AvailableBalance ? amount : WalletErrors.HoldMissing;
        }

        return AvailableBalance >= amount ? amount : WalletErrors.InsufficientFunds;
    }

    internal LedgerEntry Credit(Guid transactionId, long amount, DateTimeOffset now)
    {
        var before = Balance;
        Balance += amount;
        AvailableBalance += amount;
        Version++;
        return LedgerEntry.Create(transactionId, Id, EntryDirection.Credit, amount, Currency, before, Balance, now);
    }

    /// <summary>When fromHold is true, AvailableBalance already dropped when the hold was placed, so only Balance moves.</summary>
    internal LedgerEntry Debit(Guid transactionId, long amount, bool fromHold, DateTimeOffset now)
    {
        var before = Balance;
        Balance -= amount;
        if (!fromHold)
        {
            AvailableBalance -= amount;
        }

        Version++;
        return LedgerEntry.Create(transactionId, Id, EntryDirection.Debit, amount, Currency, before, Balance, now);
    }
}
