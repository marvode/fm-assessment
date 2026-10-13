using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Wallets;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Wallet.Common;

namespace Wallet.Ledger.Withdrawals;

/// <summary>The payout definitely failed: give the held amount back to AvailableBalance. No ledger entries, because no money moved.</summary>
public sealed partial class ReleaseWithdrawalHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;
    private readonly ILogger<ReleaseWithdrawalHandler> _logger;

    public ReleaseWithdrawalHandler(WalletDbContext db, IClock clock, ILogger<ReleaseWithdrawalHandler> logger)
    {
        _db = db;
        _clock = clock;
        _logger = logger;
    }

    public Task<Result<TransactionResponse>> HandleAsync(Guid transactionId, string reason, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await _db.Transactions
                .SingleOrDefaultAsync(t => t.Id == transactionId && t.Type == TransactionType.Withdrawal, ct)
                .ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (transaction.IsFinal)
            {
                return TransactionResponse.From(transaction);
            }

            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var released = wallet.ReleaseHold(transaction.Amount);
            if (released.IsFailure)
            {
                return released.Error;
            }

            transaction.MarkFailed(_clock.UtcNow);
            LogReleased(_logger, transaction.Id, transaction.Amount, reason);
            return TransactionResponse.From(transaction);
        }, cancellationToken);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Released withdrawal {TransactionId} hold of {Amount} kobo: {Reason}")]
    private static partial void LogReleased(ILogger logger, Guid transactionId, long amount, string reason);
}
