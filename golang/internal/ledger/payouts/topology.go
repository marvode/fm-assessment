package payouts

import amqp "github.com/rabbitmq/amqp091-go"

// The payout queue is a durable quorum queue. RabbitMQ counts redeliveries and dead-letters a message after
// DeliveryLimit attempts. Declaring is idempotent; the publisher and the consumer both declare before use.
const (
	QueueName          = "wallet.payouts"
	DeadLetterExchange = "wallet.payouts.dlx"
	DeadLetterQueue    = "wallet.payouts.dead"
	DeliveryLimit      = 5
)

func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(DeadLetterExchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(DeadLetterQueue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(DeadLetterQueue, "", DeadLetterExchange, false, nil); err != nil {
		return err
	}
	_, err := ch.QueueDeclare(QueueName, true, false, false, false, amqp.Table{
		"x-queue-type":           "quorum",
		"x-delivery-limit":       int32(DeliveryLimit),
		"x-dead-letter-exchange": DeadLetterExchange,
	})
	return err
}
