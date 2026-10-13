using Wallet.Ledger.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;
using Wallet.Common;

namespace Wallet.Ledger.Outbox;

/// <summary>
/// Publishes unprocessed outbox messages oldest first and marks each processed after the broker accepts it.
/// A crash between publish and save re-publishes the message, so delivery is at-least-once and consumers must be idempotent.
/// </summary>
public sealed partial class OutboxRelay
{
    private readonly WalletDbContext _db;
    private readonly IMessagePublisher _publisher;
    private readonly IClock _clock;
    private readonly WalletOptions _options;
    private readonly ILogger<OutboxRelay> _logger;

    public OutboxRelay(
        WalletDbContext db,
        IMessagePublisher publisher,
        IClock clock,
        IOptions<WalletOptions> options,
        ILogger<OutboxRelay> logger)
    {
        _db = db;
        _publisher = publisher;
        _clock = clock;
        _options = options.Value;
        _logger = logger;
    }

    public async Task<int> RelayPendingAsync(CancellationToken cancellationToken)
    {
        var batch = await _db.OutboxMessages
            .Where(m => m.ProcessedAt == null)
            .OrderBy(m => m.OccurredAt)
            .ThenBy(m => m.Id)
            .Take(_options.OutboxBatchSize)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);

        var published = 0;
        foreach (var message in batch)
        {
            try
            {
                await _publisher.PublishAsync(message.Type, message.Payload, message.Id, cancellationToken).ConfigureAwait(false);
                message.MarkProcessed(_clock.UtcNow);
                published++;
            }
            catch (Exception exception) when (!cancellationToken.IsCancellationRequested)
            {
                message.RecordFailure(exception.Message);
                LogPublishFailed(_logger, exception, message.Id, message.Attempts);
            }

            await _db.SaveChangesAsync(cancellationToken).ConfigureAwait(false);
        }

        return published;
    }

    [LoggerMessage(Level = LogLevel.Warning, Message = "Publishing outbox message {MessageId} failed (attempt {Attempts})")]
    private static partial void LogPublishFailed(ILogger logger, Exception exception, Guid messageId, int attempts);
}
