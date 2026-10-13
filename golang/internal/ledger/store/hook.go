package store

import "context"

// Hook runs inside Run after the operation succeeded and just before its changes are written. Tests use it to commit
// a competing write from another connection, which forces a concurrency conflict deterministically.
type Hook func(ctx context.Context) error
