using System.Text;
using Wallet.Ledger.Payouts;
using RabbitMQ.Client;

namespace Wallet.Ledger.Outbox;

/// <summary>Publishes outbox messages with publisher confirms, so a publish completes only after the broker has the message.</summary>
public sealed class RabbitMqMessagePublisher : IMessagePublisher, IAsyncDisposable
{
    private readonly IConnection _connection;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private IChannel? _channel;

    public RabbitMqMessagePublisher(IConnection connection)
    {
        _connection = connection;
    }

    public async Task PublishAsync(string messageType, string payload, Guid messageId, CancellationToken cancellationToken)
    {
        if (messageType != PayoutRequested.MessageType)
        {
            throw new InvalidOperationException($"No queue is configured for message type '{messageType}'.");
        }

        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            var channel = await GetChannelAsync(cancellationToken).ConfigureAwait(false);
            var properties = new BasicProperties
            {
                Persistent = true,
                MessageId = messageId.ToString(),
                Type = messageType,
                ContentType = "application/json",
            };
            await channel.BasicPublishAsync(
                exchange: string.Empty,
                routingKey: PayoutQueueTopology.QueueName,
                mandatory: true,
                basicProperties: properties,
                body: Encoding.UTF8.GetBytes(payload),
                cancellationToken: cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _gate.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (_channel is not null)
        {
            await _channel.DisposeAsync().ConfigureAwait(false);
        }

        _gate.Dispose();
    }

    private async Task<IChannel> GetChannelAsync(CancellationToken cancellationToken)
    {
        if (_channel is { IsOpen: true } open)
        {
            return open;
        }

        if (_channel is not null)
        {
            await _channel.DisposeAsync().ConfigureAwait(false);
        }

        var options = new CreateChannelOptions(publisherConfirmationsEnabled: true, publisherConfirmationTrackingEnabled: true);
        var channel = await _connection.CreateChannelAsync(options, cancellationToken).ConfigureAwait(false);
        await PayoutQueueTopology.DeclareAsync(channel, cancellationToken).ConfigureAwait(false);
        _channel = channel;
        return channel;
    }
}
