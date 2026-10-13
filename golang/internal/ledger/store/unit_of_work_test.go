package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/store"
)

func placeHold(walletID uuid.UUID, amount int64, attempts *int) func(context.Context, *store.Tx) (int64, error) {
	return func(ctx context.Context, tx *store.Tx) (int64, error) {
		if attempts != nil {
			*attempts++
		}
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return 0, err
		}
		return wallet.PlaceHold(amount)
	}
}

// holdElsewhere commits a hold through a separate unit of work, as a concurrent request would.
func holdElsewhere(s *store.Store, walletID uuid.UUID, amount int64) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := store.Run(ctx, s, placeHold(walletID, amount, nil))
		return err
	}
}

func TestWithRetrySavesTheOperationOnTheFirstAttempt(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	attempts := 0

	available, err := store.WithRetry(context.Background(), s, placeHold(walletID, 100, &attempts))

	require.NoError(t, err)
	require.EqualValues(t, 900, available)
	require.Equal(t, 1, attempts)
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestWithRetryRetriesFromFreshStateAfterAConcurrentWrite(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(1, holdElsewhere(s, walletID, 300))))
	attempts := 0

	available, err := store.WithRetry(context.Background(), hooked, placeHold(walletID, 100, &attempts))

	require.NoError(t, err)
	require.EqualValues(t, 600, available)
	require.Equal(t, 2, attempts)
	require.EqualValues(t, 600, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestWithRetryGivesUpAfterTheMaximumAttempts(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(store.MaxAttempts, holdElsewhere(s, walletID, 10))))
	attempts := 0

	_, err := store.WithRetry(context.Background(), hooked, placeHold(walletID, 100, &attempts))

	require.Equal(t, "Concurrency.Exhausted", common.CodeOf(err))
	require.Equal(t, store.MaxAttempts, attempts)
	require.EqualValues(t, 1_000-10*store.MaxAttempts, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestAFailedOperationIsNotSaved(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)

	_, err := store.WithRetry(context.Background(), s, func(ctx context.Context, tx *store.Tx) (int64, error) {
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return 0, err
		}
		if _, err := wallet.PlaceHold(100); err != nil {
			return 0, err
		}
		return 0, domain.ErrInsufficientFunds
	})

	require.ErrorIs(t, err, domain.ErrInsufficientFunds)
	require.EqualValues(t, 1_000, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestSavingAStaleAccountWritesNothing(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	hooked := store.New(s.Pool(), store.WithBeforeWrite(ledgertest.Times(1, holdElsewhere(s, walletID, 100))))

	_, err := store.Run(context.Background(), hooked, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		wallet, err := tx.Account(ctx, walletID)
		if err != nil {
			return struct{}{}, err
		}
		if _, err := wallet.PlaceHold(200); err != nil {
			return struct{}{}, err
		}
		tx.AddOutbox(store.NewOutboxMessage("test.message", []byte("{}"), ledgertest.Start))
		return struct{}{}, nil
	})

	require.True(t, errors.Is(err, store.ErrConflict))
	require.Zero(t, ledgertest.Count(t, s, "SELECT count(*) FROM outbox_messages"))
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}

func TestAPostedTopUpRoundTrips(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()

	walletID := ledgertest.SeedWallet(t, s, userID, 5_000, ledgertest.Start)

	wallet := ledgertest.Wallet(t, s, userID)
	require.Equal(t, walletID, wallet.ID)
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	require.EqualValues(t, -5_000, ledgertest.Settlement(t, s).Balance)
	transactions, err := s.Transactions(context.Background(), walletID, 10)
	require.NoError(t, err)
	require.Len(t, transactions, 1)
	require.Equal(t, domain.Succeeded, transactions[0].Status)
	require.Equal(t, ledgertest.Start, transactions[0].CreatedAt)
	require.EqualValues(t, 2, ledgertest.Count(t, s, "SELECT count(*) FROM ledger_entries WHERE transaction_id = $1", transactions[0].ID))
}

func TestADuplicateInsertIsAConflict(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 0, ledgertest.Start)

	_, err := store.Run(context.Background(), s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddAccount(domain.OpenUserWallet(userID, ledgertest.Start))
		return struct{}{}, nil
	})

	require.ErrorIs(t, err, store.ErrConflict)
}

func TestLoadingTheSameAccountTwiceReturnsOneTrackedCopy(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)

	_, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (struct{}, error) {
		first, err := tx.Account(ctx, walletID)
		require.NoError(t, err)
		second, err := tx.WalletByUser(ctx, userID)
		require.NoError(t, err)
		require.Same(t, first, second)
		_, err = first.PlaceHold(100)
		return struct{}{}, err
	})

	require.NoError(t, err)
	require.EqualValues(t, 900, ledgertest.Wallet(t, s, userID).AvailableBalance)
}
