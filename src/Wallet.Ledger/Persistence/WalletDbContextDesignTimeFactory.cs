using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Design;

namespace Wallet.Ledger.Persistence;

/// <summary>Used only by `dotnet ef`. Generating migrations needs a provider, not a reachable database.</summary>
internal sealed class WalletDbContextDesignTimeFactory : IDesignTimeDbContextFactory<WalletDbContext>
{
    public WalletDbContext CreateDbContext(string[] args) =>
        new(new DbContextOptionsBuilder<WalletDbContext>().UseNpgsql("Host=localhost;Database=wallet_design").Options);
}
