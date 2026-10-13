// Package rabbit holds the RabbitMQ connection shared by the outbox publisher and the payout consumer.
package rabbit

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Conn is a RabbitMQ connection that re-dials on demand. The .NET client recovers connections automatically;
// amqp091-go does not, so callers ask for a new channel whenever theirs has closed and Conn dials again if the
// connection is gone.
type Conn struct {
	url  string
	log  *slog.Logger
	mu   sync.Mutex
	conn *amqp.Connection
}

func New(url string, log *slog.Logger) *Conn { return &Conn{url: url, log: log} }

// Channel opens a channel, dialling first when there is no open connection.
func (c *Conn) Channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.DialConfig(c.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("dial rabbitmq: %w", err)
		}
		c.conn = conn
		c.log.Info("connected to rabbitmq")
	}
	return c.conn.Channel()
}

// IsOpen reports whether the current connection is up. The /health check uses it.
func (c *Conn) IsOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.conn.IsClosed()
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	return c.conn.Close()
}
