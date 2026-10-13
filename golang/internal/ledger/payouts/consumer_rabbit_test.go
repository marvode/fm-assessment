package payouts_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

// runConsumer starts a consumer against the test broker and returns a function that stops it.
func runConsumer(t *testing.T, broker *ledgertest.RabbitMQ, handler handlerFunc) (stop func()) {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	c := payouts.NewConsumer(conn, handler, ledgertest.Logger())
	c.RetryDelay = 0
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(runCtx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		require.NoError(t, <-done)
		_ = conn.Close()
	}
	t.Cleanup(stop)
	return stop
}

func publish(t *testing.T, broker *ledgertest.RabbitMQ) {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	defer conn.Close()
	body := []byte(`{"transactionId":"` + uuid.NewString() + `"}`)
	require.NoError(t, outbox.NewRabbitPublisher(conn).Publish(ctx, messages.PayoutRequestedType, body, uuid.New()))
}

func TestTheConsumerAcksAProcessedMessage(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	stop := runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return payouts.Initiated, nil
	})

	publish(t, broker)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 10*time.Second, 50*time.Millisecond)
	stop() // an unacked message would return to the queue when the channel closes

	require.Zero(t, broker.Messages(t, payouts.QueueName))
	require.Zero(t, broker.Messages(t, payouts.DeadLetterQueue))
}

func TestTheConsumerDeadLettersAMessageThatKeepsFailing(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return "", errors.New("provider down")
	})

	publish(t, broker)

	require.Eventually(t, func() bool { return broker.Messages(t, payouts.DeadLetterQueue) == 1 }, 30*time.Second, 100*time.Millisecond)
	require.Equal(t, int32(payouts.DeliveryLimit), calls.Load())
	require.Zero(t, broker.Messages(t, payouts.QueueName))
}

func TestTheConsumerDeadLettersAnUnreadableMessageImmediately(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	var calls atomic.Int32
	runConsumer(t, broker, func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		calls.Add(1)
		return payouts.Initiated, nil
	})

	broker.PublishRaw(t, []byte("not json"))

	require.Eventually(t, func() bool { return broker.Messages(t, payouts.DeadLetterQueue) == 1 }, 10*time.Second, 50*time.Millisecond)
	require.Zero(t, calls.Load())
}
