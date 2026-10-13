namespace Live.Wallet.Reconciliation;

public sealed record SweepSummary
{
    public required int Settled { get; init; }

    public required int Released { get; init; }

    public required int StillPending { get; init; }

    public required int Errors { get; init; }
}
