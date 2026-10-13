namespace Live.Wallet.Ledger;

/// <summary>Credit raises an account's balance, Debit lowers it, for every account type.</summary>
public enum EntryDirection
{
    Debit,
    Credit,
}
