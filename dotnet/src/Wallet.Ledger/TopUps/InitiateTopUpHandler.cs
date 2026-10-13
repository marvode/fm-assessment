using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Providers;
using Wallet.Ledger.Wallets;
using Microsoft.EntityFrameworkCore;
using Wallet.Common;

namespace Wallet.Ledger.TopUps;

/// <summary>Starts a charge with the provider and records a Pending top-up. Money moves only when the provider's webhook confirms it.</summary>
public sealed class InitiateTopUpHandler
{
    private const int MaxAttempts = 2;

    private readonly WalletDbContext _db;
    private readonly IPaymentProvider _provider;
    private readonly IClock _clock;

    public InitiateTopUpHandler(WalletDbContext db, IPaymentProvider provider, IClock clock)
    {
        _db = db;
        _provider = provider;
        _clock = clock;
    }

    public async Task<Result<IdempotentResponse>> HandleAsync(InitiateTopUpCommand command, CancellationToken cancellationToken)
    {
        if (command.Amount <= 0)
        {
            return WalletErrors.InvalidAmount;
        }

        if (string.IsNullOrWhiteSpace(command.ReferenceId) || command.ReferenceId.Length > FieldLimits.ReferenceIdMaxLength)
        {
            return WalletErrors.Validation("ReferenceId", $"ReferenceId is required and at most {FieldLimits.ReferenceIdMaxLength} characters.");
        }

        var requestHash = RequestHash.Compute(nameof(TransactionType.TopUp), command.UserId, command.Amount);
        for (var attempt = 1; ; attempt++)
        {
            var now = _clock.UtcNow;
            var (wallet, created) = await WalletAccounts.GetOrCreateAsync(_db, command.UserId, now, cancellationToken).ConfigureAwait(false);
            if (!created)
            {
                var replay = await Idempotency.FindReplayAsync(_db, wallet.Id, command.ReferenceId, requestHash, cancellationToken).ConfigureAwait(false);
                if (replay is not null)
                {
                    return replay;
                }
            }

            var chargeKey = ProviderKeys.Charge(wallet.Id, command.ReferenceId);
            var charge = await _provider.InitiateChargeAsync(chargeKey, command.Amount, cancellationToken).ConfigureAwait(false);
            var transaction = LedgerTransaction.StartTopUp(wallet.Id, command.Amount, command.ReferenceId, requestHash, charge.ProviderReferenceId, now);
            _db.Transactions.Add(transaction);
            try
            {
                await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
                return new IdempotentResponse { Transaction = TransactionResponse.From(transaction), Replayed = false };
            }
            catch (DbUpdateException exception) when (DbErrors.IsUniqueViolation(exception) && attempt < MaxAttempts)
            {
                // A concurrent request committed this referenceId or opened this user's wallet first (unique index).
                // Retry against what it committed: the replay check or the existing wallet now finds it.
                _db.ChangeTracker.Clear();
            }
        }
    }
}
