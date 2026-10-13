using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class AccountConfiguration : IEntityTypeConfiguration<Account>
{
    private static readonly DateTimeOffset SeededAt = new(2026, 1, 1, 0, 0, 0, TimeSpan.Zero);

    public void Configure(EntityTypeBuilder<Account> builder)
    {
        builder.ToTable("Accounts", table =>
        {
            table.HasCheckConstraint(
                "CK_Accounts_UserWalletBalances",
                "\"Type\" <> 'User' OR (\"Balance\" >= 0 AND \"AvailableBalance\" >= 0 AND \"AvailableBalance\" <= \"Balance\")");
            table.HasCheckConstraint("CK_Accounts_Currency", "\"Currency\" = 'NGN'");
        });
        builder.HasKey(a => a.Id);
        builder.Property(a => a.Type).HasConversion<string>().HasMaxLength(16);
        builder.Property(a => a.Name).HasMaxLength(64);
        builder.Property(a => a.Currency).HasMaxLength(3).IsFixedLength();
        builder.Property(a => a.Version).IsConcurrencyToken();
        builder.HasIndex(a => new { a.UserId, a.Currency }).IsUnique().HasFilter("\"UserId\" IS NOT NULL");
        builder.HasIndex(a => a.Name).IsUnique().HasFilter("\"Name\" IS NOT NULL");
        builder.HasData(new
        {
            Id = Account.ProviderSettlementId,
            Type = AccountType.System,
            Name = Account.ProviderSettlementName,
            Currency = WalletCurrency.Code,
            Balance = 0L,
            AvailableBalance = 0L,
            Version = 0,
            CreatedAt = SeededAt,
        });
    }
}
