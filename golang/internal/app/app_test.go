package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"wallet/internal/app"
	"wallet/internal/config"
	"wallet/internal/ledger/ledgertest"
)

type running struct {
	base string
	pool *pgxpool.Pool
	stop func() error
}

func start(t *testing.T, rabbitURL string) *running {
	t.Helper()
	pool := ledgertest.NewPool(t)
	cfg := config.Config{
		AppEnv: "development", HTTPAddr: "127.0.0.1:0",
		DatabaseURL: pool.Config().ConnString(), RabbitMQURL: rabbitURL,
		Wallet: config.Wallet{
			OutboxPollInterval: 50 * time.Millisecond, OutboxBatchSize: 50,
			SweeperInterval: time.Hour, PendingThreshold: 15 * time.Minute,
		},
	}
	a, err := app.New(context.Background(), cfg, ledgertest.Logger())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(15 * time.Second):
				stopErr = errors.New("Run did not return within 15s of cancellation")
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	return &running{base: "http://" + a.Addr(), pool: pool, stop: stop}
}

func (r *running) call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, r.base+path, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(text)
}

func (r *running) fundAndWithdraw(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	status, body := r.call(t, http.MethodPost, "/users/"+userID.String()+"/top-ups", `{"referenceId":"tu-1","amount":9000}`)
	require.Equal(t, http.StatusCreated, status, body)
	var topUp struct {
		ProviderReferenceID string `json:"providerReferenceId"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &topUp))
	status, body = r.call(t, http.MethodPost, "/webhooks/provider",
		`{"event":"charge.success","providerReferenceId":"`+topUp.ProviderReferenceID+`","amount":9000}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = r.call(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals",
		`{"referenceId":"wd-1","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}`)
	require.Equal(t, http.StatusAccepted, status, body)
	var withdrawal struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &withdrawal))
	return withdrawal.ID
}

func TestRunServesAndShutsDownCleanly(t *testing.T) {
	r := start(t, ledgertest.NewRabbitMQ(t).URL)

	status, body := r.call(t, http.MethodGet, "/alive", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "Healthy", body)
	require.Eventually(t, func() bool {
		status, _ := r.call(t, http.MethodGet, "/health", "")
		return status == http.StatusOK
	}, 15*time.Second, 100*time.Millisecond)

	require.NoError(t, r.stop())
}

func TestAWithdrawalFlowsThroughRabbitMQToTheProvider(t *testing.T) {
	r := start(t, ledgertest.NewRabbitMQ(t).URL)

	withdrawalID := r.fundAndWithdraw(t, uuid.New())

	require.Eventually(t, func() bool {
		var providerReferenceID *string
		err := r.pool.QueryRow(context.Background(),
			"SELECT provider_reference_id FROM transactions WHERE id = $1", withdrawalID).Scan(&providerReferenceID)
		return err == nil && providerReferenceID != nil && strings.HasPrefix(*providerReferenceID, "trf_")
	}, 20*time.Second, 100*time.Millisecond)
}

func TestTheAPIServesWhileRabbitMQIsDown(t *testing.T) {
	r := start(t, "amqp://guest:guest@127.0.0.1:1/")

	status, _ := r.call(t, http.MethodGet, "/alive", "")
	require.Equal(t, http.StatusOK, status)
	status, body := r.call(t, http.MethodGet, "/health", "")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "Unhealthy", body)

	withdrawalID := r.fundAndWithdraw(t, uuid.New())

	require.Eventually(t, func() bool {
		var attempts int32
		err := r.pool.QueryRow(context.Background(),
			"SELECT attempts FROM outbox_messages WHERE processed_at IS NULL AND payload->>'transactionId' = $1",
			withdrawalID).Scan(&attempts)
		return err == nil && attempts >= 1
	}, 10*time.Second, 100*time.Millisecond, "the relay records failed publishes and keeps the message")
	require.NoError(t, r.stop())
}
