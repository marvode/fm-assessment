using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Practice.Common;

namespace Live.Wallet.Wallets;

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
