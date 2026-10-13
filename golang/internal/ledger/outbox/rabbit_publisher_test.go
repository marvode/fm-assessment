package outbox_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

func newRabbitPublisher(t *testing.T, broker *ledgertest.RabbitMQ) *outbox.RabbitPublisher {
	t.Helper()
	conn := rabbit.New(broker.URL, ledgertest.Logger())
	t.Cleanup(func() { _ = conn.Close() })
	return outbox.NewRabbitPublisher(conn)
}

func TestTheRabbitPublisherDeliversPersistentConfirmedMessages(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	publisher := newRabbitPublisher(t, broker)
	id := uuid.New()

	require.NoError(t, publisher.Publish(ctx, messages.PayoutRequestedType, []byte(`{"transactionId":"x"}`), id))

	delivered := broker.Get(t, payouts.QueueName)
	require.Equal(t, id.String(), delivered.MessageId)
	require.Equal(t, messages.PayoutRequestedType, delivered.Type)
	require.Equal(t, "application/json", delivered.ContentType)
	require.Equal(t, amqp.Persistent, delivered.DeliveryMode)
	require.JSONEq(t, `{"transactionId":"x"}`, string(delivered.Body))
}

func TestTheRabbitPublisherRejectsUnknownMessageTypes(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)

	err := newRabbitPublisher(t, broker).Publish(ctx, "test.message", []byte("{}"), uuid.New())

	require.ErrorContains(t, err, "no queue is configured")
}

func TestRabbitPublisherRecoversAfterTheConnectionDrops(t *testing.T) {
	broker := ledgertest.NewRabbitMQ(t)
	publisher := newRabbitPublisher(t, broker)
	require.NoError(t, publisher.Publish(ctx, messages.PayoutRequestedType, []byte("{}"), uuid.New()))

	broker.CloseAllConnections(t)

	require.Eventually(t, func() bool {
		return publisher.Publish(ctx, messages.PayoutRequestedType, []byte("{}"), uuid.New()) == nil
	}, 20*time.Second, 200*time.Millisecond)
}
