package store

import (
	"context"
	"errors"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// MaxAttempts bounds WithRetry.
const MaxAttempts = 3

// WithBeforeWrite installs a hook that runs after a successful operation and before its changes are written.
func WithBeforeWrite(h Hook) Option { return func(s *Store) { s.beforeWrite = h } }

// Run executes fn as one unit of work: load → change → save. If fn fails nothing is written. If another writer
// changed a tracked row first, or committed the same unique key, nothing is written and Run returns ErrConflict.
func Run[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error) {
	var zero T
	pgTx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = pgTx.Rollback(context.WithoutCancel(ctx)) }() // no-op after Commit

	tx := newTx(db.New(pgTx))
	result, err := fn(ctx, tx)
	if err != nil {
		return zero, err
	}
	if !tx.hasWrites() {
		return result, pgTx.Commit(ctx)
	}
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx); err != nil {
			return zero, err
		}
	}
	if err := tx.flush(ctx); err != nil {
		return zero, err
	}
	if err := pgTx.Commit(ctx); err != nil {
		return zero, writeErr(err)
	}
	return result, nil
}

// WithRetry is Run, rerun from fresh state after a conflict (the port of .NET's ConcurrencyRetry). After MaxAttempts
// conflicts it returns domain.ErrConcurrencyExhausted.
func WithRetry[T any](ctx context.Context, s *Store, fn func(context.Context, *Tx) (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		result, err := Run(ctx, s, fn)
		if !errors.Is(err, ErrConflict) {
			return result, err
		}
		if attempt == MaxAttempts {
			var zero T
			return zero, domain.ErrConcurrencyExhausted
		}
	}
}
