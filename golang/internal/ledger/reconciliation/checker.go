package reconciliation

import (
	"context"
	"fmt"
	"log/slog"

	"wallet/internal/ledger/store"
)

type Violation struct {
	Invariant string
	Detail    string
}

// Checker checks the ledger invariants and logs every violation. It never corrects anything: drift needs a human.
//   - I1: every posted transaction has exactly two entries, one debit, and debits equal credits.
//   - I3: an account's balance equals the net of its entries.
//   - I4: a user wallet's balances are valid (non-negative, available ≤ balance).
//   - I6: all balances sum to zero.
type Checker struct {
	store *store.Store
	log   *slog.Logger
}

func NewChecker(s *store.Store, log *slog.Logger) *Checker { return &Checker{store: s, log: log} }

func (c *Checker) Check(ctx context.Context) ([]Violation, error) {
	q := c.store.Queries()
	var violations []Violation

	unbalanced, err := q.UnbalancedTransactions(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range unbalanced {
		violations = append(violations, Violation{"I1", fmt.Sprintf("Transaction %s has %d entries (debits %d, credits %d).",
			x.TransactionID, x.EntryCount, x.Debits, x.Credits)})
	}

	drifted, err := q.DriftedAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range drifted {
		violations = append(violations, Violation{"I3", fmt.Sprintf("Account %s balance %d differs from its entries' net %d.",
			x.ID, x.Balance, x.Net)})
	}

	invalid, err := q.InvalidWallets(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range invalid {
		violations = append(violations, Violation{"I4", fmt.Sprintf("Wallet %s has balance %d and available %d.",
			x.ID, x.Balance, x.AvailableBalance)})
	}

	total, err := q.TotalBalance(ctx)
	if err != nil {
		return nil, err
	}
	if total != 0 {
		violations = append(violations, Violation{"I6", fmt.Sprintf("Account balances sum to %d, not 0.", total)})
	}

	for _, v := range violations {
		c.log.Error("ledger invariant violated", "invariant", v.Invariant, "detail", v.Detail)
	}
	return violations, nil
}
