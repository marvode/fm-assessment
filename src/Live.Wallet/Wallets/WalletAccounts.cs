using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;

namespace Live.Wallet.Wallets;

internal static class WalletAccounts
{
    public static Task<Account?> FindAsync(WalletDbContext db, Guid userId, CancellationToken cancellationToken) =>
        db.Accounts.SingleOrDefaultAsync(a => a.UserId == userId && a.Type == AccountType.User, cancellationToken);

    /// <summary>Returns the user's wallet, adding a new one to the context (not saved) when none exists.</summary>
    public static async Task<(Account Account, bool Created)> GetOrCreateAsync(
        WalletDbContext db, Guid userId, DateTimeOffset now, CancellationToken cancellationToken)
    {
        var existing = await FindAsync(db, userId, cancellationToken).ConfigureAwait(false);
        if (existing is not null)
        {
            return (existing, false);
        }

        var account = Account.OpenUserWallet(userId, now);
        db.Accounts.Add(account);
        return (account, true);
    }
}
