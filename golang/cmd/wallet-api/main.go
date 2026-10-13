// Command wallet-api serves the wallet HTTP API and runs its background workers. `wallet-api migrate` applies the
// database migrations and exits.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/app"
	"wallet/internal/config"
	"wallet/internal/ledger/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("wallet-api failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case len(args) == 0:
		a, err := app.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		return a.Run(ctx)
	case args[0] == "migrate":
		pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		return store.Migrate(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (run with no arguments, or \"migrate\")", args[0])
	}
}
