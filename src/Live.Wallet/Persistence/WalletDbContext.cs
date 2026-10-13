using Live.Wallet.Ledger;
using Live.Wallet.Outbox;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;

namespace Live.Wallet.Persistence;

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
