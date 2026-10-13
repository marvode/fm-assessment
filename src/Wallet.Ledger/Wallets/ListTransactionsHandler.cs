using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Microsoft.EntityFrameworkCore;
using Wallet.Common;

namespace Wallet.Ledger.Wallets;

public sealed class ListTransactionsHandler
{
    private const int PageSize = 100;

    private readonly WalletDbContext _db;

    public ListTransactionsHandler(WalletDbContext db)
    {
        _db = db;
    }

    public async Task<Result<TransactionResponse[]>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var account = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
        if (account is null)
        {
            return WalletErrors.WalletNotFound;
        }

        var transactions = await _db.Transactions.AsNoTracking()
            .Where(t => t.AccountId == account.Id)
            .OrderByDescending(t => t.CreatedAt)
            .ThenByDescending(t => t.Id)
            .Take(PageSize)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        return transactions.Select(TransactionResponse.From).ToArray();
    }
}
