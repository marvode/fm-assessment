using RabbitMQ.Client;

namespace Live.Wallet.Payouts;

/// <summary>
/// A durable quorum queue. RabbitMQ counts redeliveries and dead-letters a message after DeliveryLimit attempts.
/// Declaring is idempotent; the publisher and the consumer both declare before use.
/// </summary>
public static class PayoutQueueTopology
{
    public const string QueueName = "wallet.payouts";
    public const string DeadLetterExchange = "wallet.payouts.dlx";
    public const string DeadLetterQueue = "wallet.payouts.dead";
    public const int DeliveryLimit = 5;

    public static async Task DeclareAsync(IChannel channel, CancellationToken cancellationToken)
    {
        await channel.ExchangeDeclareAsync(DeadLetterExchange, ExchangeType.Fanout, durable: true, autoDelete: false, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueDeclareAsync(DeadLetterQueue, durable: true, exclusive: false, autoDelete: false, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueBindAsync(DeadLetterQueue, DeadLetterExchange, routingKey: string.Empty, arguments: null, cancellationToken: cancellationToken).ConfigureAwait(false);
        await channel.QueueDeclareAsync(
            QueueName,
            durable: true,
            exclusive: false,
            autoDelete: false,
            arguments: new Dictionary<string, object?>
            {
                ["x-queue-type"] = "quorum",
                ["x-delivery-limit"] = DeliveryLimit,
                ["x-dead-letter-exchange"] = DeadLetterExchange,
            },
            cancellationToken: cancellationToken).ConfigureAwait(false);
    }
}
