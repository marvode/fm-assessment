using Wallet.Ledger;
using Wallet.Ledger.Persistence;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Mvc.Testing;
using Microsoft.AspNetCore.TestHost;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Storage;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;

namespace Wallet.Tests.Api;

/// <summary>Runs the real API pipeline on an InMemory WalletDbContext, with the RabbitMQ workers removed.</summary>
public sealed class WalletApiFactory : WebApplicationFactory<Program>
{
    private readonly string _databaseName = Guid.NewGuid().ToString();
    private readonly InMemoryDatabaseRoot _root = new();

    protected override void ConfigureWebHost(IWebHostBuilder builder)
    {
        builder.UseEnvironment("Testing");
        // Aspire's client registrations need connection strings to exist; nothing connects, because the DbContext is replaced and no worker runs.
        builder.UseSetting("ConnectionStrings:appdb", "Host=localhost;Database=wallet_api_tests");
        builder.UseSetting("ConnectionStrings:messaging", "amqp://localhost:5672");
        builder.ConfigureTestServices(services =>
        {
            var walletDbRegistrations = services
                .Where(d => d.ServiceType == typeof(WalletDbContext) || d.ServiceType.GenericTypeArguments.Contains(typeof(WalletDbContext)))
                .ToList();
            foreach (var descriptor in walletDbRegistrations)
            {
                services.Remove(descriptor);
            }

            services.AddDbContext<WalletDbContext>(options => options.UseInMemoryDatabase(_databaseName, _root));

            var walletWorkers = services
                .Where(d => d.ServiceType == typeof(IHostedService) && d.ImplementationType?.Assembly == typeof(WalletOptions).Assembly)
                .ToList();
            foreach (var descriptor in walletWorkers)
            {
                services.Remove(descriptor);
            }
        });
    }

    protected override IHost CreateHost(IHostBuilder builder)
    {
        var host = base.CreateHost(builder);
        // EnsureCreated applies the HasData seed (the ProviderSettlement account) to the InMemory store.
        using var scope = host.Services.CreateScope();
        scope.ServiceProvider.GetRequiredService<WalletDbContext>().Database.EnsureCreated();
        return host;
    }
}
