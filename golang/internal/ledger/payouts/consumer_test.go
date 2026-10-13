package payouts_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/payouts"
)

type handlerFunc func(context.Context, messages.PayoutRequested) (payouts.Outcome, error)

func (f handlerFunc) Process(ctx context.Context, m messages.PayoutRequested) (payouts.Outcome, error) {
	return f(ctx, m)
}

type acknowledger struct {
	mu    sync.Mutex
	acks  int
	nacks []bool // requeue flag of each nack
}

func (a *acknowledger) Ack(uint64, bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return nil
}

func (a *acknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nacks = append(a.nacks, requeue)
	return nil
}

func (a *acknowledger) Reject(tag uint64, requeue bool) error { return a.Nack(tag, false, requeue) }

type retrier struct {
	attempts []int32
	err      error
}

func (r *retrier) Retry(_ context.Context, _ amqp.Delivery, attempts int32) error {
	r.attempts = append(r.attempts, attempts)
	return r.err
}

func consumer(handler handlerFunc) *payouts.Consumer {
	c := payouts.NewConsumer(nil, handler, ledgertest.Logger())
	c.RetryDelay = 0
	return c
}

func delivery(a *acknowledger, body string) amqp.Delivery {
	return amqp.Delivery{Acknowledger: a, DeliveryTag: 1, Body: []byte(body)}
}

func validBody() string { return `{"transactionId":"` + uuid.NewString() + `"}` }

func failing(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
	return "", errors.New("provider down")
}

func TestHandleAcksAProcessedDelivery(t *testing.T) {
	a, r := &acknowledger{}, &retrier{}
	c := consumer(func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
		return payouts.Initiated, nil
	})

	c.Handle(ctx, delivery(a, validBody()), r)

	require.Equal(t, 1, a.acks)
	require.Empty(t, a.nacks)
	require.Empty(t, r.attempts)
}

func TestHandleRetriesAFailedDeliveryWithItsAttemptCount(t *testing.T) {
	a, r := &acknowledger{}, &retrier{}
	d := delivery(a, validBody())
	d.Headers = amqp.Table{payouts.AttemptsHeader: int32(2)}

	consumer(failing).Handle(ctx, d, r)

	require.Equal(t, []int32{3}, r.attempts)
	require.Equal(t, 1, a.acks, "the original is acked once its retry copy is on the queue")
	require.Empty(t, a.nacks)
}

func TestHandleDeadLettersADeliveryThatReachedTheLimit(t *testing.T) {
	a, r := &acknowledger{}, &retrier{}
	d := delivery(a, validBody())
	d.Headers = amqp.Table{payouts.AttemptsHeader: int32(payouts.DeliveryLimit - 1)}

	consumer(failing).Handle(ctx, d, r)

	require.Empty(t, r.attempts)
	require.Zero(t, a.acks)
	require.Equal(t, []bool{false}, a.nacks)
}

func TestHandleRequeuesWhenTheRetryCannotBePublished(t *testing.T) {
	a, r := &acknowledger{}, &retrier{err: errors.New("channel closed")}

	consumer(failing).Handle(ctx, delivery(a, validBody()), r)

	require.Equal(t, []int32{1}, r.attempts)
	require.Zero(t, a.acks)
	require.Equal(t, []bool{true}, a.nacks)
}

func TestHandleDeadLettersUnreadableDeliveries(t *testing.T) {
	for _, body := range []string{"not json", `{"transactionId":"00000000-0000-0000-0000-000000000000"}`, `{}`} {
		a, r := &acknowledger{}, &retrier{}
		called := false
		c := consumer(func(context.Context, messages.PayoutRequested) (payouts.Outcome, error) {
			called = true
			return "", nil
		})

		c.Handle(ctx, delivery(a, body), r)

		require.False(t, called, body)
		require.Equal(t, []bool{false}, a.nacks, body)
	}
}

func TestHandleLeavesADeliveryUnsettledWhenShuttingDown(t *testing.T) {
	a, r := &acknowledger{}, &retrier{}
	shutdown, cancel := context.WithCancel(ctx)
	c := consumer(func(ctx context.Context, _ messages.PayoutRequested) (payouts.Outcome, error) {
		cancel()
		return "", ctx.Err()
	})

	c.Handle(shutdown, delivery(a, validBody()), r)

	require.Zero(t, a.acks)
	require.Empty(t, a.nacks, "the broker redelivers unacked messages after the channel closes")
	require.Empty(t, r.attempts)
}

func TestHandleLeavesADeliveryUnsettledWhenShutdownInterruptsTheRetry(t *testing.T) {
	a := &acknowledger{}
	shutdown, cancel := context.WithCancel(ctx)
	r := &cancellingRetrier{cancel: cancel}

	consumer(failing).Handle(shutdown, delivery(a, validBody()), r)

	require.Zero(t, a.acks)
	require.Empty(t, a.nacks, "a retry cut short by shutdown must not also requeue the original")
}

type cancellingRetrier struct{ cancel context.CancelFunc }

func (r *cancellingRetrier) Retry(ctx context.Context, _ amqp.Delivery, _ int32) error {
	r.cancel()
	return ctx.Err()
}
