package payouts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"wallet/internal/ledger/messages"
	"wallet/internal/rabbit"
)

const (
	prefetch   = 10
	maxBackoff = 30 * time.Second
)

// PayoutHandler is what the consumer feeds; Processor implements it.
type PayoutHandler interface {
	Process(ctx context.Context, message messages.PayoutRequested) (Outcome, error)
}

// Retrier puts a failed delivery back on the queue as a new message carrying its attempt count.
type Retrier interface {
	Retry(ctx context.Context, delivery amqp.Delivery, attempts int32) error
}

// AttemptsHeader counts failed processing attempts. RabbitMQ 4 quorum queues do not count AMQP 0.9.1
// nack-with-requeue toward x-delivery-limit, so the consumer counts attempts itself.
const AttemptsHeader = "x-wallet-attempts"

// Consumer feeds wallet.payouts messages to the handler one at a time. It acks on success. A failure is retried by
// republishing the message with its attempt count, until DeliveryLimit failures dead-letter it. Unreadable messages
// go straight to the dead-letter queue.
type Consumer struct {
	conn    *rabbit.Conn
	handler PayoutHandler
	log     *slog.Logger
	// RetryDelay is a short pause before a retry so a struggling provider isn't hammered.
	RetryDelay time.Duration
}

func NewConsumer(conn *rabbit.Conn, handler PayoutHandler, log *slog.Logger) *Consumer {
	return &Consumer{conn: conn, handler: handler, log: log, RetryDelay: 2 * time.Second}
}

// Run consumes until ctx ends, reconnecting with exponential backoff whenever the connection or channel drops.
func (c *Consumer) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		subscribed, err := c.consume(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if subscribed {
			backoff = time.Second
		}
		c.log.Warn("payout consumer disconnected; reconnecting", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (c *Consumer) consume(ctx context.Context) (subscribed bool, err error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return false, err
	}
	defer ch.Close()
	if err := DeclareTopology(ch); err != nil {
		return false, err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return false, err
	}
	// Retries are published on their own confirm-mode channel so a confirm never interleaves with consumption.
	retryCh, err := c.conn.Channel()
	if err != nil {
		return false, err
	}
	defer retryCh.Close()
	if err := retryCh.Confirm(false); err != nil {
		return false, err
	}
	retrier := channelRetrier{ch: retryCh}
	// Plain Consume, not ConsumeWithContext: on cancellation that sends basic.cancel from its own goroutine, which
	// races the deferred channel.close RPC and can leave Close waiting forever. Closing the channel cancels the
	// consumer on the broker anyway.
	deliveries, err := ch.Consume(QueueName, "", false, false, false, false, nil)
	if err != nil {
		return false, err
	}
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return true, errors.New("delivery channel closed")
			}
			c.Handle(ctx, delivery, retrier)
		}
	}
}

// Handle processes one delivery and settles it with the broker.
func (c *Consumer) Handle(ctx context.Context, delivery amqp.Delivery, retrier Retrier) {
	var message messages.PayoutRequested
	if err := json.Unmarshal(delivery.Body, &message); err != nil || message.TransactionID == uuid.Nil {
		c.log.Error("dead-lettering unreadable payout message", "message_id", delivery.MessageId, "error", err)
		c.settle(delivery.Nack(false, false))
		return
	}
	outcome, err := c.handler.Process(ctx, message)
	if err == nil {
		c.log.Info("payout processed", "transaction_id", message.TransactionID, "outcome", outcome)
		c.settle(delivery.Ack(false))
		return
	}
	if ctx.Err() != nil {
		return // shutting down: the broker redelivers unacked messages once the channel closes
	}
	attempts := attemptsOf(delivery) + 1
	if attempts >= DeliveryLimit {
		c.log.Error("dead-lettering payout after repeated failures",
			"transaction_id", message.TransactionID, "attempts", attempts, "error", err)
		c.settle(delivery.Nack(false, false))
		return
	}
	c.log.Warn("payout failed; retrying", "transaction_id", message.TransactionID, "attempts", attempts, "error", err)
	select {
	case <-ctx.Done():
		return
	case <-time.After(c.RetryDelay):
	}
	if err := retrier.Retry(ctx, delivery, attempts); err != nil {
		c.log.Warn("republishing payout retry failed; requeueing", "transaction_id", message.TransactionID, "error", err)
		c.settle(delivery.Nack(false, true))
		return
	}
	c.settle(delivery.Ack(false))
}

func attemptsOf(delivery amqp.Delivery) int32 {
	switch v := delivery.Headers[AttemptsHeader].(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case int:
		return int32(v)
	default:
		return 0
	}
}

// channelRetrier republishes to the payout queue on a confirm-mode channel and waits for the broker's confirm.
type channelRetrier struct {
	ch *amqp.Channel
}

func (r channelRetrier) Retry(ctx context.Context, delivery amqp.Delivery, attempts int32) error {
	headers := amqp.Table{}
	for k, v := range delivery.Headers {
		headers[k] = v
	}
	headers[AttemptsHeader] = attempts
	confirm, err := r.ch.PublishWithDeferredConfirmWithContext(ctx, "", QueueName, false, false, amqp.Publishing{
		Headers:      headers,
		DeliveryMode: amqp.Persistent,
		MessageId:    delivery.MessageId,
		Type:         delivery.Type,
		ContentType:  delivery.ContentType,
		Body:         delivery.Body,
	})
	if err != nil {
		return err
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !acked {
		return errors.New("broker nacked the retry")
	}
	return nil
}

func (c *Consumer) settle(err error) {
	if err != nil {
		c.log.Warn("settling payout delivery failed", "error", err)
	}
}
