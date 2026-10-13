package topups_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
)

var ctx = context.Background()

type fixture struct {
	s        *store.Store
	clock    *common.FakeClock
	provider *providers.FakePaymentProvider
	userID   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return &fixture{
		s:        ledgertest.NewStore(t),
		clock:    common.NewFakeClock(ledgertest.Start),
		provider: providers.NewFakePaymentProvider(),
		userID:   uuid.New(),
	}
}

func (f *fixture) initiate(s *store.Store) *topups.InitiateHandler {
	return topups.NewInitiateHandler(s, f.provider, f.clock)
}

func (f *fixture) complete(s *store.Store) *topups.CompleteHandler {
	return topups.NewCompleteHandler(s, f.clock, ledgertest.Logger())
}

func (f *fixture) command(amount int64) topups.InitiateCommand {
	return topups.InitiateCommand{UserID: f.userID, Amount: amount, ReferenceID: "topup-1"}
}

func (f *fixture) startTopUp(t *testing.T, amount int64) string {
	t.Helper()
	started, err := f.initiate(f.s).Handle(ctx, f.command(amount))
	require.NoError(t, err)
	return *started.Transaction.ProviderReferenceID
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	return ledgertest.Count(t, f.s, sql, args...)
}

func TestInitiateOpensTheWalletLazilyAndRecordsAPendingTopUp(t *testing.T) {
	f := newFixture(t)

	result, err := f.initiate(f.s).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, "TopUp", result.Transaction.Type)
	require.Equal(t, "Pending", result.Transaction.Status)
	require.EqualValues(t, 5_000, result.Transaction.Amount)
	require.True(t, strings.HasPrefix(*result.Transaction.ProviderReferenceID, "chg_"))
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestInitiateRejectsNonPositiveAmounts(t *testing.T) {
	for _, amount := range []int64{0, -5} {
		f := newFixture(t)

		_, err := f.initiate(f.s).Handle(ctx, f.command(amount))

		require.Equal(t, "Money.InvalidAmount", common.CodeOf(err), "amount %d", amount)
		require.Zero(t, f.count(t, "SELECT count(*) FROM transactions"))
	}
}

func TestInitiateRejectsABlankReference(t *testing.T) {
	f := newFixture(t)
	command := f.command(5_000)
	command.ReferenceID = " "

	_, err := f.initiate(f.s).Handle(ctx, command)

	require.Equal(t, "Validation.ReferenceId", common.CodeOf(err))
}

func TestInitiateRejectsAnOversizedReferenceBeforeCharging(t *testing.T) {
	f := newFixture(t)
	command := f.command(5_000)
	command.ReferenceID = strings.Repeat("r", 129)

	_, err := f.initiate(f.s).Handle(ctx, command)

	require.Equal(t, "Validation.ReferenceId", common.CodeOf(err))
	require.Zero(t, f.count(t, "SELECT count(*) FROM transactions"))
}

func TestInitiateReplaysAnIdenticalRequest(t *testing.T) {
	f := newFixture(t)

	first, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)
	second, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)

	require.True(t, second.Replayed)
	require.Equal(t, first.Transaction.ID, second.Transaction.ID)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
}

func TestInitiateRejectsAReusedReferenceWithADifferentAmount(t *testing.T) {
	f := newFixture(t)
	_, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)

	_, err = f.initiate(f.s).Handle(ctx, f.command(6_000))

	require.Equal(t, "Transaction.DuplicateReference", common.CodeOf(err))
}

func TestSucceedCreditsTheWalletWithABalancedPosting(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	wallet := ledgertest.Wallet(t, f.s, f.userID)
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, -5_000, ledgertest.Settlement(t, f.s).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestSucceedIsIdempotentForDuplicateWebhooks(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	_, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)
	require.NoError(t, err)

	again, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", again.Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", again.ID))
}

