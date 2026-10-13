using Microsoft.EntityFrameworkCore;
using Npgsql;

namespace Wallet.Ledger.Persistence;

internal static class DbErrors
{
    /// <summary>A concurrent writer committed the same unique key first (Postgres 23505).</summary>
    public static bool IsUniqueViolation(DbUpdateException exception) =>
        exception.InnerException is PostgresException { SqlState: PostgresErrorCodes.UniqueViolation };
}
