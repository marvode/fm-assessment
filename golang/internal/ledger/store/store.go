// Package store persists the ledger in Postgres. Store wraps the pool; Run and WithRetry (run.go) execute a unit of
// work with EF-style change tracking and optimistic concurrency.
package store

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet/internal/ledger/store/db"
)

type Store struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	beforeWrite Hook
}

type Option func(*Store)

func New(pool *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{pool: pool, queries: db.New(pool)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Queries runs generated queries directly on the pool, outside any unit of work. Use it for reads.
func (s *Store) Queries() *db.Queries { return s.queries }
