namespace Live.Wallet.Ledger;

/// <summary>One money movement (a top-up or a withdrawal) and its lifecycle. Stored in the Transactions table.</summary>
public sealed class LedgerTransaction
{
    private LedgerTransaction()
    {
        ReferenceId = string.Empty;
        RequestHash = string.Empty;
    }

    public Guid Id { get; private set; }

    public Guid AccountId { get; private set; }

    public TransactionType Type { get; private set; }

    public TransactionStatus Status { get; private set; }

    public long Amount { get; private set; }

    /// <summary>The client's idempotency key.</summary>
    public string ReferenceId { get; private set; }

    public string? ProviderReferenceId { get; private set; }

    /// <summary>Fingerprint of the original request, to tell a safe retry from a reused ReferenceId.</summary>
    public string RequestHash { get; private set; }

    public DateTimeOffset CreatedAt { get; private set; }

    public DateTimeOffset? CompletedAt { get; private set; }

    public bool IsFinal => Status != TransactionStatus.Pending;

    public static LedgerTransaction StartTopUp(
        Guid accountId, long amount, string referenceId, string requestHash, string providerReferenceId, DateTimeOffset now) =>
        Start(TransactionType.TopUp, accountId, amount, referenceId, requestHash, providerReferenceId, now);

    public static LedgerTransaction StartWithdrawal(
        Guid accountId, long amount, string referenceId, string requestHash, DateTimeOffset now) =>
        Start(TransactionType.Withdrawal, accountId, amount, referenceId, requestHash, providerReferenceId: null, now);

    public void AssignProviderReference(string providerReferenceId) => ProviderReferenceId = providerReferenceId;

    public void MarkSucceeded(DateTimeOffset now) => Complete(TransactionStatus.Succeeded, now);

    public void MarkFailed(DateTimeOffset now) => Complete(TransactionStatus.Failed, now);

    private void Complete(TransactionStatus status, DateTimeOffset now)
    {
        if (IsFinal)
        {
            // Handlers check IsFinal first; reaching this is a bug, not an expected failure.
            throw new InvalidOperationException($"Transaction {Id} is already {Status}.");
        }

        Status = status;
        CompletedAt = now;
    }

    private static LedgerTransaction Start(
        TransactionType type,
        Guid accountId,
        long amount,
        string referenceId,
        string requestHash,
        string? providerReferenceId,
        DateTimeOffset now) =>
        new()
        {
            Id = Guid.CreateVersion7(now),
            AccountId = accountId,
            Type = type,
            Status = TransactionStatus.Pending,
            Amount = amount,
            ReferenceId = referenceId,
            RequestHash = requestHash,
            ProviderReferenceId = providerReferenceId,
            CreatedAt = now,
        };
}
