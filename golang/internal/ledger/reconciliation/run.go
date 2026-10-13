package reconciliation

import (
	"context"
	"log/slog"
)

// RunOnce is one reconciliation pass: sweep stale payouts, log the summary, then check the ledger invariants.
func RunOnce(ctx context.Context, sweeper *Sweeper, checker *Checker, log *slog.Logger) error {
	summary, err := sweeper.Sweep(ctx)
	if err != nil {
		return err
	}
	log.Info("payout sweep",
		"settled", summary.Settled,
		"released", summary.Released,
		"redriven", summary.Redriven,
		"still_pending", summary.StillPending,
		"errors", summary.Errors)
	_, err = checker.Check(ctx)
	return err
}
