using FluentAssertions;
using Practice.Common;

namespace Live.Tests;

public sealed class FakeClockTests
{
    [Fact]
    public void Advance_moves_time_forward_by_exactly_the_given_amount()
    {
        var start = new DateTimeOffset(2026, 10, 13, 12, 0, 0, TimeSpan.Zero);
        var clock = new FakeClock(start);

        clock.Advance(TimeSpan.FromMinutes(5));

        clock.UtcNow.Should().Be(start.AddMinutes(5));
    }
}
