using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Live.Wallet.Providers;
using Live.Wallet.Wallets;
using Practice.Common;

namespace Live.Wallet.TopUps;

/// <summary>Starts a charge with the provider and records a Pending top-up. Money moves only when the provider's webhook confirms it.</summary>
public sealed class InitiateTopUpHandler
{
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

        if (string.IsNullOrWhiteSpace(command.ReferenceId))
        {
            return WalletErrors.Validation("ReferenceId", "ReferenceId is required.");
        }

        var requestHash = RequestHash.Compute(nameof(TransactionType.TopUp), command.UserId, command.Amount);
        var replay = await Idempotency.FindReplayAsync(_db, command.ReferenceId, requestHash, cancellationToken).ConfigureAwait(false);
        if (replay is not null)
        {
            return replay;
        }

        var now = _clock.UtcNow;
        var (wallet, _) = await WalletAccounts.GetOrCreateAsync(_db, command.UserId, now, cancellationToken).ConfigureAwait(false);
        var charge = await _provider.InitiateChargeAsync(command.ReferenceId, command.Amount, cancellationToken).ConfigureAwait(false);
        var transaction = LedgerTransaction.StartTopUp(wallet.Id, command.Amount, command.ReferenceId, requestHash, charge.ProviderReferenceId, now);
        _db.Transactions.Add(transaction);
        await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        return new IdempotentResponse { Transaction = TransactionResponse.From(transaction), Replayed = false };
    }
}
