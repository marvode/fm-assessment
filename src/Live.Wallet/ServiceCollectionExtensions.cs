using Live.Wallet.Outbox;
using Live.Wallet.Payouts;
using Live.Wallet.Providers;
using Live.Wallet.Reconciliation;
using Live.Wallet.TopUps;
using Live.Wallet.Wallets;
using Live.Wallet.Webhooks;
using Live.Wallet.Withdrawals;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.DependencyInjection.Extensions;
using Practice.Common;

namespace Live.Wallet;

public static class ServiceCollectionExtensions
{
    /// <summary>Registers the wallet's handlers and adapters. The host must also register WalletDbContext and a RabbitMQ IConnection.</summary>
    public static IServiceCollection AddWallet(this IServiceCollection services, IConfiguration configuration)
    {
        services.AddOptions<WalletOptions>().Bind(configuration.GetSection(WalletOptions.SectionName));
        services.TryAddSingleton<IClock, SystemClock>();
        services.AddSingleton<FakePaymentProvider>();
        services.AddSingleton<IPaymentProvider>(sp => sp.GetRequiredService<FakePaymentProvider>());
        services.AddSingleton<IMessagePublisher, RabbitMqMessagePublisher>();

        services.AddScoped<CreateWalletHandler>();
        services.AddScoped<GetWalletHandler>();
        services.AddScoped<ListTransactionsHandler>();
        services.AddScoped<InitiateTopUpHandler>();
        services.AddScoped<CompleteTopUpHandler>();
        services.AddScoped<RequestWithdrawalHandler>();
        services.AddScoped<SettleWithdrawalHandler>();
        services.AddScoped<ReleaseWithdrawalHandler>();
        services.AddScoped<ProviderWebhookHandler>();
        services.AddScoped<OutboxRelay>();
        services.AddScoped<PayoutProcessor>();
        services.AddScoped<PendingPayoutSweeper>();
        services.AddScoped<LedgerInvariantChecker>();
        return services;
    }

    public static IServiceCollection AddWalletBackgroundWorkers(this IServiceCollection services)
    {
        services.AddHostedService<OutboxRelayService>();
        services.AddHostedService<RabbitMqPayoutConsumer>();
        services.AddHostedService<ReconciliationService>();
        return services;
    }
}
