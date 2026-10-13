using Live.Wallet.TopUps;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/top-ups")]
public sealed class TopUpsController : ControllerBase
{
    private readonly InitiateTopUpHandler _initiate;

    public TopUpsController(InitiateTopUpHandler initiate)
    {
        _initiate = initiate;
    }

    [HttpPost]
    public async Task<IActionResult> InitiateAsync(Guid userId, TopUpRequest request, CancellationToken cancellationToken)
    {
        var command = new InitiateTopUpCommand { UserId = userId, ReferenceId = request.ReferenceId, Amount = request.Amount };
        var result = await _initiate.HandleAsync(command, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Replayed
            ? Ok(result.Value.Transaction)
            : Created($"/users/{userId}/wallet/transactions", result.Value.Transaction);
    }
}
