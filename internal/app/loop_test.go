package app_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
)

func TestEveryRunsOnEachTickUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	var gotD atomic.Int64
	after := func(d time.Duration) <-chan time.Time {
		gotD.Store(int64(d))
		return ticks
	}
	runs := make(chan context.Context)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Every(ctx, after, 5*time.Second, func(c context.Context) { runs <- c })
	}()

	for i := range 2 {
		ticks <- time.Time{}
		if c := <-runs; c != ctx {
			t.Errorf("run %d got a different context", i)
		}
	}
	if d := time.Duration(gotD.Load()); d != 5*time.Second {
		t.Errorf("after got %v, want 5s", d)
	}
	cancel()
	<-done
	select {
	case <-runs:
		t.Error("f ran after cancel")
	default:
	}
}

func TestEveryDoesNotRunBeforeTheFirstTick(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	app.Every(ctx, func(time.Duration) <-chan time.Time { return nil }, time.Second, func(context.Context) { ran = true })
	if ran {
		t.Error("f ran without a tick")
	}
}
