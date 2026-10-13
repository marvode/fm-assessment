using Wallet.Ledger.Domain;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Wallet.Ledger.Persistence;

internal sealed class LedgerTransactionConfiguration : IEntityTypeConfiguration<LedgerTransaction>
{
    public void Configure(EntityTypeBuilder<LedgerTransaction> builder)
    {
        builder.ToTable("Transactions", table => table.HasCheckConstraint("CK_Transactions_Amount", "\"Amount\" > 0"));
        builder.HasKey(t => t.Id);
        builder.Property(t => t.Type).HasConversion<string>().HasMaxLength(16);
        // Concurrency token: a status change only commits if nobody finalised the transaction first.
        builder.Property(t => t.Status).HasConversion<string>().HasMaxLength(16).IsConcurrencyToken();
        builder.Property(t => t.ReferenceId).HasMaxLength(FieldLimits.ReferenceIdMaxLength);
        builder.Property(t => t.ProviderReferenceId).HasMaxLength(128);
        builder.Property(t => t.RequestHash).HasMaxLength(64);
        // Client referenceIds are idempotency keys scoped to one wallet.
        builder.HasIndex(t => new { t.AccountId, t.ReferenceId }).IsUnique();
        builder.HasIndex(t => t.ProviderReferenceId).IsUnique().HasFilter("\"ProviderReferenceId\" IS NOT NULL");
        builder.HasIndex(t => new { t.AccountId, t.CreatedAt });
        builder.HasIndex(t => new { t.Type, t.Status, t.CreatedAt });
        builder.HasOne<Account>().WithMany().HasForeignKey(t => t.AccountId).OnDelete(DeleteBehavior.Restrict);
    }
}
