package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/outbox"
	"wallet/internal/ledger/store"
)

var ctx = context.Background()

type published struct {
	messageType string
	payload     []byte
	messageID   uuid.UUID
}

type recordingPublisher struct {
	mu          sync.Mutex
	published   []published
	failWhen    func(uuid.UUID) bool
	timeOutWhen func(uuid.UUID) bool
}

func (p *recordingPublisher) Publish(_ context.Context, messageType string, payload []byte, messageID uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWhen != nil && p.failWhen(messageID) {
		return errors.New("broker unavailable")
	}
	if p.timeOutWhen != nil && p.timeOutWhen(messageID) {
		// What broker clients return on their own timeout: a deadline the caller's context did not set.
		return fmt.Errorf("publish timed out: %w", context.DeadlineExceeded)
	}
	p.published = append(p.published, published{messageType, payload, messageID})
	return nil
}

type fixture struct {
	s         *store.Store
	clock     *common.FakeClock
	publisher *recordingPublisher
}

func newFixture(t *testing.T) *fixture {
	return &fixture{s: ledgertest.NewStore(t), clock: common.NewFakeClock(ledgertest.Start), publisher: &recordingPublisher{}}
}

func (f *fixture) relay(batchSize int) *outbox.Relay {
	return outbox.NewRelay(f.s, f.publisher, f.clock, batchSize, ledgertest.Logger())
}

func (f *fixture) addMessage(t *testing.T) uuid.UUID {
	t.Helper()
	message := store.NewOutboxMessage("test.message", []byte("{}"), f.clock.Now())
	_, err := store.Run(ctx, f.s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddOutbox(message)
		return struct{}{}, nil
	})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	return message.ID
}

type row struct {
	attempts    int32
	lastError   *string
	processedAt *time.Time
}

func (f *fixture) row(t *testing.T, id uuid.UUID) row {
	t.Helper()
	var r row
	err := f.s.Pool().QueryRow(ctx, "SELECT attempts, last_error, processed_at FROM outbox_messages WHERE id = $1", id).
		Scan(&r.attempts, &r.lastError, &r.processedAt)
	require.NoError(t, err)
	return r
}

func TestPublishesPendingMessagesOldestFirstAndMarksThemProcessed(t *testing.T) {
	f := newFixture(t)
	first, second := f.addMessage(t), f.addMessage(t)

	published, err := f.relay(50).RelayPending(ctx)
	require.NoError(t, err)
	publishedAgain, err := f.relay(50).RelayPending(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, published)
	require.Zero(t, publishedAgain)
	require.Equal(t, []uuid.UUID{first, second}, []uuid.UUID{f.publisher.published[0].messageID, f.publisher.published[1].messageID})
	require.Zero(t, ledgertest.Count(t, f.s, "SELECT count(*) FROM outbox_messages WHERE processed_at IS NULL"))
}

func TestAFailedPublishIsRecordedAndRetriedOnTheNextRun(t *testing.T) {
	f := newFixture(t)
	failing := f.addMessage(t)
	f.addMessage(t)
	f.publisher.failWhen = func(id uuid.UUID) bool { return id == failing }

	published, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	failed := f.row(t, failing)
	require.Nil(t, failed.processedAt)
	require.EqualValues(t, 1, failed.attempts)
	require.Equal(t, "broker unavailable", *failed.lastError)

	f.publisher.failWhen = nil
	published, err = f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	require.NotNil(t, f.row(t, failing).processedAt)
}

func TestAPublishTimeoutIsRecordedRatherThanTreatedAsShutdown(t *testing.T) {
	f := newFixture(t)
	timingOut := f.addMessage(t)
	f.addMessage(t)
	f.publisher.timeOutWhen = func(id uuid.UUID) bool { return id == timingOut }

	published, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, published)
	failed := f.row(t, timingOut)
	require.Nil(t, failed.processedAt)
	require.EqualValues(t, 1, failed.attempts)
}

func TestShutdownStopsTheRelayWithoutRecordingAFailure(t *testing.T) {
	f := newFixture(t)
	message := f.addMessage(t)
	runCtx, cancel := context.WithCancel(ctx)
	f.publisher.failWhen = func(uuid.UUID) bool { cancel(); return true }

	_, err := f.relay(50).RelayPending(runCtx)

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, f.row(t, message).attempts)
}

func TestPublishesAtMostOneBatchPerRun(t *testing.T) {
	f := newFixture(t)
	f.addMessage(t)
	f.addMessage(t)
	f.addMessage(t)

	first, err := f.relay(2).RelayPending(ctx)
	require.NoError(t, err)
	second, err := f.relay(2).RelayPending(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, first)
	require.Equal(t, 1, second)
}

func TestAWithdrawalPublishesAPayoutRequestForItsTransaction(t *testing.T) {
	f := newFixture(t)
	transactionID := ledgertest.PendingWithdrawal(t, f.s, f.clock, uuid.New(), 10_000, 4_000)

	_, err := f.relay(50).RelayPending(ctx)

	require.NoError(t, err)
	require.Len(t, f.publisher.published, 1)
	require.Equal(t, messages.PayoutRequestedType, f.publisher.published[0].messageType)
	var payload messages.PayoutRequested
	require.NoError(t, json.Unmarshal(f.publisher.published[0].payload, &payload))
	require.Equal(t, transactionID, payload.TransactionID)
}
