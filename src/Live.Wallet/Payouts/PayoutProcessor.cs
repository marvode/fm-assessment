using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Practice.Common;

namespace Live.Wallet.Payouts;

/// <summary>
/// Sends one held withdrawal to the provider. Acceptance only records the provider reference: the webhook or the sweeper settles it later.
/// A definite rejection releases the hold. Transient faults throw so the message is redelivered.
/// </summary>
public sealed partial class PayoutProcessor
{
    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly ReleaseWithdrawalHandler _release;
    private readonly ILogger<PayoutProcessor> _logger;

    public PayoutProcessor(
        WalletDbContext db,
        IPaymentProvider provider,
        ReleaseWithdrawalHandler release,
        ILogger<PayoutProcessor> logger)
    {
        _db = db;
        _provider = provider;
        _release = release;
        _logger = logger;
    }

    public async Task<Result<PayoutProcessingOutcome>> ProcessAsync(PayoutRequested message, CancellationToken cancellationToken)
    {
        var transaction = await _db.Transactions
            .SingleOrDefaultAsync(t => t.Id == message.TransactionId && t.Type == TransactionType.Withdrawal, cancellationToken)
            .ConfigureAwait(false);
        if (transaction is null)
        {
            LogUnknownTransaction(_logger, message.TransactionId);
            return PayoutProcessingOutcome.Skipped;
        }

        // A provider reference means an earlier delivery already got this payout accepted.
        if (transaction.IsFinal || transaction.ProviderReferenceId is not null)
        {
            return PayoutProcessingOutcome.Skipped;
        }

        var details = await _db.Withdrawals.AsNoTracking()
            .SingleAsync(w => w.TransactionId == transaction.Id, cancellationToken)
            .ConfigureAwait(false);
        var instruction = new PayoutInstruction
        {
            ReferenceId = ProviderKeys.Payout(transaction.Id),
            Amount = transaction.Amount,
            BankCode = details.BankCode,
            AccountNumber = details.AccountNumber,
            AccountName = details.AccountName,
        };
        var initiation = await _provider.InitiatePayoutAsync(instruction, cancellationToken).ConfigureAwait(false);

        switch (initiation)
        {
            case { Outcome: PayoutOutcome.Accepted, ProviderReferenceId: { } providerReferenceId }:
                transaction.AssignProviderReference(providerReferenceId);
                await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
                return PayoutProcessingOutcome.Initiated;
            case { Outcome: PayoutOutcome.Rejected }:
                break;
            default:
                // Ambiguous: the provider may have paid. Releasing could double-spend, so fail the delivery and let it retry.
                throw new InvalidOperationException($"Provider returned an unusable payout result ({initiation.Outcome}, no reference) for withdrawal {transaction.Id}.");
        }

        var released = await _release
            .HandleAsync(transaction.Id, initiation.FailureReason ?? "Provider rejected the payout.", cancellationToken)
            .ConfigureAwait(false);
        if (released.IsFailure)
        {
            return released.Error;
        }

        return PayoutProcessingOutcome.Rejected;
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout requested for unknown withdrawal {TransactionId}; skipping")]
    private static partial void LogUnknownTransaction(ILogger logger, Guid transactionId);
}
