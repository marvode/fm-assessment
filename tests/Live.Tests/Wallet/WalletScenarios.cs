using Live.Wallet.Withdrawals;
using Practice.Common;

namespace Live.Tests.Wallet;

internal static class WalletScenarios
{
    /// <summary>A funded wallet with one held, Pending withdrawal. Returns the withdrawal's transaction id.</summary>
    public static async Task<Guid> PendingWithdrawalAsync(
        TestDb testDb, FakeClock clock, Guid userId, long balance, long amount, string referenceId = "wd-1")
    {
        await testDb.SeedWalletAsync(userId, balance, clock.UtcNow);
        await using var db = testDb.CreateContext();
        var result = await new RequestWithdrawalHandler(db, clock)
            .HandleAsync(TestCommands.Withdrawal(userId, amount, referenceId), CancellationToken.None);
        return result.Value.Transaction.Id;
    }
}
