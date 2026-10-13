namespace Wallet.Ledger.Reconciliation;

public sealed record InvariantViolation
{
    /// <summary>The spec's invariant id: I1 balanced, I3 account matches entries, I4 non-negative wallet, I6 global zero-sum.</summary>
    public required string Invariant { get; init; }

    public required string Detail { get; init; }
}
