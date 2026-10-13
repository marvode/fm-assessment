using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace Live.Wallet.Ledger;

/// <summary>A stable fingerprint of a request's meaningful fields, stored with the transaction for idempotency checks.</summary>
public static class RequestHash
{
    public static string Compute(params object[] parts)
    {
        var canonical = string.Join('|', parts.Select(part => Convert.ToString(part, CultureInfo.InvariantCulture)));
        return Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(canonical)));
    }
}
