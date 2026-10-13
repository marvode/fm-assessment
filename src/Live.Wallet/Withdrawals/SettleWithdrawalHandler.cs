using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Microsoft.EntityFrameworkCore;
using Practice.Common;

namespace Live.Wallet.Withdrawals;

/// <summary>The provider paid out: post the held amount from the wallet to settlement. Called by the webhook and the sweeper.</summary>
public sealed class SettleWithdrawalHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public SettleWithdrawalHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public Task<Result<TransactionResponse>> HandleAsync(Guid transactionId, CancellationToken cancellationToken) =>
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

            var now = _clock.UtcNow;
            var wallet = await _db.Accounts.SingleAsync(a => a.Id == transaction.AccountId, ct).ConfigureAwait(false);
            var settlement = await _db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId, ct).ConfigureAwait(false);
            var posting = LedgerPosting.Post(transaction, wallet, settlement, debitFromHold: true, now);
            if (posting.IsFailure)
            {
                return posting.Error;
            }

            _db.LedgerEntries.AddRange(posting.Value);
            transaction.MarkSucceeded(now);
            return TransactionResponse.From(transaction);
        }, cancellationToken);
}
