package common

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock. Times are UTC and truncated to microseconds, the precision Postgres stores,
// so a value read back from the database equals the one written.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// FakeClock is a clock tests move by hand, so expiry is tested without sleeping.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start.UTC()} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
