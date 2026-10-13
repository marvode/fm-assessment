namespace Live.Wallet.TopUps;

public sealed record InitiateTopUpCommand
{
    public required Guid UserId { get; init; }

    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }
}
