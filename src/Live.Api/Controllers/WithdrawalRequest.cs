namespace Live.Api.Controllers;

public sealed record WithdrawalRequest
{
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }

    public required string BankCode { get; init; }

    public required string AccountNumber { get; init; }

    public required string AccountName { get; init; }
}
