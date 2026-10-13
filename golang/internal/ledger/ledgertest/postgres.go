// Package ledgertest holds test helpers shared by the wallet's test packages. Only _test.go files import it.
package ledgertest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"wallet/internal/ledger/store"
)

var (
	pgOnce     sync.Once
	pgAdminURL string
	pgErr      error
	// One template per test binary: go test runs packages in parallel processes that may share one server.
	templateDB = fmt.Sprintf("wallet_template_%d", os.Getpid())
)

// NewPool returns a pool on a fresh, migrated database that is dropped when the test ends. The server is
// WALLET_TEST_DATABASE_URL when set, otherwise a postgres:17 container started once per test binary.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pgOnce.Do(func() { pgAdminURL, pgErr = setupPostgres(ctx) })
	require.NoError(t, pgErr)

	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	exec(t, pgAdminURL, "CREATE DATABASE "+name+" TEMPLATE "+templateDB)
	pool, err := pgxpool.New(ctx, withDatabase(pgAdminURL, name))
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		exec(t, pgAdminURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool
}

func NewStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(NewPool(t))
}

// RequirePgCode asserts err is a Postgres error with the given SQLSTATE (23505 unique, 23514 check).
func RequirePgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a Postgres error, got %v", err)
	require.Equal(t, code, pgErr.Code)
}

func setupPostgres(ctx context.Context) (string, error) {
	serverURL := os.Getenv("WALLET_TEST_DATABASE_URL")
	if serverURL == "" {
		container, err := postgres.Run(ctx, "postgres:17",
			postgres.WithDatabase("postgres"),
			postgres.WithUsername("postgres"),
			postgres.WithPassword("postgres"),
			postgres.BasicWaitStrategies())
		if err != nil {
			return "", err
		}
		if serverURL, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return "", err
		}
	}
	admin := withDatabase(serverURL, "postgres")
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDB+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return "", err
	}
	// Close the migration pool before cloning: CREATE DATABASE ... TEMPLATE fails while the template has sessions.
	pool, err := pgxpool.New(ctx, withDatabase(serverURL, templateDB))
	if err != nil {
		return "", err
	}
	defer pool.Close()
	return admin, store.Migrate(ctx, pool)
}

func exec(t *testing.T, connString, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connString)
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	require.NoError(t, err)
}

func withDatabase(connString, database string) string {
	u, err := url.Parse(connString)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + database
	return u.String()
}
