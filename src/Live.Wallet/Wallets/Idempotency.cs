using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Wallets;

internal static class Idempotency
{
    /// <summary>
    /// Null when the referenceId is new for this wallet. Otherwise the original transaction when the request matches,
    /// or DuplicateReference when the same referenceId arrives with a different request.
    /// </summary>
    public static async Task<Result<IdempotentResponse>?> FindReplayAsync(
        WalletDbContext db, Guid accountId, string referenceId, string requestHash, CancellationToken cancellationToken)
    {
        var existing = await db.Transactions.AsNoTracking()
            .SingleOrDefaultAsync(t => t.AccountId == accountId && t.ReferenceId == referenceId, cancellationToken)
            .ConfigureAwait(false);
        if (existing is null)
        {
            return null;
        }

        if (existing.RequestHash != requestHash)
        {
            return WalletErrors.DuplicateReference;
        }

        return new IdempotentResponse { Transaction = TransactionResponse.From(existing), Replayed = true };
    }
}
