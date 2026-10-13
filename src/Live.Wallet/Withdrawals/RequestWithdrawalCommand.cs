namespace Live.Wallet.Withdrawals;

public sealed record RequestWithdrawalCommand
{
    public required Guid UserId { get; init; }

    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
