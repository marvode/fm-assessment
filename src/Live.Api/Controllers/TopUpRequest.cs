namespace Live.Api.Controllers;

public sealed record TopUpRequest
{
    public required string ReferenceId { get; init; }

    public required long Amount { get; init; }
}
