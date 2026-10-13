using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Practice.Common;

namespace Live.Wallet.TopUps;

/// <summary>Applies the provider's verdict on a charge. Repeat deliveries for a final top-up change nothing.</summary>
public sealed partial class CompleteTopUpHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;
    private readonly ILogger<CompleteTopUpHandler> _logger;

    public CompleteTopUpHandler(WalletDbContext db, IClock clock, ILogger<CompleteTopUpHandler> logger)
    {
        _db = db;
        _clock = clock;
        _logger = logger;
    }

    public Task<Result<TransactionResponse>> SucceedAsync(string providerReferenceId, long amount, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await FindAsync(providerReferenceId, ct).ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (transaction.IsFinal)
            {
                return TransactionResponse.From(transaction);
            }

            var now = _clock.UtcNow;
            if (transaction.Amount != amount)
            {
                LogAmountMismatch(_logger, transaction.Id, transaction.Amount, amount);
                transaction.MarkFailed(now);
                return TransactionResponse.From(transaction);
            }

            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var settlement = await _db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId, ct).ConfigureAwait(false);
            var posting = LedgerPosting.Post(transaction, settlement, wallet, debitFromHold: false, now);
            if (posting.IsFailure)
            {
                return posting.Error;
            }

            _db.LedgerEntries.AddRange(posting.Value);
            transaction.MarkSucceeded(now);
            return TransactionResponse.From(transaction);
        }, cancellationToken);

    public Task<Result<TransactionResponse>> FailAsync(string providerReferenceId, CancellationToken cancellationToken) =>
        ConcurrencyRetry.ExecuteAsync<TransactionResponse>(_db, async ct =>
        {
            var transaction = await FindAsync(providerReferenceId, ct).ConfigureAwait(false);
            if (transaction is null)
            {
                return WalletErrors.TransactionNotFound;
            }

            if (!transaction.IsFinal)
            {
                transaction.MarkFailed(_clock.UtcNow);
            }

            return TransactionResponse.From(transaction);
        }, cancellationToken);

    private Task<LedgerTransaction?> FindAsync(string providerReferenceId, CancellationToken cancellationToken) =>
        _db.Transactions.SingleOrDefaultAsync(
            t => t.ProviderReferenceId == providerReferenceId && t.Type == TransactionType.TopUp, cancellationToken);

    [LoggerMessage(Level = LogLevel.Error, Message = "Top-up {TransactionId} expected {ExpectedAmount} kobo but the provider reported {ReportedAmount}; marking it failed")]
    private static partial void LogAmountMismatch(ILogger logger, Guid transactionId, long expectedAmount, long reportedAmount);
}
