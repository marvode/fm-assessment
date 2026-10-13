using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class LedgerTransactionTests
{
    private static readonly DateTimeOffset Now = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void A_new_withdrawal_is_pending_without_a_provider_reference()
    {
        var accountId = Guid.NewGuid();

        var withdrawal = LedgerTransaction.StartWithdrawal(accountId, 400, "wd-1", "hash", Now);

        withdrawal.Type.Should().Be(TransactionType.Withdrawal);
        withdrawal.Status.Should().Be(TransactionStatus.Pending);
        withdrawal.IsFinal.Should().BeFalse();
        withdrawal.AccountId.Should().Be(accountId);
        withdrawal.Amount.Should().Be(400);
        withdrawal.ReferenceId.Should().Be("wd-1");
        withdrawal.RequestHash.Should().Be("hash");
        withdrawal.ProviderReferenceId.Should().BeNull();
        withdrawal.CreatedAt.Should().Be(Now);
        withdrawal.CompletedAt.Should().BeNull();
    }

    [Fact]
    public void A_new_top_up_carries_the_provider_reference()
    {
        LedgerTransaction.StartTopUp(Guid.NewGuid(), 500, "ref-1", "hash", "chg_1", Now)
            .ProviderReferenceId.Should().Be("chg_1");
    }

    [Fact]
    public void A_transaction_can_be_finalised_only_once()
    {
        var withdrawal = LedgerTransaction.StartWithdrawal(Guid.NewGuid(), 400, "wd-1", "hash", Now);
        var later = Now.AddMinutes(1);

        withdrawal.MarkSucceeded(later);

        withdrawal.Status.Should().Be(TransactionStatus.Succeeded);
        withdrawal.IsFinal.Should().BeTrue();
        withdrawal.CompletedAt.Should().Be(later);
        var again = () => withdrawal.MarkFailed(later);
        again.Should().Throw<InvalidOperationException>();
    }
}
