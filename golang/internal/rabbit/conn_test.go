package rabbit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/rabbit"
)

func TestConnDialsLazilyAndRedialsAfterTheBrokerClosesIt(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	t.Cleanup(func() { _ = conn.Close() })
	require.False(t, conn.IsOpen())

	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, ch.Close())
	require.True(t, conn.IsOpen())

	broker.CloseAllConnections(t)
	require.Eventually(t, func() bool { return !conn.IsOpen() }, 10*time.Second, 50*time.Millisecond)

	ch, err = conn.Channel()
	require.NoError(t, err)
	require.NoError(t, ch.Close())
	require.True(t, conn.IsOpen())
}

func TestAnUnreachableBrokerIsAnErrorNotAPanic(t *testing.T) {
	conn := rabbit.New("amqp://guest:guest@127.0.0.1:1/", ledgertest.Logger())

	_, err := conn.Channel()

	require.Error(t, err)
	require.False(t, conn.IsOpen())
}
