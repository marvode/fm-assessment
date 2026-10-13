using Live.Wallet.Outbox;

namespace Live.Tests.Wallet;

internal sealed class RecordingPublisher : IMessagePublisher
{
    public List<(string Type, string Payload, Guid MessageId)> Published { get; } = [];

    public Func<Guid, bool> FailWhen { get; set; } = _ => false;

    public Func<Guid, bool> TimeOutWhen { get; set; } = _ => false;

    public Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken)
    {
        if (FailWhen(messageId))
        {
            throw new InvalidOperationException("broker unavailable");
        }

        if (TimeOutWhen(messageId))
        {
            // What HttpClient and broker clients throw on a timeout: a cancellation the caller did not request.
            throw new TaskCanceledException("publish timed out");
        }

        Published.Add((messageType, payload, messageId));
        return Task.CompletedTask;
    }
}
