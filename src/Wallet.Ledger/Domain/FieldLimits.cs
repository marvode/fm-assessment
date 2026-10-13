namespace Wallet.Ledger.Domain;

/// <summary>Column sizes, shared by validation and the EF mapping so oversized input is a 400, never a database error.</summary>
public static class FieldLimits
{
    public const int ReferenceIdMaxLength = 128;
    public const int BankCodeMaxLength = 16;
    public const int AccountNumberLength = 10;
    public const int AccountNameMaxLength = 128;
}
