namespace Wallet.Ledger.Providers;

/// <summary>
/// The payment provider seen by the wallet. Transient faults (network, 5xx) surface as exceptions;
/// a definite refusal is a Rejected PayoutInitiation. Payouts are idempotent per ReferenceId.
/// </summary>
public interface IPaymentProvider
{
    Task<ChargeInitiation> InitiateChargeAsync(string referenceId, long amount, CancellationToken cancellationToken);

    Task<PayoutInitiation> InitiatePayoutAsync(PayoutInstruction instruction, CancellationToken cancellationToken);

    Task<PayoutStatus> GetPayoutStatusAsync(string referenceId, CancellationToken cancellationToken);
}
