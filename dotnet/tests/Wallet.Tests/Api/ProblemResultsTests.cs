using FluentAssertions;
using Wallet.Api.Controllers;
using Wallet.Common;

namespace Wallet.Tests.Api;

public sealed class ProblemResultsTests
{
    [Theory]
    [InlineData("Money.InvalidAmount", 400)]
    [InlineData("Money.UnsupportedCurrency", 400)]
    [InlineData("Validation.AccountNumber", 400)]
    [InlineData("Wallet.NotFound", 404)]
    [InlineData("Transaction.NotFound", 404)]
    [InlineData("Transaction.DuplicateReference", 409)]
    [InlineData("Concurrency.Exhausted", 409)]
    [InlineData("Ledger.HoldMissing", 409)]
    [InlineData("Wallet.InsufficientFunds", 422)]
    [InlineData("Something.Unexpected", 500)]
    public void Error_codes_map_to_http_statuses(string code, int expectedStatus)
    {
        ProblemResults.StatusCodeFor(Error.Create(code, "message")).Should().Be(expectedStatus);
    }
}
