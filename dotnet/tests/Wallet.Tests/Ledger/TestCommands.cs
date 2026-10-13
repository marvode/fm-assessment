using Wallet.Ledger.Withdrawals;

namespace Wallet.Tests.Ledger;

internal static class TestCommands
{
    public static RequestWithdrawalCommand Withdrawal(Guid userId, long amount, string referenceId = "wd-1") =>
        new()
        {
            UserId = userId,
            Amount = amount,
            ReferenceId = referenceId,
            BankCode = "058",
            AccountNumber = "0123456789",
            AccountName = "Ada Lovelace",
        };
}
