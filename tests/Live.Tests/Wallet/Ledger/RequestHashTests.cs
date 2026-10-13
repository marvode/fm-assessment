using FluentAssertions;
using Live.Wallet.Ledger;

namespace Live.Tests.Wallet.Ledger;

public sealed class RequestHashTests
{
    [Fact]
    public void Equal_requests_hash_equally()
    {
        var userId = Guid.NewGuid();

        RequestHash.Compute("Withdrawal", userId, 400L).Should().Be(RequestHash.Compute("Withdrawal", userId, 400L));
    }

    [Fact]
    public void Any_difference_changes_the_hash()
    {
        var userId = Guid.NewGuid();

        RequestHash.Compute("Withdrawal", userId, 400L).Should().NotBe(RequestHash.Compute("Withdrawal", userId, 401L));
        RequestHash.Compute("a", "b").Should().NotBe(RequestHash.Compute("b", "a"));
    }
}
