namespace Wallet.Common;

/// <summary>A clock tests move by hand, so expiry is tested without sleeping.</summary>
public sealed class FakeClock : IClock
{
    public FakeClock()
        : this(new DateTimeOffset(2026, 1, 1, 0, 0, 0, TimeSpan.Zero))
    {
    }

    public FakeClock(DateTimeOffset start)
    {
        UtcNow = start;
    }

    public DateTimeOffset UtcNow { get; private set; }

    public void Advance(TimeSpan by) => UtcNow += by;
}
