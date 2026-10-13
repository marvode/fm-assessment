package ledgertest

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store"
)

// Start is the fixed time every test clock starts at.
var Start = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// SeedWallet opens a wallet and, when balance is positive, funds it with a settled top-up. Returns the wallet id.
func SeedWallet(t *testing.T, s *store.Store, userID uuid.UUID, balance int64, at time.Time) uuid.UUID {
	t.Helper()
	wallet := domain.OpenUserWallet(userID, at)
	_, err := store.Run(context.Background(), s, func(_ context.Context, tx *store.Tx) (struct{}, error) {
		tx.AddAccount(wallet)
		return struct{}{}, nil
	})
	require.NoError(t, err)
	if balance > 0 {
		SeedTopUp(t, s, wallet.ID, balance, at)
	}
	return wallet.ID
}

// SeedTopUp posts a settled top-up to the wallet and returns its transaction id.
func SeedTopUp(t *testing.T, s *store.Store, accountID uuid.UUID, amount int64, at time.Time) uuid.UUID {
	t.Helper()
	id, err := store.Run(context.Background(), s, func(ctx context.Context, tx *store.Tx) (uuid.UUID, error) {
		wallet, err := tx.Account(ctx, accountID)
		if err != nil {
			return uuid.Nil, err
		}
		settlement, err := tx.Account(ctx, domain.ProviderSettlementID)
		if err != nil {
			return uuid.Nil, err
		}
		topUp := domain.StartTopUp(accountID, amount, "seed-"+uuid.NewString(), "seed", "chg_seed_"+uuid.NewString(), at)
		entries, err := domain.Post(topUp, settlement, wallet, false, at)
		if err != nil {
			return uuid.Nil, err
		}
		topUp.MarkSucceeded(at)
		tx.AddTransaction(topUp)
		tx.AddEntries(entries...)
		return topUp.ID, nil
	})
	require.NoError(t, err)
	return id
}

func Wallet(t *testing.T, s *store.Store, userID uuid.UUID) *domain.Account {
	t.Helper()
	wallet, err := s.WalletByUser(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, wallet, "user %s has no wallet", userID)
	return wallet
}

func Settlement(t *testing.T, s *store.Store) *domain.Account {
	t.Helper()
	settlement, err := s.Account(context.Background(), domain.ProviderSettlementID)
	require.NoError(t, err)
	return settlement
}

func Transaction(t *testing.T, s *store.Store, id uuid.UUID) *domain.Transaction {
	t.Helper()
	transaction, err := s.Transaction(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, transaction, "transaction %s not found", id)
	return transaction
}

// Count runs a single-value count query.
func Count(t *testing.T, s *store.Store, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.Pool().QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// Times returns a before-write hook that runs write before the first n saves and does nothing afterwards: the
// equivalent of .NET's ConcurrentWriteInterceptor. write must use a store without this hook.
func Times(n int, write func(ctx context.Context) error) store.Hook {
	var mu sync.Mutex
	remaining := n
	return func(ctx context.Context) error {
		mu.Lock()
		if remaining == 0 {
			mu.Unlock()
			return nil
		}
		remaining--
		mu.Unlock()
		return write(ctx)
	}
}

// Hooked returns a store on the same pool whose first n saves each run write first (see Times). The handler under
// test uses the hooked store; write uses s, so the competing write is not itself hooked.
func Hooked(s *store.Store, n int, write func(ctx context.Context) error) *store.Store {
	return store.New(s.Pool(), store.WithBeforeWrite(Times(n, write)))
}
