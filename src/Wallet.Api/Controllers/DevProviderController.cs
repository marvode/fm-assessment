using Wallet.Ledger.Providers;
using Wallet.Ledger.Webhooks;
using Microsoft.AspNetCore.Mvc;

namespace Wallet.Api.Controllers;

/// <summary>Development only: plays the fake provider's webhook so the whole flow can be tried by hand.</summary>
[ApiController]
[Route("dev/provider")]
public sealed class DevProviderController : ControllerBase
{
    private readonly IHostEnvironment _environment;
    private readonly FakePaymentProvider _provider;
    private readonly ProviderWebhookHandler _webhooks;

    public DevProviderController(IHostEnvironment environment, FakePaymentProvider provider, ProviderWebhookHandler webhooks)
    {
        _environment = environment;
        _provider = provider;
        _webhooks = webhooks;
    }

    [HttpPost("{providerReferenceId}/complete")]
    public async Task<IActionResult> CompleteAsync(string providerReferenceId, [FromQuery] string outcome, CancellationToken cancellationToken)
    {
        if (!_environment.IsDevelopment())
        {
            return NotFound();
        }

        var succeeded = outcome switch
        {
            "success" => true,
            "failed" => false,
            _ => (bool?)null,
        };
        if (succeeded is not { } isSuccess)
        {
            return Problem(title: "Validation.Outcome", detail: "outcome must be 'success' or 'failed'.", statusCode: StatusCodes.Status400BadRequest);
        }

        var record = _provider.FindByProviderReference(providerReferenceId);
        if (record is null)
        {
            return NotFound();
        }

        if (record.IsPayout)
        {
            _provider.SetPayoutStatus(record.ReferenceId, isSuccess ? PayoutStatus.Succeeded : PayoutStatus.Failed);
        }

        var webhook = new ProviderWebhook
        {
            Event = (record.IsPayout, isSuccess) switch
            {
                (false, true) => ProviderWebhookEvents.ChargeSuccess,
                (false, false) => ProviderWebhookEvents.ChargeFailed,
                (true, true) => ProviderWebhookEvents.TransferSuccess,
                (true, false) => ProviderWebhookEvents.TransferFailed,
            },
            ProviderReferenceId = providerReferenceId,
            Reference = record.ReferenceId,
            Amount = record.Amount,
        };
        var result = await _webhooks.HandleAsync(webhook, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
