using Microsoft.AspNetCore.Mvc;
using Practice.Common;

namespace Live.Api.Controllers;

/// <summary>Turns a handler's Error into RFC 7807 ProblemDetails. The title carries the stable error code.</summary>
public static class ProblemResults
{
    public static int StatusCodeFor(Error error) => error.Code switch
    {
        "Money.InvalidAmount" or "Money.UnsupportedCurrency" => StatusCodes.Status400BadRequest,
        "Wallet.NotFound" or "Transaction.NotFound" => StatusCodes.Status404NotFound,
        "Transaction.DuplicateReference" or "Concurrency.Exhausted" => StatusCodes.Status409Conflict,
        "Wallet.InsufficientFunds" => StatusCodes.Status422UnprocessableEntity,
        var code when code.StartsWith("Validation.", StringComparison.Ordinal) => StatusCodes.Status400BadRequest,
        var code when code.StartsWith("Ledger.", StringComparison.Ordinal) => StatusCodes.Status409Conflict,
        _ => StatusCodes.Status500InternalServerError,
    };

    public static ObjectResult ToProblem(this ControllerBase controller, Error error) =>
        controller.Problem(title: error.Code, detail: error.Message, statusCode: StatusCodeFor(error));
}
