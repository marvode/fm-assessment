package reconciliation_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/reconciliation"
)

// scripts/e2e.sh greps these exact log lines.
func TestRunOnceLogsTheSweepSummaryAndEveryViolation(t *testing.T) {
	f := newFixture(t)
	walletID := ledgertest.SeedWallet(t, f.s, f.userID, 10_000, ledgertest.Start)
	f.exec(t, "UPDATE accounts SET balance = 20000 WHERE id = $1", walletID)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	err := reconciliation.RunOnce(ctx, f.sweeper(f.provider), reconciliation.NewChecker(f.s, log), log)

	require.NoError(t, err)
	require.Contains(t, logs.String(), `"msg":"payout sweep","settled":0,"released":0,"redriven":0,"still_pending":0,"errors":0`)
	require.Contains(t, logs.String(), `"msg":"ledger invariant violated","invariant":"I3"`)
}
