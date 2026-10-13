using Wallet.Ledger.Domain;
using Wallet.Ledger.Payouts;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Providers;
using Wallet.Ledger.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;
using Wallet.Common;

namespace Wallet.Ledger.Reconciliation;

/// <summary>
/// Asks the provider about withdrawals still Pending after the threshold, and settles or releases them through the webhook handlers.
/// A payout the provider has never seen stays held and is re-queued: releasing could pay out and refund if a delayed message still submits it.
/// </summary>
public sealed partial class PendingPayoutSweeper
{
    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly SettleWithdrawalHandler _settle;
    private readonly ReleaseWithdrawalHandler _release;
    private readonly IClock _clock;
    private readonly WalletOptions _options;
    private readonly ILogger<PendingPayoutSweeper> _logger;

    public PendingPayoutSweeper(
        WalletDbContext db,
        IPaymentProvider provider,
        SettleWithdrawalHandler settle,
        ReleaseWithdrawalHandler release,
        IClock clock,
        IOptions<WalletOptions> options,
        ILogger<PendingPayoutSweeper> logger)
    {
        _db = db;
        _provider = provider;
        _settle = settle;
        _release = release;
        _clock = clock;
        _options = options.Value;
        _logger = logger;
    }

    public async Task<SweepSummary> SweepAsync(CancellationToken cancellationToken)
    {
        var cutoff = _clock.UtcNow - _options.PendingThreshold;
        var stale = await _db.Transactions.AsNoTracking()
            .Where(t => t.Type == TransactionType.Withdrawal && t.Status == TransactionStatus.Pending && t.CreatedAt <= cutoff)
            .OrderBy(t => t.CreatedAt)
            .Select(t => t.Id)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);

        int settled = 0, released = 0, redriven = 0, stillPending = 0, errors = 0;
        foreach (var transactionId in stale)
        {
            PayoutStatus status;
            try
            {
                status = await _provider.GetPayoutStatusAsync(ProviderKeys.Payout(transactionId), cancellationToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (!cancellationToken.IsCancellationRequested)
            {
                errors++;
                LogStatusQueryFailed(_logger, exception, transactionId);
                continue;
            }

            switch (status)
            {
                case PayoutStatus.Succeeded:
                    var settledResult = await _settle.HandleAsync(transactionId, cancellationToken).ConfigureAwait(false);
                    if (settledResult.IsSuccess) { settled++; } else { errors++; }
                    break;
                case PayoutStatus.Failed:
                    var releasedResult = await _release
                        .HandleAsync(transactionId, "Provider reported the payout failed during reconciliation.", cancellationToken)
                        .ConfigureAwait(false);
                    if (releasedResult.IsSuccess) { released++; } else { errors++; }
                    break;
                case PayoutStatus.NotFound:
                    // The provider never received it (e.g. the message was dead-lettered). Keep the hold and queue the
                    // request again: the payout key is idempotent and the processor skips final withdrawals.
                    _db.OutboxMessages.Add(PayoutRequested.ToOutboxMessage(transactionId, _clock.UtcNow));
                    await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
                    LogPayoutRedriven(_logger, transactionId);
                    redriven++;
                    break;
                default:
                    stillPending++;
                    break;
            }
        }

        return new SweepSummary { Settled = settled, Released = released, Redriven = redriven, StillPending = stillPending, Errors = errors };
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Querying the provider for withdrawal {TransactionId} failed")]
    private static partial void LogStatusQueryFailed(ILogger logger, Exception exception, Guid transactionId);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Withdrawal {TransactionId} is past the pending threshold but unknown to the provider; keeping the hold and re-queueing the payout")]
    private static partial void LogPayoutRedriven(ILogger logger, Guid transactionId);
}
