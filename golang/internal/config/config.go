// Package config reads the service configuration from environment variables.
package config

import (
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	// AppEnv "development" applies migrations on startup and enables /dev/provider.
	AppEnv      string `env:"APP_ENV" envDefault:"production"`
	HTTPAddr    string `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	// RabbitMQURL is checked by app.New rather than here, so `wallet-api migrate` runs without it.
	RabbitMQURL string `env:"RABBITMQ_URL"`
	Wallet      Wallet `envPrefix:"WALLET_"`
}

// Wallet holds the background worker settings; the defaults are the .NET WalletOptions defaults.
type Wallet struct {
	OutboxPollInterval time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"1s"`
	OutboxBatchSize    int           `env:"OUTBOX_BATCH_SIZE" envDefault:"50"`
	SweeperInterval    time.Duration `env:"SWEEPER_INTERVAL" envDefault:"5m"`
	// PendingThreshold is how long a withdrawal may stay Pending before the sweeper asks the provider about it.
	PendingThreshold time.Duration `env:"PENDING_THRESHOLD" envDefault:"15m"`
}

func Load() (Config, error) { return env.ParseAs[Config]() }

func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }
