using Wallet.Ledger.Domain;
using Wallet.Ledger.Outbox;
using Wallet.Ledger.Withdrawals;
using Microsoft.EntityFrameworkCore;

namespace Wallet.Ledger.Persistence;

public sealed class WalletDbContext : DbContext
{
    public WalletDbContext(DbContextOptions<WalletDbContext> options)
        : base(options)
    {
    }

    public DbSet<Account> Accounts => Set<Account>();

    public DbSet<LedgerTransaction> Transactions => Set<LedgerTransaction>();

    public DbSet<LedgerEntry> LedgerEntries => Set<LedgerEntry>();

    public DbSet<WithdrawalDetails> Withdrawals => Set<WithdrawalDetails>();

    public DbSet<OutboxMessage> OutboxMessages => Set<OutboxMessage>();

    protected override void OnModelCreating(ModelBuilder modelBuilder) =>
        modelBuilder.ApplyConfigurationsFromAssembly(typeof(WalletDbContext).Assembly);
}
