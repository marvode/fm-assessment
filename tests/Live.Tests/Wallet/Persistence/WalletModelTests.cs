using FluentAssertions;
using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Infrastructure;
using Microsoft.EntityFrameworkCore.Metadata;

namespace Live.Tests.Wallet.Persistence;

/// <summary>Checks the Postgres model. InMemory can't enforce these rules, so this confirms the migration declares them.</summary>
public sealed class WalletModelTests
{
    [Fact]
    public void The_migrations_match_the_model()
    {
        using var db = CreateNpgsqlContext();

        db.Database.HasPendingModelChanges().Should().BeFalse();
    }

    [Fact]
    public void Reference_ids_are_unique_per_wallet()
    {
        using var db = CreateNpgsqlContext();
        var transactions = Entity<LedgerTransaction>(db);

        transactions.GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Select(p => p.Name).SequenceEqual(new[] { nameof(LedgerTransaction.AccountId), nameof(LedgerTransaction.ReferenceId) }));
        transactions.GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Count == 1 && i.Properties[0].Name == nameof(LedgerTransaction.ProviderReferenceId)
            && i.GetFilter() == "\"ProviderReferenceId\" IS NOT NULL");
    }

    [Fact]
    public void A_user_has_one_wallet_per_currency()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetIndexes().Should().ContainSingle(i =>
            i.IsUnique && i.Properties.Select(p => p.Name).SequenceEqual(new[] { nameof(Account.UserId), nameof(Account.Currency) }));
    }

    [Fact]
    public void The_account_version_is_a_concurrency_token()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetProperty(nameof(Account.Version)).IsConcurrencyToken.Should().BeTrue();
    }

    [Fact]
    public void Balance_and_amount_rules_are_check_constraints()
    {
        using var db = CreateNpgsqlContext();

        Entity<Account>(db).GetCheckConstraints().Select(c => c.Name)
            .Should().Contain("CK_Accounts_UserWalletBalances").And.Contain("CK_Accounts_Currency");
        Entity<LedgerTransaction>(db).GetCheckConstraints().Select(c => c.Name).Should().Contain("CK_Transactions_Amount");
        Entity<LedgerEntry>(db).GetCheckConstraints().Select(c => c.Name).Should().Contain("CK_LedgerEntries_Amount");
    }

    private static WalletDbContext CreateNpgsqlContext() =>
        new(new DbContextOptionsBuilder<WalletDbContext>().UseNpgsql("Host=localhost;Database=wallet_model_tests").Options);

    // The design-time model keeps check constraints and index filters; the runtime model drops them.
    private static IEntityType Entity<T>(WalletDbContext db) =>
        db.GetService<IDesignTimeModel>().Model.FindEntityType(typeof(T))
        ?? throw new InvalidOperationException($"{typeof(T).Name} is not mapped.");
}
