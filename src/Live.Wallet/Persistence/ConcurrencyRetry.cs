using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Persistence;

/// <summary>
/// Runs load → change → save. If another writer changed a tracked account first, the save fails on its Version token.
/// The operation then reruns from a clean change tracker, so balance checks see the latest state.
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
            catch (DbUpdateConcurrencyException)
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
