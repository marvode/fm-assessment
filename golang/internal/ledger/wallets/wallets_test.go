package wallets_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/wallets"
)

var ctx = context.Background()

func TestCreateOpensExactlyOneEmptyWalletPerUser(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	handler := wallets.NewCreateHandler(s, common.NewFakeClock(ledgertest.Start))

	first, err := handler.Handle(ctx, userID)
	require.NoError(t, err)
	second, err := handler.Handle(ctx, userID)
	require.NoError(t, err)

	require.True(t, first.Created)
	require.Equal(t, userID, first.Wallet.UserID)
	require.Zero(t, first.Wallet.Balance)
	require.Equal(t, "NGN", first.Wallet.Currency)
	require.False(t, second.Created)
	require.Equal(t, first.Wallet.AccountID, second.Wallet.AccountID)
	require.EqualValues(t, 1, ledgertest.Count(t, s, "SELECT count(*) FROM accounts WHERE user_id = $1", userID))
}

func TestCreateReturnsTheWinnerWhenAConcurrentCreateCommitsFirst(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	clock := common.NewFakeClock(ledgertest.Start)
	var winner wallets.Creation
	hooked := ledgertest.Hooked(s, 1, func(ctx context.Context) error {
		var err error
		winner, err = wallets.NewCreateHandler(s, clock).Handle(ctx, userID)
		return err
	})

	result, err := wallets.NewCreateHandler(hooked, clock).Handle(ctx, userID)

	require.NoError(t, err)
	require.False(t, result.Created)
	require.Equal(t, winner.Wallet.AccountID, result.Wallet.AccountID)
	require.EqualValues(t, 1, ledgertest.Count(t, s, "SELECT count(*) FROM accounts WHERE user_id = $1", userID))
}

func TestGetForAnUnknownUserReturnsNotFound(t *testing.T) {
	_, err := wallets.NewGetHandler(ledgertest.NewStore(t)).Handle(ctx, uuid.New())

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestGetReturnsTheBalances(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 2_500, ledgertest.Start)

	wallet, err := wallets.NewGetHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.EqualValues(t, 2_500, wallet.Balance)
	require.EqualValues(t, 2_500, wallet.AvailableBalance)
}

func TestListReturnsTransactionsNewestFirst(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	walletID := ledgertest.SeedWallet(t, s, userID, 1_000, ledgertest.Start)
	ledgertest.SeedTopUp(t, s, walletID, 200, ledgertest.Start.Add(5*time.Minute))

	transactions, err := wallets.NewListHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.Len(t, transactions, 2)
	require.EqualValues(t, 200, transactions[0].Amount)
	require.EqualValues(t, 1_000, transactions[1].Amount)
	for _, tr := range transactions {
		require.Equal(t, "TopUp", tr.Type)
		require.Equal(t, "Succeeded", tr.Status)
		require.Equal(t, "NGN", tr.Currency)
	}
}

func TestListForAnUnknownUserReturnsNotFound(t *testing.T) {
	_, err := wallets.NewListHandler(ledgertest.NewStore(t)).Handle(ctx, uuid.New())

	require.Equal(t, "Wallet.NotFound", common.CodeOf(err))
}

func TestListOfAnEmptyWalletIsAnEmptySlice(t *testing.T) {
	s, userID := ledgertest.NewStore(t), uuid.New()
	ledgertest.SeedWallet(t, s, userID, 0, ledgertest.Start)

	transactions, err := wallets.NewListHandler(s).Handle(ctx, userID)

	require.NoError(t, err)
	require.NotNil(t, transactions)
	require.Empty(t, transactions)
}
