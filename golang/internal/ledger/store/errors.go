package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConflict means another writer changed a tracked row first (a stale version or status) or committed the same
// unique key. Nothing was written; running the operation again sees the latest state.
var ErrConflict = errors.New("store: concurrent write conflict")

const uniqueViolation = "23505"

// writeErr turns a unique violation into ErrConflict (keeping the cause) and passes other errors through.
func writeErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return err
}
