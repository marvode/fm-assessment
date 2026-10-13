package common_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"wallet/internal/common"
)

func TestFakeClockAdvanceMovesTimeForwardByExactlyTheGivenAmount(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := common.NewFakeClock(start)

	clock.Advance(5 * time.Minute)

	require.Equal(t, start.Add(5*time.Minute), clock.Now())
}

func TestSystemClockIsUTCAtMicrosecondPrecision(t *testing.T) {
	now := common.SystemClock{}.Now()

	require.Equal(t, time.UTC, now.Location())
	require.Zero(t, now.Nanosecond()%1000)
}

func TestErrorsMatchByCode(t *testing.T) {
	sentinel := common.NewError("Wallet.NotFound", "The user has no wallet.")
	wrapped := fmt.Errorf("loading: %w", common.NewError("Wallet.NotFound", "other text"))

	require.ErrorIs(t, wrapped, sentinel)
	require.NotErrorIs(t, wrapped, common.NewError("Wallet.InsufficientFunds", "x"))
	require.Equal(t, "Wallet.NotFound", common.CodeOf(wrapped))
	require.Equal(t, "", common.CodeOf(errors.New("plain")))
	require.Equal(t, "Wallet.NotFound: The user has no wallet.", sentinel.Error())
}

func TestRunEveryKeepsRunningAfterAFailureAndStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs, failures atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- common.RunEvery(ctx, time.Millisecond, func(context.Context) error {
			if runs.Add(1) == 1 {
				return errors.New("boom")
			}
			return nil
		}, func(error) { failures.Add(1) })
	}()

	require.Eventually(t, func() bool { return runs.Load() >= 3 }, time.Second, time.Millisecond)
	cancel()

	require.NoError(t, <-done)
	require.Equal(t, int32(1), failures.Load())
}
