using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

namespace Live.Wallet.Outbox;

public sealed partial class OutboxRelayService : BackgroundService
{
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly WalletOptions _options;
    private readonly ILogger<OutboxRelayService> _logger;

    public OutboxRelayService(IServiceScopeFactory scopeFactory, IOptions<WalletOptions> options, ILogger<OutboxRelayService> logger)
    {
        _scopeFactory = scopeFactory;
        _options = options.Value;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        using var timer = new PeriodicTimer(_options.OutboxPollInterval);
        while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
        {
            try
            {
                await using var scope = _scopeFactory.CreateAsyncScope();
                await scope.ServiceProvider.GetRequiredService<OutboxRelay>().RelayPendingAsync(stoppingToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (!stoppingToken.IsCancellationRequested)
            {
                LogRelayFailed(_logger, exception);
            }
        }
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Outbox relay run failed")]
    private static partial void LogRelayFailed(ILogger logger, Exception exception);
}
