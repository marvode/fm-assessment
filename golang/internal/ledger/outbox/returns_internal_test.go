package outbox

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

// Internal test: the return check is unexported and only observable through a live broker otherwise.
func TestReturnedMessageCheck(t *testing.T) {
	id := uuid.New()

	empty := make(chan amqp.Return, 1)
	require.NoError(t, checkReturned(empty, id))

	returned := make(chan amqp.Return, 1)
	returned <- amqp.Return{ReplyCode: 312, ReplyText: "NO_ROUTE"}
	require.True(t, errors.Is(checkReturned(returned, id), ErrUnroutable))

	closed := make(chan amqp.Return, 1)
	close(closed)
	require.NoError(t, checkReturned(closed, id), "a returns channel closed with the AMQP channel is not an unroutable message")
}
