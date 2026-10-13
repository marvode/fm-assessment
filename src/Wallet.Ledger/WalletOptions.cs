namespace Wallet.Ledger;

public sealed class WalletOptions
{
    public const string SectionName = "Wallet";

    public TimeSpan OutboxPollInterval { get; set; } = TimeSpan.FromSeconds(1);

    public int OutboxBatchSize { get; set; } = 50;

    public TimeSpan SweeperInterval { get; set; } = TimeSpan.FromMinutes(5);

    /// <summary>How long a withdrawal may stay Pending before the sweeper asks the provider about it.</summary>
    public TimeSpan PendingThreshold { get; set; } = TimeSpan.FromMinutes(15);
}
