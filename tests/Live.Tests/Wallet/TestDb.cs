using Live.Wallet.Ledger;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Diagnostics;
using Microsoft.EntityFrameworkCore.Storage;

namespace Live.Tests.Wallet;

/// <summary>An isolated InMemory wallet database per test. Its contexts share one store, so a test can play several concurrent writers.</summary>
internal sealed class TestDb
{
    public static readonly DateTimeOffset Start = new(2026, 10, 13, 9, 0, 0, TimeSpan.Zero);

    private readonly string _databaseName = Guid.NewGuid().ToString();
    private readonly InMemoryDatabaseRoot _root = new();

    public WalletDbContext CreateContext(params IInterceptor[] interceptors)
    {
        // AtomicSaveInterceptor runs last, so a test's concurrent write lands before the version check.
        var db = new WalletDbContext(BuildOptions([.. interceptors, new AtomicSaveInterceptor(OpenStoreView)]));
        db.Database.EnsureCreated();
        return db;
    }

    public async Task<T> QueryAsync<T>(Func<WalletDbContext, Task<T>> query)
    {
        await using var db = CreateContext();
        return await query(db);
    }

    public Task<Account> GetWalletAsync(Guid userId) =>
        QueryAsync(db => db.Accounts.AsNoTracking().SingleAsync(a => a.UserId == userId));

    public Task<Account> GetSettlementAsync() =>
        QueryAsync(db => db.Accounts.AsNoTracking().SingleAsync(a => a.Id == Account.ProviderSettlementId));

    public Task<LedgerTransaction> GetTransactionAsync(Guid transactionId) =>
        QueryAsync(db => db.Transactions.AsNoTracking().SingleAsync(t => t.Id == transactionId));

    /// <summary>Opens a wallet and, when balance is positive, funds it with a settled top-up.</summary>
    public async Task<Guid> SeedWalletAsync(Guid userId, long balance, DateTimeOffset at)
    {
        var wallet = Account.OpenUserWallet(userId, at);
        await using (var db = CreateContext())
        {
            db.Accounts.Add(wallet);
            await db.SaveChangesAsync();
        }

        if (balance > 0)
        {
            await SeedTopUpAsync(wallet.Id, balance, at);
        }

        return wallet.Id;
    }

    public async Task SeedTopUpAsync(Guid accountId, long amount, DateTimeOffset at)
    {
        await using var db = CreateContext();
        var wallet = await db.Accounts.SingleAsync(a => a.Id == accountId);
        var settlement = await db.Accounts.SingleAsync(a => a.Id == Account.ProviderSettlementId);
        var topUp = LedgerTransaction.StartTopUp(accountId, amount, $"seed-{Guid.NewGuid():N}", "seed", $"chg_seed_{Guid.NewGuid():N}", at);
        var entries = LedgerPosting.Post(topUp, settlement, wallet, debitFromHold: false, at).Value;
        topUp.MarkSucceeded(at);
        db.Transactions.Add(topUp);
        db.LedgerEntries.AddRange(entries);
        await db.SaveChangesAsync();
    }

    private WalletDbContext OpenStoreView() => new(BuildOptions([]));

    private DbContextOptions<WalletDbContext> BuildOptions(IInterceptor[] interceptors) =>
        new DbContextOptionsBuilder<WalletDbContext>()
            .UseInMemoryDatabase(_databaseName, _root)
            .AddInterceptors(interceptors)
            .Options;
}
