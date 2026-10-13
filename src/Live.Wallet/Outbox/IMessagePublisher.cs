namespace Live.Wallet.Outbox;

/// <summary>Sends an outbox message to the broker. Completes only once the broker has accepted it, and throws otherwise.</summary>
public interface IMessagePublisher
{
    Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken);
}
