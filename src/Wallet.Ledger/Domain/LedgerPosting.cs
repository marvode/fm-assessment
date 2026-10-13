using Wallet.Common;

namespace Wallet.Ledger.Domain;

/// <summary>Double-entry posting: every settled transaction is exactly one Debit and one Credit of the same amount.</summary>
public static class LedgerPosting
{
    /// <summary>
    /// Moves the transaction amount from debitAccount to creditAccount. Every check runs before any account changes,
    /// so a failure leaves both accounts untouched. debitFromHold means the amount was already held on debitAccount.
    /// </summary>
    public static Result<LedgerEntry[]> Post(
        LedgerTransaction transaction,
        Account debitAccount,
        Account creditAccount,
        bool debitFromHold,
        DateTimeOffset now)
    {
        var amount = transaction.Amount;
        if (amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (debitAccount.Currency != WalletCurrency.Code || creditAccount.Currency != WalletCurrency.Code)
        {
            return WalletErrors.UnsupportedCurrency;
        }

        if (debitAccount.Id == creditAccount.Id)
        {
            return WalletErrors.SameAccount;
        }

        var canDebit = debitAccount.CanDebit(amount, debitFromHold);
        if (canDebit.IsFailure)
        {
            return canDebit.Error;
        }

        LedgerEntry[] entries =
        [
            debitAccount.Debit(transaction.Id, amount, debitFromHold, now),
            creditAccount.Credit(transaction.Id, amount, now),
        ];
        return entries;
    }
}
