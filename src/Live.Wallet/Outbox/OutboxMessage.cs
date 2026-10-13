namespace Live.Wallet.Outbox;

/// <summary>An event saved in the same database transaction as the ledger change it describes, then relayed to the queue.</summary>
public sealed class OutboxMessage
{
    private const int MaxErrorLength = 2_000;

    private OutboxMessage()
    {
        Type = string.Empty;
        Payload = string.Empty;
    }

    public Guid Id { get; private set; }

    public string Type { get; private set; }

    public string Payload { get; private set; }

    public DateTimeOffset OccurredAt { get; private set; }

    public DateTimeOffset? ProcessedAt { get; private set; }

    public int Attempts { get; private set; }

    public string? LastError { get; private set; }

    public static OutboxMessage Create(string type, string payload, DateTimeOffset now) =>
        new() { Id = Guid.CreateVersion7(now), Type = type, Payload = payload, OccurredAt = now };

    public void MarkProcessed(DateTimeOffset now)
    {
        Attempts++;
        ProcessedAt = now;
    }

    public void RecordFailure(string error)
    {
        Attempts++;
        LastError = error.Length > MaxErrorLength ? error[..MaxErrorLength] : error;
    }
}
