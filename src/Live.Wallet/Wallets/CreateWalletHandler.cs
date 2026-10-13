using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Wallets;

public sealed class CreateWalletHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public CreateWalletHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public async Task<Result<WalletCreation>> HandleAsync(Guid userId, CancellationToken cancellationToken)
    {
        var (account, created) = await WalletAccounts.GetOrCreateAsync(_db, userId, _clock.UtcNow, cancellationToken).ConfigureAwait(false);
        if (!created)
        {
            return new WalletCreation { Wallet = WalletResponse.From(account), Created = false };
        }

        try
        {
            await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        }
        catch (DbUpdateException exception) when (exception is not DbUpdateConcurrencyException)
        {
            // A concurrent create won the unique (UserId, Currency) index on Postgres; return the winner's wallet.
            _db.ChangeTracker.Clear();
            var winner = await WalletAccounts.FindAsync(_db, userId, cancellationToken).ConfigureAwait(false);
            if (winner is null)
            {
                throw;
            }

            return new WalletCreation { Wallet = WalletResponse.From(winner), Created = false };
        }

        return new WalletCreation { Wallet = WalletResponse.From(account), Created = true };
    }
}
