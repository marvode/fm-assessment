package common

import (
	"context"
	"time"
)

// RunEvery calls run once per interval until ctx ends, like .NET's PeriodicTimer loop: the first run is one interval
// after the start. A failed run is reported to onError and never stops the loop. It returns nil when ctx ends.
func RunEvery(ctx context.Context, interval time.Duration, run func(context.Context) error, onError func(error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := run(ctx); err != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
}
