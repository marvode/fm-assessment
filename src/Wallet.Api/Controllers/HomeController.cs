using Microsoft.AspNetCore.Mvc;

namespace Wallet.Api.Controllers;

public class HomeController : Controller
{
    public IActionResult Index()
    {
        return Ok("Welcome to the Live API!");
    }
}