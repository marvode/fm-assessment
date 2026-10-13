using Live.Wallet.Ledger;
using Live.Wallet.Withdrawals;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Live.Wallet.Persistence;

internal sealed class WithdrawalDetailsConfiguration : IEntityTypeConfiguration<WithdrawalDetails>
{
    public void Configure(EntityTypeBuilder<WithdrawalDetails> builder)
    {
        builder.ToTable("Withdrawals");
        builder.HasKey(w => w.TransactionId);
        builder.Property(w => w.BankCode).HasMaxLength(16);
        builder.Property(w => w.AccountNumber).HasMaxLength(10);
        builder.Property(w => w.AccountName).HasMaxLength(128);
        builder.HasOne<LedgerTransaction>().WithOne().HasForeignKey<WithdrawalDetails>(w => w.TransactionId).OnDelete(DeleteBehavior.Restrict);
    }
}
