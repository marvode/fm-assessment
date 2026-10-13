using System.Net;
using System.Net.Http.Json;
using FluentAssertions;
using Wallet.Ledger.Wallets;
using Microsoft.AspNetCore.Mvc;

namespace Wallet.Tests.Api;

public sealed class WalletApiTests : IClassFixture<WalletApiFactory>
{
    private readonly HttpClient _client;

    public WalletApiTests(WalletApiFactory factory)
    {
        _client = factory.CreateClient();
    }

    [Fact]
    public async Task Creating_a_wallet_returns_201_then_200()
    {
        var userId = Guid.NewGuid();

        var created = await _client.PostAsync($"/users/{userId}/wallet", content: null);
        var existing = await _client.PostAsync($"/users/{userId}/wallet", content: null);

        created.StatusCode.Should().Be(HttpStatusCode.Created);
        existing.StatusCode.Should().Be(HttpStatusCode.OK);
        (await ReadAsync<WalletResponse>(existing)).Currency.Should().Be("NGN");
    }

    [Fact]
    public async Task An_unknown_wallet_returns_a_404_problem()
    {
        var response = await _client.GetAsync($"/users/{Guid.NewGuid()}/wallet");

        response.StatusCode.Should().Be(HttpStatusCode.NotFound);
        response.Content.Headers.ContentType?.MediaType.Should().Be("application/problem+json");
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Wallet.NotFound");
    }

    [Fact]
    public async Task A_top_up_confirmed_by_webhook_credits_the_wallet()
    {
        var userId = Guid.NewGuid();

        await FundAsync(userId, 5_000);

        var wallet = await ReadAsync<WalletResponse>(await _client.GetAsync($"/users/{userId}/wallet"));
        wallet.Balance.Should().Be(5_000);
        wallet.AvailableBalance.Should().Be(5_000);
        var history = await ReadAsync<TransactionResponse[]>(await _client.GetAsync($"/users/{userId}/wallet/transactions"));
        history.Should().ContainSingle(t => t.Type == "TopUp" && t.Status == "Succeeded");
    }

    [Fact]
    public async Task A_withdrawal_returns_202_and_an_identical_retry_returns_200()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 10_000);
        var body = Withdrawal("wd-retry", 4_000);

        var first = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", body);
        var retry = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", body);

        first.StatusCode.Should().Be(HttpStatusCode.Accepted);
        retry.StatusCode.Should().Be(HttpStatusCode.OK);
        (await ReadAsync<TransactionResponse>(retry)).Id.Should().Be((await ReadAsync<TransactionResponse>(first)).Id);
        (await ReadAsync<WalletResponse>(await _client.GetAsync($"/users/{userId}/wallet"))).AvailableBalance.Should().Be(6_000);
    }

    [Fact]
    public async Task A_withdrawal_beyond_the_available_balance_returns_422()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 1_000);

        var response = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-big", 1_001));

        response.StatusCode.Should().Be(HttpStatusCode.UnprocessableEntity);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Wallet.InsufficientFunds");
    }

    [Fact]
    public async Task Reusing_a_reference_with_a_different_body_returns_409()
    {
        var userId = Guid.NewGuid();
        await FundAsync(userId, 10_000);
        await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-reused", 1_000));

        var response = await _client.PostAsJsonAsync($"/users/{userId}/withdrawals", Withdrawal("wd-reused", 2_000));

        response.StatusCode.Should().Be(HttpStatusCode.Conflict);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Transaction.DuplicateReference");
    }

    [Fact]
    public async Task A_non_positive_amount_returns_400()
    {
        var response = await _client.PostAsJsonAsync($"/users/{Guid.NewGuid()}/top-ups", new { referenceId = "zero", amount = 0 });

        response.StatusCode.Should().Be(HttpStatusCode.BadRequest);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Money.InvalidAmount");
    }

    [Fact]
    public async Task An_unknown_webhook_event_returns_400()
    {
        var response = await _client.PostAsJsonAsync("/webhooks/provider", new { @event = "refund.created", providerReferenceId = "x", amount = 1 });

        response.StatusCode.Should().Be(HttpStatusCode.BadRequest);
        (await ReadAsync<ProblemDetails>(response)).Title.Should().Be("Validation.Event");
    }

    [Fact]
    public async Task The_dev_provider_endpoint_is_hidden_outside_development()
    {
        var response = await _client.PostAsync("/dev/provider/chg_anything/complete?outcome=success", content: null);

        response.StatusCode.Should().Be(HttpStatusCode.NotFound);
    }

    private static object Withdrawal(string referenceId, long amount) =>
        new { referenceId, amount, bankCode = "058", accountNumber = "0123456789", accountName = "Ada Lovelace" };

    private async Task FundAsync(Guid userId, long amount)
    {
        var started = await _client.PostAsJsonAsync($"/users/{userId}/top-ups", new { referenceId = $"topup-{Guid.NewGuid():N}", amount });
        started.StatusCode.Should().Be(HttpStatusCode.Created);
        var topUp = await ReadAsync<TransactionResponse>(started);
        var confirmed = await _client.PostAsJsonAsync("/webhooks/provider", new
        {
            @event = "charge.success",
            providerReferenceId = topUp.ProviderReferenceId,
            amount,
        });
        confirmed.StatusCode.Should().Be(HttpStatusCode.OK);
    }

    private static async Task<T> ReadAsync<T>(HttpResponseMessage response) =>
        await response.Content.ReadFromJsonAsync<T>()
        ?? throw new InvalidOperationException($"Expected a {typeof(T).Name} body.");
}
