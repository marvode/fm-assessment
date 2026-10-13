// Package app is the composition root: it builds every component from Config and runs them together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"wallet/internal/api"
	"wallet/internal/common"
	"wallet/internal/config"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/reconciliation"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
	"wallet/internal/rabbit"
)

type App struct {
	cfg      config.Config
	log      *slog.Logger
	pool     *pgxpool.Pool
	rabbit   *rabbit.Conn
	listener net.Listener
	server   *http.Server
	relay    *outbox.Relay
	consumer *payouts.Consumer
	sweeper  *reconciliation.Sweeper
	checker  *reconciliation.Checker
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if cfg.RabbitMQURL == "" {
		return nil, errors.New("RABBITMQ_URL is required")
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.IsDevelopment() {
		if err := store.Migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		pool.Close()
		return nil, err
	}

	s := store.New(pool)
	clock := common.SystemClock{}
	provider := providers.NewFakePaymentProvider()
	conn := rabbit.New(cfg.RabbitMQURL, log)
	settle := withdrawals.NewSettleHandler(s, clock)
	release := withdrawals.NewReleaseHandler(s, clock, log)

	router := api.NewRouter(api.Deps{
		Log:               log,
		Development:       cfg.IsDevelopment(),
		CreateWallet:      wallets.NewCreateHandler(s, clock),
		GetWallet:         wallets.NewGetHandler(s),
		ListTransactions:  wallets.NewListHandler(s),
		InitiateTopUp:     topups.NewInitiateHandler(s, provider, clock),
		RequestWithdrawal: withdrawals.NewRequestHandler(s, clock),
		Webhooks:          webhooks.NewHandler(s, topups.NewCompleteHandler(s, clock, log), settle, release),
		FakeProvider:      provider,
		HealthChecks: []api.HealthCheck{
			pool.Ping,
			func(context.Context) error {
				if !conn.IsOpen() {
					return errors.New("rabbitmq connection is closed")
				}
				return nil
			},
		},
	})

	return &App{
		cfg:      cfg,
		log:      log,
		pool:     pool,
		rabbit:   conn,
		listener: listener,
		server:   &http.Server{Handler: router, ReadHeaderTimeout: 10 * time.Second},
		relay:    outbox.NewRelay(s, outbox.NewRabbitPublisher(conn), clock, cfg.Wallet.OutboxBatchSize, log),
		consumer: payouts.NewConsumer(conn, payouts.NewProcessor(s, provider, release, log), log),
		sweeper:  reconciliation.NewSweeper(s, provider, settle, release, clock, cfg.Wallet.PendingThreshold, log),
		checker:  reconciliation.NewChecker(s, log),
	}, nil
}

func (a *App) Addr() string { return a.listener.Addr().String() }

// Run serves HTTP and runs the background workers until ctx ends, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	defer a.pool.Close()
	defer func() { _ = a.rabbit.Close() }()

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a.log.Info("listening", "addr", a.Addr())
		if err := a.server.Serve(a.listener); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return a.server.Shutdown(shutdownCtx)
	})
	g.Go(func() error {
		return common.RunEvery(ctx, a.cfg.Wallet.OutboxPollInterval, func(ctx context.Context) error {
			_, err := a.relay.RelayPending(ctx)
			return err
		}, func(err error) { a.log.Error("outbox relay run failed", "error", err) })
	})
	g.Go(func() error { return a.consumer.Run(ctx) })
	g.Go(func() error {
		return common.RunEvery(ctx, a.cfg.Wallet.SweeperInterval, func(ctx context.Context) error {
			return reconciliation.RunOnce(ctx, a.sweeper, a.checker, a.log)
		}, func(err error) { a.log.Error("reconciliation run failed", "error", err) })
	})
	return g.Wait()
}
