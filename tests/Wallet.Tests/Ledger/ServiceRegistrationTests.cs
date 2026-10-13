using FluentAssertions;
using Wallet.Ledger;
using Wallet.Ledger.Outbox;
using Wallet.Ledger.Payouts;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Providers;
using Wallet.Ledger.Reconciliation;
using Wallet.Ledger.TopUps;
using Wallet.Ledger.Wallets;
using Wallet.Ledger.Webhooks;
using Wallet.Ledger.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Options;
using NSubstitute;
using RabbitMQ.Client;

namespace Wallet.Tests.Ledger;

public sealed class ServiceRegistrationTests
{
    [Fact]
    public async Task Every_wallet_service_resolves_with_valid_lifetimes()
    {
        var configuration = new ConfigurationBuilder()
            .AddInMemoryCollection(new Dictionary<string, string?> { ["Wallet:PendingThreshold"] = "00:30:00" })
            .Build();
        var services = new ServiceCollection();
        services.AddLogging();
        services.AddDbContext<WalletDbContext>(options => options.UseInMemoryDatabase(Guid.NewGuid().ToString()));
        services.AddSingleton(Substitute.For<IConnection>());

        services.AddWallet(configuration);
        services.AddWalletBackgroundWorkers();

        // Async disposal: RabbitMqMessagePublisher is IAsyncDisposable only, and sync Dispose of the provider would throw.
        await using var provider = services.BuildServiceProvider(new ServiceProviderOptions { ValidateOnBuild = true, ValidateScopes = true });
        await using var scope = provider.CreateAsyncScope();
        Type[] scoped =
        [
            typeof(CreateWalletHandler), typeof(GetWalletHandler), typeof(ListTransactionsHandler),
            typeof(InitiateTopUpHandler), typeof(CompleteTopUpHandler), typeof(RequestWithdrawalHandler),
            typeof(SettleWithdrawalHandler), typeof(ReleaseWithdrawalHandler), typeof(ProviderWebhookHandler),
            typeof(OutboxRelay), typeof(PayoutProcessor), typeof(PendingPayoutSweeper), typeof(LedgerInvariantChecker),
        ];
        foreach (var type in scoped)
        {
            scope.ServiceProvider.GetRequiredService(type).Should().NotBeNull(type.Name);
        }

        provider.GetRequiredService<IPaymentProvider>().Should().BeSameAs(provider.GetRequiredService<FakePaymentProvider>());
        provider.GetRequiredService<IMessagePublisher>().Should().BeOfType<RabbitMqMessagePublisher>();
        provider.GetRequiredService<IOptions<WalletOptions>>().Value.PendingThreshold.Should().Be(TimeSpan.FromMinutes(30));
        provider.GetServices<IHostedService>().Should().HaveCount(3);
    }
}
