using System.Text.Json;
using Wallet.Ledger.Outbox;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using RabbitMQ.Client;
using RabbitMQ.Client.Events;

namespace Wallet.Ledger.Payouts;

/// <summary>
/// Feeds wallet.payouts messages to PayoutProcessor. It acks on success; on failure it nacks with requeue,
/// so the quorum queue dead-letters after the delivery limit. Unreadable messages go straight to the dead-letter queue.
/// </summary>
public sealed partial class RabbitMqPayoutConsumer : BackgroundService
{
    private static readonly TimeSpan RetryDelay = TimeSpan.FromSeconds(2);

    private readonly IConnection _connection;
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly ILogger<RabbitMqPayoutConsumer> _logger;

    public RabbitMqPayoutConsumer(IConnection connection, IServiceScopeFactory scopeFactory, ILogger<RabbitMqPayoutConsumer> logger)
    {
        _connection = connection;
        _scopeFactory = scopeFactory;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        await using var channel = await _connection.CreateChannelAsync(cancellationToken: stoppingToken).ConfigureAwait(false);
        await PayoutQueueTopology.DeclareAsync(channel, stoppingToken).ConfigureAwait(false);
        await channel.BasicQosAsync(prefetchSize: 0, prefetchCount: 10, global: false, cancellationToken: stoppingToken).ConfigureAwait(false);
        var consumer = new AsyncEventingBasicConsumer(channel);
        consumer.ReceivedAsync += (_, delivery) => HandleDeliveryAsync(channel, delivery, stoppingToken);
        await channel.BasicConsumeAsync(PayoutQueueTopology.QueueName, autoAck: false, consumer: consumer, cancellationToken: stoppingToken).ConfigureAwait(false);
        await Task.Delay(Timeout.Infinite, stoppingToken).ConfigureAwait(false);
    }

    private async Task HandleDeliveryAsync(IChannel channel, BasicDeliverEventArgs delivery, CancellationToken cancellationToken)
    {
        PayoutRequested? message;
        try
        {
            message = JsonSerializer.Deserialize(delivery.Body.Span, WalletJsonContext.Default.PayoutRequested);
        }
        catch (JsonException exception)
        {
            LogUnreadableMessage(_logger, exception, delivery.BasicProperties.MessageId);
            message = null;
        }

        if (message is null)
        {
            await channel.BasicNackAsync(delivery.DeliveryTag, multiple: false, requeue: false, cancellationToken: cancellationToken).ConfigureAwait(false);
            return;
        }

        try
        {
            await using var scope = _scopeFactory.CreateAsyncScope();
            var processor = scope.ServiceProvider.GetRequiredService<PayoutProcessor>();
            var result = await processor.ProcessAsync(message, cancellationToken).ConfigureAwait(false);
            if (result.IsSuccess)
            {
                await channel.BasicAckAsync(delivery.DeliveryTag, multiple: false, cancellationToken: cancellationToken).ConfigureAwait(false);
                return;
            }

            LogProcessingFailed(_logger, message.TransactionId, result.Error.Code);
        }
        catch (Exception exception) when (!cancellationToken.IsCancellationRequested)
        {
            LogTransientFailure(_logger, exception, message.TransactionId);
        }

        // Short pause so a struggling provider isn't hammered; the quorum queue's delivery limit caps the retries.
        await Task.Delay(RetryDelay, cancellationToken).ConfigureAwait(false);
        await channel.BasicNackAsync(delivery.DeliveryTag, multiple: false, requeue: true, cancellationToken: cancellationToken).ConfigureAwait(false);
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Dead-lettering unreadable payout message {MessageId}")]
    private static partial void LogUnreadableMessage(ILogger logger, Exception exception, string? messageId);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout for withdrawal {TransactionId} failed with {ErrorCode}; requeueing")]
    private static partial void LogProcessingFailed(ILogger logger, Guid transactionId, string errorCode);

    [LoggerMessage(Level = LogLevel.Warning, Message = "Payout for withdrawal {TransactionId} hit a transient error; requeueing")]
    private static partial void LogTransientFailure(ILogger logger, Exception exception, Guid transactionId);
}
