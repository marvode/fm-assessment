using Live.Wallet.Wallets;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/wallet")]
public sealed class WalletsController : ControllerBase
{
    private readonly CreateWalletHandler _create;
    private readonly GetWalletHandler _get;
    private readonly ListTransactionsHandler _list;

    public WalletsController(CreateWalletHandler create, GetWalletHandler get, ListTransactionsHandler list)
    {
        _create = create;
        _get = get;
        _list = list;
    }

    [HttpPost]
    public async Task<IActionResult> CreateAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _create.HandleAsync(userId, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Created
            ? Created($"/users/{userId}/wallet", result.Value.Wallet)
            : Ok(result.Value.Wallet);
    }

    [HttpGet]
    public async Task<IActionResult> GetAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _get.HandleAsync(userId, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }

    [HttpGet("transactions")]
    public async Task<IActionResult> ListTransactionsAsync(Guid userId, CancellationToken cancellationToken)
    {
        var result = await _list.HandleAsync(userId, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
