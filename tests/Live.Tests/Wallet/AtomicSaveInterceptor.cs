using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Diagnostics;

namespace Live.Tests.Wallet;

/// <summary>
/// Makes EF InMemory behave like Postgres on save. InMemory writes a SaveChanges row by row, so a conflict can leave
/// earlier rows saved, and it ignores unique indexes. This checks every concurrency token and the unique indexes the
/// handlers rely on before anything is written, so a conflicting save writes nothing, as on Postgres.
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

        await using var store = _openStoreView();
        await CheckConcurrencyTokensAsync(eventData.Context, store, cancellationToken);
        await CheckUniqueIndexesAsync(eventData.Context, store, cancellationToken);
        return result;
    }

    private static async Task CheckConcurrencyTokensAsync(DbContext context, WalletDbContext store, CancellationToken cancellationToken)
    {
        var modified = context.ChangeTracker.Entries().Where(entry => entry.State == EntityState.Modified).ToList();
        foreach (var entry in modified)
        {
            var tokens = entry.Properties.Where(p => p.Metadata.IsConcurrencyToken).ToList();
            if (tokens.Count == 0)
            {
                continue;
            }

            var key = entry.Metadata.FindPrimaryKey()?.Properties.Select(p => entry.Property(p.Name).CurrentValue).ToArray()
                ?? throw new InvalidOperationException($"{entry.Metadata.Name} has no primary key.");
            var stored = await store.FindAsync(entry.Metadata.ClrType, key, cancellationToken);
            if (stored is null || tokens.Any(token => !Equals(store.Entry(stored).Property(token.Metadata.Name).CurrentValue, token.OriginalValue)))
            {
                throw new DbUpdateConcurrencyException($"{entry.Metadata.ClrType.Name} was modified concurrently.");
            }
        }
    }

    private static async Task CheckUniqueIndexesAsync(DbContext context, WalletDbContext store, CancellationToken cancellationToken)
    {
        foreach (var added in context.ChangeTracker.Entries<LedgerTransaction>().Where(e => e.State == EntityState.Added))
        {
            var referenceId = added.Entity.ReferenceId;
            if (await store.Transactions.AnyAsync(t => t.ReferenceId == referenceId, cancellationToken))
            {
                throw new DbUpdateException($"Duplicate key: IX_Transactions_ReferenceId ({referenceId}).");
            }
        }

        foreach (var added in context.ChangeTracker.Entries<Account>().Where(e => e.State == EntityState.Added && e.Entity.UserId != null))
        {
            var userId = added.Entity.UserId;
            if (await store.Accounts.AnyAsync(a => a.UserId == userId, cancellationToken))
            {
                throw new DbUpdateException($"Duplicate key: IX_Accounts_UserId_Currency ({userId}).");
            }
        }
    }
}
