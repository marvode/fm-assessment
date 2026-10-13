# AGENTS.md — AI Coding Guidelines (.NET 10)

## 1. Project Overview & Tech Stack
- **Framework**: .NET 10 (`net10.0`)
- **Language**: C# 14 (use latest language features: field keyword where applicable, collection expressions, raw string literals)
- **Language**: C# 14 (don't use primary constructors)
- **AI Integration**: `Microsoft.Extensions.AI` / `Microsoft.Extensions.VectorData` (if applicable)
- **Architecture**: Vertical Slice Architecture organized by feature folders (or Clean Architecture with distinct Domain, Application, Infrastructure, and API layers).
- **API Style**: ASP.NET Core MVC controllers (`[ApiController]`, attribute routing). Do not use Minimal APIs (`app.MapGet`/`MapPost`, etc.) for endpoints.
- **Primary Goal**: Keep code decoupled, testable, and strongly typed. Avoid throwing exceptions for control flow; use `Result<T>` or pattern matching.

## 2. Build & Test Commands
Always run and verify via terminal before finishing a task:
- **Restore**: `dotnet restore`
- **Build**: `dotnet build --no-restore`
- **Run Tests**: `dotnet test --no-build --verbosity normal`
- **Format Code**: `dotnet format --verify-no-changes` (or `dotnet format` to apply fixes)
- **Run App**: `dotnet run --project src/MyProject.Api`

## 3. Code Style & C# Conventions
- **Naming**:
  - Classes/Interfaces: `PascalCase` (interfaces start with `I`, e.g., `ICustomerRepository`).
  - Private fields: `_camelCase`.
  - Constants / Static Readonly: `PascalCase` or `UPPER_SNAKE_CASE` as per local repo rules.
- **File Organization**:
  - One primary type per file (match file name to class/interface name).
  - Feature-based grouping: Keep commands, handlers, controllers, and DTOs close inside their feature slice.
- **Dependency Injection**:
  - Register services cleanly in `Program.dev.cs` / `Extensions/ServiceCollectionExtensions.cs`.
  - Prefer constructor injection via explicit constructors assigning `private readonly` fields.
  - Use scoped/singleton lifetimes intentionally; never capture a scoped service in a singleton.
- **Async/Await**:
  - Always suffix async methods with `Async`.
  - Always pass `CancellationToken` through the entire call stack down to EF Core / HttpClient calls.
  - Use `.ConfigureAwait(false)` in library code (or follow application-wide convention).

## 4. .NET 10 & C# Specifics
- Use modern C# collection expressions (`[1, 2, 3]`, `[\..list]`).
- Leverage nullable reference types strictly (`<Nullable>enable</Nullable>`). Do not suppress warnings with `!` unless proven safe with a comment.
- Prefer `System.Text.Json` source generators for high-performance serialization where applicable.

## 5. Testing Instructions
- **Unit Tests**: Use **xUnit** + **FluentAssertions** / **Moq** or **NSubstitute**.
- **Integration Tests**: Use `WebApplicationFactory<TProgram>` with Testcontainers or in-memory/SQLite test databases.
- Every new feature or bug fix **must** include a corresponding unit or integration test.

## 6. Safety, Rules & "Don'ts"
- **DO NOT** delete or modify migration files manually without checking EF Core context.
- **DO NOT** commit secrets, connection strings, or API keys. Use `dotnet user-secrets` or environment variables.
- **DO NOT** use `Task.Result` or `.Wait()` — always `await` asynchronously to prevent deadlocks.
- **DO NOT** use `Console.WriteLine` for logging; inject `ILogger<T>` and use source-generated high-performance logging (`LoggerMessageAttribute`).
- **DO NOT** ignore compiler warnings (`<TreatWarningsAsErrors>true</TreatWarningsAsErrors>`).

## 7. Workflow & Pull Requests
- Keep changes atomic and focused on a single responsibility.
- Before completing, run `dotnet test` and ensure zero errors or regressions.
- Write clear, conventional git commit messages (e.g., `feat(catalog): add product search endpoint`).
