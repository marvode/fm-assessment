using System.Collections.Concurrent;

namespace Wallet.Ledger.Providers;

/// <summary>In-memory stand-in for a real provider. Tests script payout behaviour; the dev endpoint reads it to fake webhooks.</summary>
public sealed class FakePaymentProvider : IPaymentProvider
{
    private readonly ConcurrentDictionary<string, FakeProviderRecord> _charges = new();
    private readonly ConcurrentDictionary<string, FakeProviderRecord> _payouts = new();
    private int _payoutAttempts;

    /// <summary>Decides new payouts. Null accepts them; throwing simulates a transient provider fault.</summary>
    public Func<PayoutInstruction, PayoutInitiation>? PayoutBehaviour { get; set; }

    public int PayoutAttempts => _payoutAttempts;

    public Task<ChargeInitiation> InitiateChargeAsync(string referenceId, long amount, CancellationToken cancellationToken)
    {
        var record = _charges.GetOrAdd(referenceId, reference => new FakeProviderRecord
        {
            IsPayout = false,
            ReferenceId = reference,
            ProviderReferenceId = $"chg_{Guid.NewGuid():N}",
            Amount = amount,
        });
        return Task.FromResult(new ChargeInitiation { ProviderReferenceId = record.ProviderReferenceId });
    }

    public Task<PayoutInitiation> InitiatePayoutAsync(PayoutInstruction instruction, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref _payoutAttempts);
        if (_payouts.TryGetValue(instruction.ReferenceId, out var existing))
        {
            return Task.FromResult(existing.Status == PayoutStatus.Failed
                ? PayoutInitiation.Rejected("The payout already failed.")
                : PayoutInitiation.Accepted(existing.ProviderReferenceId));
        }

        var initiation = PayoutBehaviour?.Invoke(instruction) ?? PayoutInitiation.Accepted($"trf_{Guid.NewGuid():N}");
        if (initiation is { Outcome: PayoutOutcome.Accepted, ProviderReferenceId: { } providerReferenceId })
        {
            _payouts[instruction.ReferenceId] = new FakeProviderRecord
            {
                IsPayout = true,
                ReferenceId = instruction.ReferenceId,
                ProviderReferenceId = providerReferenceId,
                Amount = instruction.Amount,
            };
        }

        return Task.FromResult(initiation);
    }

    public Task<PayoutStatus> GetPayoutStatusAsync(string referenceId, CancellationToken cancellationToken) =>
        Task.FromResult(_payouts.TryGetValue(referenceId, out var record) ? record.Status : PayoutStatus.NotFound);

    public void SetPayoutStatus(string referenceId, PayoutStatus status)
    {
        if (_payouts.TryGetValue(referenceId, out var record))
        {
            record.Status = status;
        }
    }

    public FakeProviderRecord? FindByProviderReference(string providerReferenceId) =>
        _charges.Values.Concat(_payouts.Values).FirstOrDefault(r => r.ProviderReferenceId == providerReferenceId);
}
