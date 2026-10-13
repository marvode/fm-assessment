using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

namespace Wallet.Ledger.Reconciliation;

public sealed partial class ReconciliationService : BackgroundService
{
    private readonly IServiceScopeFactory _scopeFactory;
    private readonly WalletOptions _options;
    private readonly ILogger<ReconciliationService> _logger;

    public ReconciliationService(IServiceScopeFactory scopeFactory, IOptions<WalletOptions> options, ILogger<ReconciliationService> logger)
    {
        _scopeFactory = scopeFactory;
        _options = options.Value;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        using var timer = new PeriodicTimer(_options.SweeperInterval);
        while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
        {
            try
            {
                await using var scope = _scopeFactory.CreateAsyncScope();
                var summary = await scope.ServiceProvider.GetRequiredService<PendingPayoutSweeper>().SweepAsync(stoppingToken).ConfigureAwait(false);
                LogSweepCompleted(_logger, summary.Settled, summary.Released, summary.StillPending, summary.Errors);
                await scope.ServiceProvider.GetRequiredService<LedgerInvariantChecker>().CheckAsync(stoppingToken).ConfigureAwait(false);
            }
            catch (Exception exception) when (!stoppingToken.IsCancellationRequested)
            {
                LogReconciliationFailed(_logger, exception);
            }
        }
    }

    [LoggerMessage(Level = LogLevel.Information, Message = "Payout sweep: {Settled} settled, {Released} released, {StillPending} still pending, {Errors} errors")]
    private static partial void LogSweepCompleted(ILogger logger, int settled, int released, int stillPending, int errors);

    [LoggerMessage(Level = LogLevel.Error, Message = "Reconciliation run failed")]
    private static partial void LogReconciliationFailed(ILogger logger, Exception exception);
}
