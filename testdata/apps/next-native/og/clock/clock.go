// Package clock is og:clock.
package clock

import (
	"context"
	"time"
)

// After waits ms milliseconds, then returns the time ms after t.
func After(ctx context.Context, t time.Time, ms int) (time.Time, error) {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return t.Add(time.Duration(ms) * time.Millisecond), nil
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
}
