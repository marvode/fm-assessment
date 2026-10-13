using Live.Wallet.Ledger;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class LedgerEntryConfiguration : IEntityTypeConfiguration<LedgerEntry>
{
    public void Configure(EntityTypeBuilder<LedgerEntry> builder)
    {
        builder.ToTable("LedgerEntries", table => table.HasCheckConstraint("CK_LedgerEntries_Amount", "\"Amount\" > 0"));
        builder.HasKey(e => e.Id);
        builder.Property(e => e.Direction).HasConversion<string>().HasMaxLength(8);
        builder.Property(e => e.Currency).HasMaxLength(3).IsFixedLength();
        builder.HasIndex(e => e.TransactionId);
        builder.HasIndex(e => new { e.AccountId, e.CreatedAt });
        builder.HasOne<LedgerTransaction>().WithMany().HasForeignKey(e => e.TransactionId).OnDelete(DeleteBehavior.Restrict);
        builder.HasOne<Account>().WithMany().HasForeignKey(e => e.AccountId).OnDelete(DeleteBehavior.Restrict);
    }
}
