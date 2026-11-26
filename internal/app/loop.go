package app

import (
	"context"
	"time"
)

// Every runs f once per d, starting d after the call, until ctx is done.
// after is time.After in production; tests pass a channel they control.
func Every(ctx context.Context, after func(time.Duration) <-chan time.Time, d time.Duration, f func(context.Context)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(d):
			f(ctx)
		}
	}
}
