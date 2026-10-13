using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Wallet.Common;

namespace Wallet.Ledger.Wallets;

public sealed class GetWalletHandler
{
    private readonly WalletDbContext _db;

    public GetWalletHandler(WalletDbContext db)
    {
        _db = db;
    }

    public async Task<Result<WalletResponse>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var account = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
        if (account is null)
        {
            return WalletErrors.WalletNotFound;
        }

        return WalletResponse.From(account);
    }
}
