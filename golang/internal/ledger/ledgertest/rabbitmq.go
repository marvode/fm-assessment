package ledgertest

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"wallet/internal/ledger/payouts"
)

type RabbitMQ struct {
	URL       string
	container *rabbitmq.RabbitMQContainer
}

var (
	rmqOnce sync.Once
	rmq     *RabbitMQ
	rmqErr  error
)

// NewRabbitMQ returns the test broker (WALLET_TEST_RABBITMQ_URL, or a rabbitmq:4-management container started once
// per test binary) with the payout topology declared and its queues purged.
func NewRabbitMQ(t *testing.T) *RabbitMQ {
	t.Helper()
	rmqOnce.Do(func() { rmq, rmqErr = setupRabbitMQ(context.Background()) })
	require.NoError(t, rmqErr)
	rmq.withChannel(t, func(ch *amqp.Channel) {
		require.NoError(t, payouts.DeclareTopology(ch))
		_, err := ch.QueuePurge(payouts.QueueName, false)
		require.NoError(t, err)
		_, err = ch.QueuePurge(payouts.DeadLetterQueue, false)
		require.NoError(t, err)
	})
	return rmq
}

func setupRabbitMQ(ctx context.Context) (*RabbitMQ, error) {
	if url := os.Getenv("WALLET_TEST_RABBITMQ_URL"); url != "" {
		return &RabbitMQ{URL: url}, nil
	}
	container, err := rabbitmq.Run(ctx, "rabbitmq:4-management")
	if err != nil {
		return nil, err
	}
	url, err := container.AmqpURL(ctx)
	if err != nil {
		return nil, err
	}
	return &RabbitMQ{URL: url, container: container}, nil
}

// Messages returns the number of ready messages in queue.
func (r *RabbitMQ) Messages(t *testing.T, queue string) int {
	t.Helper()
	var n int
	r.withChannel(t, func(ch *amqp.Channel) {
		q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
		require.NoError(t, err)
		n = q.Messages
	})
	return n
}

// Get takes one message off queue, waiting up to 10s for it.
func (r *RabbitMQ) Get(t *testing.T, queue string) amqp.Delivery {
	t.Helper()
	var delivery amqp.Delivery
	r.withChannel(t, func(ch *amqp.Channel) {
		require.Eventually(t, func() bool {
			d, ok, err := ch.Get(queue, true)
			require.NoError(t, err)
			delivery = d
			return ok
		}, 10*time.Second, 50*time.Millisecond)
	})
	return delivery
}

// PublishRaw puts body on the payout queue without going through the wallet's publisher.
func (r *RabbitMQ) PublishRaw(t *testing.T, body []byte) {
	t.Helper()
	r.withChannel(t, func(ch *amqp.Channel) {
		require.NoError(t, ch.PublishWithContext(context.Background(), "", payouts.QueueName, false, false,
			amqp.Publishing{Body: body, MessageId: "raw"}))
	})
}

// CloseAllConnections forcibly closes every client connection, as a broker restart would.
func (r *RabbitMQ) CloseAllConnections(t *testing.T) {
	t.Helper()
	if r.container == nil {
		t.Skip("needs a broker container started by ledgertest")
	}
	code, _, err := r.container.Exec(context.Background(), []string{"rabbitmqctl", "close_all_connections", "test"})
	require.NoError(t, err)
	require.Zero(t, code)
}

func (r *RabbitMQ) withChannel(t *testing.T, use func(*amqp.Channel)) {
	t.Helper()
	conn, err := amqp.Dial(r.URL)
	require.NoError(t, err)
	defer conn.Close()
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()
	use(ch)
}
