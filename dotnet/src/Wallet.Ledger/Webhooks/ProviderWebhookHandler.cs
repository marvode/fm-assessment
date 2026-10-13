using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.TopUps;
using Wallet.Ledger.Wallets;
using Wallet.Ledger.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Wallet.Common;

namespace Wallet.Ledger.Webhooks;

/// <summary>Routes a provider callback to the top-up or withdrawal handler. Every route is idempotent.</summary>
public sealed class ProviderWebhookHandler
{
    private readonly WalletDbContext _db;
    private readonly CompleteTopUpHandler _topUps;
    private readonly SettleWithdrawalHandler _settle;
    private readonly ReleaseWithdrawalHandler _release;

    public ProviderWebhookHandler(
        WalletDbContext db,
        CompleteTopUpHandler topUps,
        SettleWithdrawalHandler settle,
        ReleaseWithdrawalHandler release)
    {
        _db = db;
        _topUps = topUps;
        _settle = settle;
        _release = release;
    }

    public async Task<Result<TransactionResponse>> HandleAsync(ProviderWebhook webhook, CancellationToken cancellationToken)
    {
        switch (webhook.Event)
        {
            case ProviderWebhookEvents.ChargeSuccess:
                return await _topUps.SucceedAsync(webhook.ProviderReferenceId, webhook.Amount, cancellationToken).ConfigureAwait(false);
            case ProviderWebhookEvents.ChargeFailed:
                return await _topUps.FailAsync(webhook.ProviderReferenceId, cancellationToken).ConfigureAwait(false);
            case ProviderWebhookEvents.TransferSuccess:
            case ProviderWebhookEvents.TransferFailed:
                var transactionId = await FindWithdrawalIdAsync(webhook, cancellationToken).ConfigureAwait(false);
                if (transactionId is not { } id)
                {
                    return WalletErrors.TransactionNotFound;
                }

                return webhook.Event == ProviderWebhookEvents.TransferSuccess
                    ? await _settle.HandleAsync(id, cancellationToken).ConfigureAwait(false)
                    : await _release.HandleAsync(id, "Provider reported transfer.failed.", cancellationToken).ConfigureAwait(false);
            default:
                return WalletErrors.UnknownWebhookEvent;
        }
    }

    private Task<Guid?> FindWithdrawalIdAsync(ProviderWebhook webhook, CancellationToken cancellationToken)
    {
        // The payout key is the transaction id (ProviderKeys.Payout), so a well-formed reference identifies the withdrawal directly.
        Guid? keyedId = Guid.TryParseExact(webhook.Reference, "N", out var parsed) ? parsed : null;
        return _db.Transactions.AsNoTracking()
            .Where(t => t.Type == TransactionType.Withdrawal
                && (t.ProviderReferenceId == webhook.ProviderReferenceId || (keyedId != null && t.Id == keyedId)))
            .Select(t => (Guid?)t.Id)
            .FirstOrDefaultAsync(cancellationToken);
    }
}