func TestSucceedWithAMismatchedAmountFailsTheTopUpWithoutCrediting(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 9_000)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestSucceedForAnUnknownReferenceReturnsNotFound(t *testing.T) {
	f := newFixture(t)

	_, err := f.complete(f.s).Succeed(ctx, "chg_missing", 5_000)

	require.Equal(t, "Transaction.NotFound", common.CodeOf(err))
}

func TestFailMarksTheTopUpFailedWithoutEntries(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)

	result, err := f.complete(f.s).Fail(ctx, providerReferenceID)

	require.NoError(t, err)
	require.Equal(t, "Failed", result.Status)
	require.Zero(t, ledgertest.Wallet(t, f.s, f.userID).Balance)
	require.Zero(t, f.count(t, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", result.ID))
}

func TestAFailureWebhookRacingBehindASuccessDoesNotOverwriteIt(t *testing.T) {
	f := newFixture(t)
	providerReferenceID := f.startTopUp(t, 5_000)
	successFirst := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.complete(f.s).Succeed(ctx, providerReferenceID, 5_000)
		return err
	})

	result, err := f.complete(successFirst).Fail(ctx, providerReferenceID)

	require.NoError(t, err)
	require.Equal(t, "Succeeded", result.Status)
	require.Equal(t, domain.Succeeded, ledgertest.Transaction(t, f.s, result.ID).Status)
	require.EqualValues(t, 5_000, ledgertest.Wallet(t, f.s, f.userID).Balance)
}

func TestConcurrentIdenticalInitiationsCreateOneTopUp(t *testing.T) {
	f := newFixture(t)
	twin := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		_, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
		return err
	})

	result, err := f.initiate(twin).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM accounts WHERE user_id = $1", f.userID))
}

func TestConcurrentFirstTopUpsShareOneWallet(t *testing.T) {
	f := newFixture(t)
	other := ledgertest.Hooked(f.s, 1, func(ctx context.Context) error {
		command := f.command(1_000)
		command.ReferenceID = "topup-other"
		_, err := f.initiate(f.s).Handle(ctx, command)
		return err
	})

	result, err := f.initiate(other).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.EqualValues(t, 1, f.count(t, "SELECT count(*) FROM accounts WHERE user_id = $1", f.userID))
	require.EqualValues(t, 2, f.count(t, "SELECT count(*) FROM transactions WHERE type = 'TopUp'"))
}

func TestReferenceIDsAreScopedToEachUsersWallet(t *testing.T) {
	f := newFixture(t)
	mine, err := f.initiate(f.s).Handle(ctx, f.command(5_000))
	require.NoError(t, err)
	command := f.command(5_000)
	command.UserID = uuid.New()

	theirs, err := f.initiate(f.s).Handle(ctx, command)

	require.NoError(t, err)
	require.False(t, theirs.Replayed)
	require.NotEqual(t, mine.Transaction.ID, theirs.Transaction.ID)
	require.NotEqual(t, *mine.Transaction.ProviderReferenceID, *theirs.Transaction.ProviderReferenceID)
}

// observingProvider records how many pool connections were checked out when the provider was called.
type observingProvider struct {
	*providers.FakePaymentProvider
	s        *store.Store
	acquired []int32
}

func (p *observingProvider) InitiateCharge(ctx context.Context, referenceID string, amount int64) (providers.ChargeInitiation, error) {
	p.acquired = append(p.acquired, p.s.Pool().Stat().AcquiredConns())
	return p.FakePaymentProvider.InitiateCharge(ctx, referenceID, amount)
}

// A slow provider must not hold a database connection: pool exhaustion would stall every other request.
func TestInitiateChargesTheProviderWithNoDatabaseConnectionHeld(t *testing.T) {
	f := newFixture(t)
	provider := &observingProvider{FakePaymentProvider: f.provider, s: f.s}

	_, err := topups.NewInitiateHandler(f.s, provider, f.clock).Handle(ctx, f.command(5_000))

	require.NoError(t, err)
	require.Equal(t, []int32{0}, provider.acquired)
}
