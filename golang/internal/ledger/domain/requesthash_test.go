package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/ledger/domain"
)

func TestEqualRequestsHashEqually(t *testing.T) {
	userID := uuid.New()

	require.Equal(t, domain.RequestHash("Withdrawal", userID, int64(400)), domain.RequestHash("Withdrawal", userID, int64(400)))
}

func TestAnyDifferenceChangesTheHash(t *testing.T) {
	userID := uuid.New()

	require.NotEqual(t, domain.RequestHash("Withdrawal", userID, int64(400)), domain.RequestHash("Withdrawal", userID, int64(401)))
	require.NotEqual(t, domain.RequestHash("a", "b"), domain.RequestHash("b", "a"))
	require.Len(t, domain.RequestHash("a"), 64)
}

// Postgres text cannot hold NUL, so a field containing one must be a validation failure, not a database error.
func TestBlankOrLongerRejectsNULBytes(t *testing.T) {
	require.True(t, domain.BlankOrLonger("a\x00b", 128))
	require.False(t, domain.BlankOrLonger("ab", 128))
}
