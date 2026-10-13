using Live.Wallet.Ledger;
using Live.Wallet.Payouts;
using Live.Wallet.Persistence;
using Live.Wallet.Wallets;
using Practice.Common;

namespace Live.Wallet.Withdrawals;

/// <summary>
/// Holds the funds and records the withdrawal, its bank details and a PayoutRequested outbox event in one SaveChanges,
/// so the payout is queued exactly when the hold commits.
/// </summary>
public sealed class RequestWithdrawalHandler
{
    private readonly WalletDbContext _db;
    private readonly IClock _clock;

    public RequestWithdrawalHandler(WalletDbContext db, IClock clock)
    {
        _db = db;
        _clock = clock;
    }

    public async Task<Result<IdempotentResponse>> HandleAsync(RequestWithdrawalCommand command, CancellationToken cancellationToken)
    {
        if (Validate(command) is { } invalid)
        {
            return invalid;
        }

        var requestHash = RequestHash.Compute(
            nameof(TransactionType.Withdrawal), command.UserId, command.Amount, command.BankCode, command.AccountNumber, command.AccountName);

        return await ConcurrencyRetry.ExecuteAsync<IdempotentResponse>(_db, async ct =>
        {
            var wallet = await WalletAccounts.FindAsync(_db, command.UserId, ct).ConfigureAwait(false);
            if (wallet is null)
            {
                return WalletErrors.WalletNotFound;
            }

            // Checked inside the retry: a racing identical request may have committed since the last attempt.
            var replay = await Idempotency.FindReplayAsync(_db, wallet.Id, command.ReferenceId, requestHash, ct).ConfigureAwait(false);
            if (replay is not null)
            {
                return replay;
            }

            var hold = wallet.PlaceHold(command.Amount);
            if (hold.IsFailure)
            {
                return hold.Error;
            }

            var now = _clock.UtcNow;
            var transaction = LedgerTransaction.StartWithdrawal(wallet.Id, command.Amount, command.ReferenceId, requestHash, now);
            _db.Transactions.Add(transaction);
            _db.Withdrawals.Add(WithdrawalDetails.Create(transaction.Id, command.BankCode, command.AccountNumber, command.AccountName));
            _db.OutboxMessages.Add(PayoutRequested.ToOutboxMessage(transaction.Id, now));
            return new IdempotentResponse { Transaction = TransactionResponse.From(transaction), Replayed = false };
        }, cancellationToken).ConfigureAwait(false);
    }

    private static Error? Validate(RequestWithdrawalCommand command)
    {
        if (command.Amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (string.IsNullOrWhiteSpace(command.ReferenceId) || command.ReferenceId.Length > FieldLimits.ReferenceIdMaxLength)
        {
            return WalletErrors.Validation("ReferenceId", $"ReferenceId is required and at most {FieldLimits.ReferenceIdMaxLength} characters.");
        }

        if (string.IsNullOrWhiteSpace(command.BankCode) || command.BankCode.Length > FieldLimits.BankCodeMaxLength)
        {
            return WalletErrors.Validation("BankCode", $"BankCode is required and at most {FieldLimits.BankCodeMaxLength} characters.");
        }

        if (command.AccountNumber.Length != FieldLimits.AccountNumberLength || !command.AccountNumber.All(char.IsAsciiDigit))
        {
            return WalletErrors.Validation("AccountNumber", "AccountNumber must be a 10-digit NUBAN.");
        }

        if (string.IsNullOrWhiteSpace(command.AccountName) || command.AccountName.Length > FieldLimits.AccountNameMaxLength)
        {
            return WalletErrors.Validation("AccountName", $"AccountName is required and at most {FieldLimits.AccountNameMaxLength} characters.");
        }

        return null;
    }
}
