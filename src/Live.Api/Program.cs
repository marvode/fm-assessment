using Live.Wallet;
using Live.Wallet.Persistence;
using Microsoft.EntityFrameworkCore;
using Scalar.AspNetCore;

var builder = WebApplication.CreateBuilder(args);

builder.AddServiceDefaults();
builder.AddNpgsqlDbContext<WalletDbContext>("appdb");
builder.AddRabbitMQClient("messaging");

builder.Services.AddWallet(builder.Configuration);
builder.Services.AddWalletBackgroundWorkers();
builder.Services.AddControllers();
builder.Services.AddProblemDetails();
builder.Services.AddOpenApi();

var app = builder.Build();

app.MapDefaultEndpoints();

if (app.Environment.IsDevelopment())
{
    app.MapOpenApi();
    app.MapScalarApiReference();

    await using var scope = app.Services.CreateAsyncScope();
    await scope.ServiceProvider.GetRequiredService<WalletDbContext>().Database.MigrateAsync();
}

app.MapControllers();

await app.RunAsync();
