using Wallet.Ledger.Domain;
using Wallet.Ledger.Persistence;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Logging;

namespace Wallet.Ledger.Reconciliation;

/// <summary>Checks the ledger invariants and logs every violation. It never corrects anything: drift needs a human.</summary>
public sealed partial class LedgerInvariantChecker
{
    private readonly WalletDbContext _db;
    private readonly ILogger<LedgerInvariantChecker> _logger;

    public LedgerInvariantChecker(WalletDbContext db, ILogger<LedgerInvariantChecker> logger)
    {
        _db = db;
        _logger = logger;
    }

    public async Task<IReadOnlyList<InvariantViolation>> CheckAsync(CancellationToken cancellationToken)
    {
        List<InvariantViolation> violations = [];

        var unbalanced = await _db.LedgerEntries
            .GroupBy(e => e.TransactionId)
            .Select(g => new
            {
                TransactionId = g.Key,
                Count = g.Count(),
                DebitCount = g.Count(e => e.Direction == EntryDirection.Debit),
                Debits = g.Where(e => e.Direction == EntryDirection.Debit).Sum(e => e.Amount),
                Credits = g.Where(e => e.Direction == EntryDirection.Credit).Sum(e => e.Amount),
            })
            .Where(x => x.Count != 2 || x.DebitCount != 1 || x.Debits != x.Credits)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(unbalanced.Select(x => new InvariantViolation
        {
            Invariant = "I1",
            Detail = $"Transaction {x.TransactionId} has {x.Count} entries (debits {x.Debits}, credits {x.Credits}).",
        }));

        var drifted = await _db.Accounts
            .Select(a => new
            {
                a.Id,
                a.Balance,
                Net = _db.LedgerEntries.Where(e => e.AccountId == a.Id)
                    .Sum(e => e.Direction == EntryDirection.Credit ? e.Amount : -e.Amount),
            })
            .Where(x => x.Balance != x.Net)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(drifted.Select(x => new InvariantViolation
        {
            Invariant = "I3",
            Detail = $"Account {x.Id} balance {x.Balance} differs from its entries' net {x.Net}.",
        }));

        var invalidWallets = await _db.Accounts
            .Where(a => a.Type == AccountType.User && (a.Balance < 0 || a.AvailableBalance < 0 || a.AvailableBalance > a.Balance))
            .Select(a => new { a.Id, a.Balance, a.AvailableBalance })
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        violations.AddRange(invalidWallets.Select(x => new InvariantViolation
        {
            Invariant = "I4",
            Detail = $"Wallet {x.Id} has balance {x.Balance} and available {x.AvailableBalance}.",
        }));

        var total = await _db.Accounts.SumAsync(a => a.Balance, cancellationToken).ConfigureAwait(false);
        if (total != 0)
        {
            violations.Add(new InvariantViolation { Invariant = "I6", Detail = $"Account balances sum to {total}, not 0." });
        }

        foreach (var violation in violations)
        {
            LogViolation(_logger, violation.Invariant, violation.Detail);
        }

        return violations;
    }

    [LoggerMessage(Level = LogLevel.Error, Message = "Ledger invariant {Invariant} violated: {Detail}")]
    private static partial void LogViolation(ILogger logger, string invariant, string detail);
}
