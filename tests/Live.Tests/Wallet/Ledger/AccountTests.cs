using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class AccountTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void OpenUserWallet_starts_empty_in_NGN()
    {
        var userId = Guid.NewGuid();

        var account = Account.OpenUserWallet(userId, Now);

        account.UserId.Should().Be(userId);
        account.Type.Should().Be(AccountType.User);
        account.Currency.Should().Be("NGN");
        account.Balance.Should().Be(0);
        account.AvailableBalance.Should().Be(0);
        account.CreatedAt.Should().Be(Now);
    }

    [Fact]
    public void Credit_raises_both_balances_and_records_the_running_balance()
    {
        var account = Account.OpenUserWallet(Guid.NewGuid(), Now);
        var transactionId = Guid.NewGuid();

        var entry = account.Credit(transactionId, 500, Now);

        account.Balance.Should().Be(500);
        account.AvailableBalance.Should().Be(500);
        entry.Direction.Should().Be(EntryDirection.Credit);
        entry.TransactionId.Should().Be(transactionId);
        entry.AccountId.Should().Be(account.Id);
        entry.Amount.Should().Be(500);
        entry.Currency.Should().Be("NGN");
        entry.BalanceBefore.Should().Be(0);
        entry.BalanceAfter.Should().Be(500);
    }

    [Fact]
    public void PlaceHold_reduces_only_the_available_balance()
    {
        var account = Funded(500);

        var result = account.PlaceHold(200);

        result.IsSuccess.Should().BeTrue();
        account.AvailableBalance.Should().Be(300);
        account.Balance.Should().Be(500);
    }

    [Fact]
    public void PlaceHold_for_the_entire_available_balance_succeeds()
    {
        var account = Funded(500);

        account.PlaceHold(500).IsSuccess.Should().BeTrue();

        account.AvailableBalance.Should().Be(0);
    }

    [Fact]
    public void PlaceHold_beyond_the_available_balance_fails_and_changes_nothing()
    {
        var account = Funded(100);
        var versionBefore = account.Version;

        var result = account.PlaceHold(101);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        account.AvailableBalance.Should().Be(100);
        account.Version.Should().Be(versionBefore);
    }

    [Theory]
    [InlineData(0)]
    [InlineData(-1)]
    public void PlaceHold_rejects_non_positive_amounts(long amount)
    {
        Funded(100).PlaceHold(amount).Error.Code.Should().Be("Money.InvalidAmount");
    }

    [Fact]
    public void ReleaseHold_restores_exactly_the_held_amount()
    {
        var account = Funded(500);
        account.PlaceHold(200);

        account.ReleaseHold(200).IsSuccess.Should().BeTrue();

        account.AvailableBalance.Should().Be(500);
        account.Balance.Should().Be(500);
    }

    [Fact]
    public void ReleaseHold_without_a_matching_hold_fails()
    {
        var account = Funded(500);

        account.ReleaseHold(1).Error.Code.Should().Be("Ledger.HoldMissing");

        account.AvailableBalance.Should().Be(500);
    }

    [Fact]
    public void Debit_from_a_hold_lowers_the_balance_without_lowering_available_again()
    {
        var account = Funded(500);
        account.PlaceHold(200);

        var entry = account.Debit(Guid.NewGuid(), 200, fromHold: true, Now);

        account.Balance.Should().Be(300);
        account.AvailableBalance.Should().Be(300);
        entry.Direction.Should().Be(EntryDirection.Debit);
        entry.BalanceBefore.Should().Be(500);
        entry.BalanceAfter.Should().Be(300);
    }

    [Fact]
    public void CanDebit_without_a_hold_requires_available_funds()
    {
        var account = Funded(500);
        account.PlaceHold(400);

        account.CanDebit(200, fromHold: false).Error.Code.Should().Be("Wallet.InsufficientFunds");
    }

    [Fact]
    public void CanDebit_from_a_hold_requires_a_matching_hold()
    {
        Funded(500).CanDebit(200, fromHold: true).Error.Code.Should().Be("Ledger.HoldMissing");
    }

    [Fact]
    public void A_system_account_can_be_debited_below_zero()
    {
        var settlement = Account.OpenSystem(Account.ProviderSettlementId, Account.ProviderSettlementName, Now);

        settlement.CanDebit(1_000, fromHold: false).IsSuccess.Should().BeTrue();
        settlement.Debit(Guid.NewGuid(), 1_000, fromHold: false, Now);

        settlement.Balance.Should().Be(-1_000);
        settlement.Type.Should().Be(AccountType.System);
        settlement.UserId.Should().BeNull();
    }

    [Fact]
    public void Every_change_bumps_the_version()
    {
        var account = Funded(500);
        var versionBefore = account.Version;

        account.PlaceHold(100);

        account.Version.Should().Be(versionBefore + 1);
    }

    private static Account Funded(long amount)
    {
        var account = Account.OpenUserWallet(Guid.NewGuid(), Now);
        account.Credit(Guid.NewGuid(), amount, Now);
        return account;
    }
}
