using Live.Wallet.Webhooks;
using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

[ApiController]
[Route("webhooks/provider")]
public sealed class ProviderWebhooksController : ControllerBase
{
    private readonly ProviderWebhookHandler _webhooks;

    public ProviderWebhooksController(ProviderWebhookHandler webhooks)
    {
        _webhooks = webhooks;
    }

    [HttpPost]
    public async Task<IActionResult> ReceiveAsync(ProviderWebhook webhook, CancellationToken cancellationToken)
    {
        var result = await _webhooks.HandleAsync(webhook, cancellationToken);
        return result.IsSuccess ? Ok(result.Value) : this.ToProblem(result.Error);
    }
}
