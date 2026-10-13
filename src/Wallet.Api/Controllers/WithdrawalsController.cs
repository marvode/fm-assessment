using Wallet.Ledger.Withdrawals;
using Microsoft.AspNetCore.Mvc;

namespace Wallet.Api.Controllers;

[ApiController]
[Route("users/{userId:guid}/withdrawals")]
public sealed class WithdrawalsController : ControllerBase
{
    private readonly RequestWithdrawalHandler _request;

    public WithdrawalsController(RequestWithdrawalHandler request)
    {
        _request = request;
    }

    [HttpPost]
    public async Task<IActionResult> RequestAsync(Guid userId, WithdrawalRequest request, CancellationToken cancellationToken)
    {
        var command = new RequestWithdrawalCommand
        {
            UserId = userId,
            ReferenceId = request.ReferenceId,
            Amount = request.Amount,
            BankCode = request.BankCode,
            AccountNumber = request.AccountNumber,
            AccountName = request.AccountName,
        };
        var result = await _request.HandleAsync(command, cancellationToken);
        if (result.IsFailure)
        {
            return this.ToProblem(result.Error);
        }

        return result.Value.Replayed ? Ok(result.Value.Transaction) : Accepted(result.Value.Transaction);
    }
}
