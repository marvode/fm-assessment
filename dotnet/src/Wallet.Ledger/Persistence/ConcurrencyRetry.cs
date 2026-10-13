using Wallet.Ledger.Domain;
using Microsoft.EntityFrameworkCore;
using Wallet.Common;

namespace Wallet.Ledger.Persistence;

/// <summary>
/// Runs load → change → save. If another writer changed a tracked row first (a stale concurrency token) or committed the
/// same unique key (e.g. an identical retry), the operation reruns from a clean change tracker and sees the latest state.
/// </summary>
public static class ConcurrencyRetry
{
    public const int MaxAttempts = 3;

    public static async Task<Result<T>> ExecuteAsync<T>(
        WalletDbContext db,
        Func<CancellationToken, Task<Result<T>>> operation,
        CancellationToken cancellationToken)
    {
        for (var attempt = 1; ; attempt++)
        {
            var result = await operation(cancellationToken).ConfigureAwait(false);
            if (result.IsFailure)
            {
                db.ChangeTracker.Clear();
                return result;
            }

            try
            {
                await db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
                return result;
            }
            catch (DbUpdateException exception) when (exception is DbUpdateConcurrencyException || DbErrors.IsUniqueViolation(exception))
            {
                db.ChangeTracker.Clear();
                if (attempt == MaxAttempts)
                {
                    return WalletErrors.ConcurrencyExhausted;
                }
            }
        }
    }
}
