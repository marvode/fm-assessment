namespace Live.Wallet.Withdrawals;

/// <summary>Where a withdrawal pays out to. One row per withdrawal transaction.</summary>
public sealed class WithdrawalDetails
{
    private WithdrawalDetails()
    {
        BankCode = string.Empty;
        AccountNumber = string.Empty;
        AccountName = string.Empty;
    }

    public Guid TransactionId { get; private set; }

    public string BankCode { get; private set; }

    public string AccountNumber { get; private set; }

    public string AccountName { get; private set; }

    public static WithdrawalDetails Create(Guid transactionId, string bankCode, string accountNumber, string accountName) =>
        new() { TransactionId = transactionId, BankCode = bankCode, AccountNumber = accountNumber, AccountName = accountName };
}
