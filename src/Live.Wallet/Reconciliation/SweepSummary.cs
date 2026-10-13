namespace Live.Wallet.Reconciliation;

public sealed record SweepSummary
{
    public required int Settled { get; init; }

    public required int Released { get; init; }

    /// <summary>Withdrawals the provider has never seen; their payout request was queued again.</summary>
    public required int Redriven { get; init; }

    public required int StillPending { get; init; }

    public required int Errors { get; init; }
}
