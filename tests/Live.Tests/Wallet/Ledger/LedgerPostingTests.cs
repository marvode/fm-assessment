using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class LedgerPostingTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void A_top_up_posting_is_balanced_and_moves_money_from_settlement_to_the_wallet()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, 500, "ref-1", "hash", "chg_1", Now);

        var entries = LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, Now).Value;

        entries.Should().HaveCount(2);
        entries.Should().ContainSingle(e => e.Direction == EntryDirection.Debit && e.AccountId == settlement.Id && e.Amount == 500);
        entries.Should().ContainSingle(e => e.Direction == EntryDirection.Credit && e.AccountId == wallet.Id && e.Amount == 500);
        entries.Should().OnlyContain(e => e.TransactionId == topUp.Id && e.Currency == "NGN");
        wallet.Balance.Should().Be(500);
        wallet.AvailableBalance.Should().Be(500);
        settlement.Balance.Should().Be(-500);
        (wallet.Balance + settlement.Balance).Should().Be(0);
    }

    [Fact]
    public void Consecutive_postings_chain_running_balances()
    {
        var settlement = Settlement();
        var wallet = Wallet();

        var first = Fund(wallet, settlement, 500);
        var second = Fund(wallet, settlement, 300);

        var firstCredit = first.Single(e => e.Direction == EntryDirection.Credit);
        var secondCredit = second.Single(e => e.Direction == EntryDirection.Credit);
        secondCredit.BalanceBefore.Should().Be(firstCredit.BalanceAfter);
        secondCredit.BalanceAfter.Should().Be(800);
        var secondDebit = second.Single(e => e.Direction == EntryDirection.Debit);
        secondDebit.BalanceBefore.Should().Be(-500);
        secondDebit.BalanceAfter.Should().Be(-800);
    }

    [Fact]
    public void Settling_a_withdrawal_consumes_the_hold()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 1_000);
        wallet.PlaceHold(400);
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 400, "wd-1", "hash", Now);

        var result = LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: true, Now);

        result.IsSuccess.Should().BeTrue();
        wallet.Balance.Should().Be(600);
        wallet.AvailableBalance.Should().Be(600);
        settlement.Balance.Should().Be(-600);
    }

    [Fact]
    public void A_debit_without_funds_fails_and_leaves_both_accounts_untouched()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 100);
        var walletVersion = wallet.Version;
        var settlementVersion = settlement.Version;
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 150, "wd-1", "hash", Now);

        var result = LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: false, Now);

        result.Error.Code.Should().Be("Wallet.InsufficientFunds");
        wallet.Balance.Should().Be(100);
        wallet.AvailableBalance.Should().Be(100);
        settlement.Balance.Should().Be(-100);
        wallet.Version.Should().Be(walletVersion);
        settlement.Version.Should().Be(settlementVersion);
    }

    [Fact]
    public void Settling_without_a_hold_fails()
    {
        var settlement = Settlement();
        var wallet = Wallet();
        Fund(wallet, settlement, 1_000);
        var withdrawal = LedgerTransaction.StartWithdrawal(wallet.Id, 400, "wd-1", "hash", Now);

        LedgerPosting.Post(withdrawal, wallet, settlement, debitFromHold: true, Now)
            .Error.Code.Should().Be("Ledger.HoldMissing");
    }

    [Fact]
    public void Posting_to_the_same_account_is_rejected()
    {
        var wallet = Wallet();
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, 10, "ref-1", "hash", "chg_1", Now);

        LedgerPosting.Post(topUp, wallet, wallet, debitFromHold: false, Now)
            .Error.Code.Should().Be("Ledger.SameAccount");
    }

    private static Account Settlement() =>
        Account.OpenSystem(Account.ProviderSettlementId, Account.ProviderSettlementName, Now);

    private static Account Wallet() => Account.OpenUserWallet(Guid.NewGuid(), Now);

    private static LedgerEntry[] Fund(Account wallet, Account settlement, long amount)
    {
        var topUp = LedgerTransaction.StartTopUp(wallet.Id, amount, $"ref-{Guid.NewGuid():N}", "hash", $"chg_{Guid.NewGuid():N}", Now);
        return LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, Now).Value;
    }
}
