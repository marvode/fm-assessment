package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
	"wallet/internal/rabbit"
)

// ErrUnroutable means the broker returned a mandatory message because no queue was bound for it.
var ErrUnroutable = errors.New("rabbitmq returned the message as unroutable")

const (
	confirmTimeout = 10 * time.Second
	returnsBuffer  = 64
)

// RabbitPublisher publishes outbox messages with publisher confirms, so Publish returns only after the broker has
// the message. Publishes are serialised on one confirm-mode channel, which is reopened when it closes.
type RabbitPublisher struct {
	conn    *rabbit.Conn
	mu      sync.Mutex
	ch      *amqp.Channel
	returns chan amqp.Return
}

func NewRabbitPublisher(conn *rabbit.Conn) *RabbitPublisher { return &RabbitPublisher{conn: conn} }

func (p *RabbitPublisher) Publish(ctx context.Context, messageType string, payload []byte, messageID uuid.UUID) error {
	if messageType != messages.PayoutRequestedType {
		return fmt.Errorf("no queue is configured for message type %q", messageType)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, err := p.channel()
	if err != nil {
		return err
	}
	for len(p.returns) > 0 { // drop a stale return so it can't be blamed on this message
		<-p.returns
	}

	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", payouts.QueueName, true, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		MessageId:    messageID.String(),
		Type:         messageType,
		ContentType:  "application/json",
		Body:         payload,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", messageID, err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm %s: %w", messageID, err)
	}
	if !acked {
		return fmt.Errorf("broker nacked message %s", messageID)
	}
	// The broker sends basic.return before the ack, and amqp091 delivers it to p.returns before resolving the
	// confirmation, so a return for this message is already buffered.
	return checkReturned(p.returns, messageID)
}

func checkReturned(returns <-chan amqp.Return, messageID uuid.UUID) error {
	select {
	case returned, ok := <-returns:
		if !ok {
			return nil // amqp091 closes the returns channel with the AMQP channel; the confirm already succeeded
		}
		return fmt.Errorf("%w: %s (%d %s)", ErrUnroutable, messageID, returned.ReplyCode, returned.ReplyText)
	default:
		return nil
	}
}

func (p *RabbitPublisher) channel() (*amqp.Channel, error) {
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	ch, err := p.conn.Channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	if err := payouts.DeclareTopology(ch); err != nil {
		_ = ch.Close()
		return nil, err
	}
	// amqp091 sends returns synchronously on the connection's reader goroutine, so a full buffer would stall the
	// whole connection. Publish drains it every time; the headroom covers returns from timed-out publishes.
	p.returns = ch.NotifyReturn(make(chan amqp.Return, returnsBuffer))
	p.ch = ch
	return ch, nil
}
