using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Diagnostics;

namespace Live.Tests.Wallet;

/// <summary>
/// EF InMemory writes a SaveChanges row by row, so a concurrency conflict can leave earlier rows saved.
/// Postgres rolls the whole save back. Checking account versions before anything is written makes a conflicting save write nothing, as on Postgres.
/// </summary>
internal sealed class AtomicSaveInterceptor : SaveChangesInterceptor
{
    private readonly Func<WalletDbContext> _openStoreView;

    public AtomicSaveInterceptor(Func<WalletDbContext> openStoreView)
    {
        _openStoreView = openStoreView;
    }

    public override async ValueTask<InterceptionResult<int>> SavingChangesAsync(
        DbContextEventData eventData,
        InterceptionResult<int> result,
        CancellationToken cancellationToken = default)
    {
        if (eventData.Context is null)
        {
            return result;
        }

        var modifiedAccounts = eventData.Context.ChangeTracker.Entries<Account>()
            .Where(entry => entry.State == EntityState.Modified)
            .ToList();
        if (modifiedAccounts.Count == 0)
        {
            return result;
        }

        await using var store = _openStoreView();
        foreach (var entry in modifiedAccounts)
        {
            var storedVersion = await store.Accounts.AsNoTracking()
                .Where(account => account.Id == entry.Entity.Id)
                .Select(account => account.Version)
                .SingleAsync(cancellationToken);
            if (storedVersion != entry.Property(account => account.Version).OriginalValue)
            {
                throw new DbUpdateConcurrencyException($"Account {entry.Entity.Id} was modified concurrently.");
            }
        }

        return result;
    }
}
