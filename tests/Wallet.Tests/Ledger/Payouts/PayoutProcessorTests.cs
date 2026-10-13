using FluentAssertions;
using Wallet.Ledger.Payouts;
using Wallet.Ledger.Persistence;
using Wallet.Ledger.Providers;
using Wallet.Ledger.Withdrawals;
using Microsoft.Extensions.Logging.Abstractions;
using Wallet.Common;

namespace Wallet.Tests.Ledger.Payouts;

public sealed class PayoutProcessorTests
{
    private readonly TestDb _testDb = new();
    private readonly FakeClock _clock = new(TestDb.Start);
    private readonly FakePaymentProvider _provider = new();
    private readonly Guid _userId = Guid.NewGuid();

    [Fact]
    public async Task An_accepted_payout_stores_the_provider_reference_and_stays_pending()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        PayoutInstruction? sent = null;
        _provider.PayoutBehaviour = instruction =>
        {
            sent = instruction;
            return PayoutInitiation.Accepted("trf_accepted");
        };
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Initiated);
        sent.Should().BeEquivalentTo(new PayoutInstruction
        {
            ReferenceId = ProviderKeys.Payout(transactionId),
            Amount = 4_000,
            BankCode = "058",
            AccountNumber = "0123456789",
            AccountName = "Ada Lovelace",
        });
        var transaction = await _testDb.GetTransactionAsync(transactionId);
        transaction.ProviderReferenceId.Should().Be("trf_accepted");
        transaction.IsFinal.Should().BeFalse();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task A_rejected_payout_releases_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _provider.PayoutBehaviour = _ => PayoutInitiation.Rejected("Invalid account");
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Rejected);
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeTrue();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(10_000);
    }

    [Fact]
    public async Task A_transient_provider_error_propagates_and_keeps_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _provider.PayoutBehaviour = _ => throw new HttpRequestException("provider timeout");
        await using var db = _testDb.CreateContext();

        var process = () => Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        await process.Should().ThrowAsync<HttpRequestException>();
        var transaction = await _testDb.GetTransactionAsync(transactionId);
        transaction.IsFinal.Should().BeFalse();
        transaction.ProviderReferenceId.Should().BeNull();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task An_acceptance_without_a_provider_reference_keeps_the_hold()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        _provider.PayoutBehaviour = _ => new PayoutInitiation { Outcome = PayoutOutcome.Accepted };
        await using var db = _testDb.CreateContext();

        var process = () => Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        await process.Should().ThrowAsync<InvalidOperationException>();
        (await _testDb.GetTransactionAsync(transactionId)).IsFinal.Should().BeFalse();
        (await _testDb.GetWalletAsync(_userId)).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task Redelivered_message_after_acceptance_does_not_pay_twice()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        var message = new PayoutRequested { TransactionId = transactionId };

        await Processor(db).ProcessAsync(message, CancellationToken.None);
        var redelivery = await Processor(db).ProcessAsync(message, CancellationToken.None);

        redelivery.Value.Should().Be(PayoutProcessingOutcome.Skipped);
        _provider.PayoutAttempts.Should().Be(1);
    }

    [Fact]
    public async Task A_message_for_a_final_withdrawal_is_skipped()
    {
        var transactionId = await WalletScenarios.PendingWithdrawalAsync(_testDb, _clock, _userId, 10_000, 4_000);
        await using var db = _testDb.CreateContext();
        await Release(db).HandleAsync(transactionId, "test", CancellationToken.None);

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = transactionId }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Skipped);
        _provider.PayoutAttempts.Should().Be(0);
    }

    [Fact]
    public async Task A_message_for_an_unknown_transaction_is_skipped()
    {
        await using var db = _testDb.CreateContext();

        var outcome = await Processor(db).ProcessAsync(new PayoutRequested { TransactionId = Guid.NewGuid() }, CancellationToken.None);

        outcome.Value.Should().Be(PayoutProcessingOutcome.Skipped);
    }

    private PayoutProcessor Processor(WalletDbContext db) =>
        new(db, _provider, Release(db), NullLogger<PayoutProcessor>.Instance);

    private ReleaseWithdrawalHandler Release(WalletDbContext db) =>
        new(db, _clock, NullLogger<ReleaseWithdrawalHandler>.Instance);
}
