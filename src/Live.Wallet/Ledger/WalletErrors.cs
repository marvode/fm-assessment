using Practice.Common;

namespace Live.Wallet.Ledger;

/// <summary>Every expected wallet failure. Codes are stable: the API maps them to HTTP statuses.</summary>
public static class WalletErrors
{
    public static readonly Error InvalidAmount = Error.Create("Money.InvalidAmount", "Amount must be a positive number of kobo.");

    public static readonly Error UnsupportedCurrency = Error.Create("Money.UnsupportedCurrency", "Only NGN is supported.");

    public static readonly Error WalletNotFound = Error.Create("Wallet.NotFound", "The user has no wallet.");

    public static readonly Error InsufficientFunds = Error.Create("Wallet.InsufficientFunds", "The available balance is too low.");

    public static readonly Error TransactionNotFound = Error.Create("Transaction.NotFound", "No matching transaction.");

    public static readonly Error DuplicateReference = Error.Create("Transaction.DuplicateReference", "This referenceId was already used for a different request.");

    public static readonly Error ConcurrencyExhausted = Error.Create("Concurrency.Exhausted", "The wallet kept changing concurrently; retry the request.");

    public static readonly Error HoldMissing = Error.Create("Ledger.HoldMissing", "There is no matching hold for this amount.");

    public static readonly Error SameAccount = Error.Create("Ledger.SameAccount", "A posting needs two different accounts.");

    public static readonly Error UnknownWebhookEvent = Error.Create("Validation.Event", "Unknown webhook event.");

    public static Error Validation(string field, string message) => Error.Create($"Validation.{field}", message);
}
