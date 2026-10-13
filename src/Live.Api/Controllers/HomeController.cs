using Microsoft.AspNetCore.Mvc;

namespace Live.Api.Controllers;

public class HomeController : Controller
{
    public IActionResult Index()
    {
        return Ok("Welcome to the Live API!");
    }
}