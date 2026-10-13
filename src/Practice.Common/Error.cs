namespace Practice.Common;

/// <summary>A failure the caller is expected to handle: a stable code plus a human-readable message.</summary>
public sealed record Error
{
    public required string Code { get; init; }

    public required string Message { get; init; }

    public static Error Create(string code, string message) => new() { Code = code, Message = message };
}
