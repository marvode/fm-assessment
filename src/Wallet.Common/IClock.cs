namespace Wallet.Common;

public interface IClock
{
    DateTimeOffset UtcNow { get; }
}
