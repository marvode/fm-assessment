package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/config"
)

func TestDefaultsMatchTheDotnetWalletOptions(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/wallet")

	cfg, err := config.Load()

	require.NoError(t, err)
	require.Equal(t, "production", cfg.AppEnv)
	require.False(t, cfg.IsDevelopment())
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, time.Second, cfg.Wallet.OutboxPollInterval)
	require.Equal(t, 50, cfg.Wallet.OutboxBatchSize)
	require.Equal(t, 5*time.Minute, cfg.Wallet.SweeperInterval)
	require.Equal(t, 15*time.Minute, cfg.Wallet.PendingThreshold)
}

func TestEnvironmentVariablesOverrideTheDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/wallet")
	t.Setenv("APP_ENV", "development")
	t.Setenv("WALLET_PENDING_THRESHOLD", "30m")
	t.Setenv("WALLET_SWEEPER_INTERVAL", "5s")

	cfg, err := config.Load()

	require.NoError(t, err)
	require.True(t, cfg.IsDevelopment())
	require.Equal(t, 30*time.Minute, cfg.Wallet.PendingThreshold)
	require.Equal(t, 5*time.Second, cfg.Wallet.SweeperInterval)
}

func TestTheDatabaseURLIsRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	_, err := config.Load()

	require.ErrorContains(t, err, "DATABASE_URL")
}
