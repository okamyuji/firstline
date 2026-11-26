package web

import (
	"maps"
	"slices"
	"testing"
	"time"
)

func TestHoldLimiterSweepsIdleIPsOncePerMinute(t *testing.T) {
	l := &holdLimiter{limit: 10, hits: map[string][]time.Time{}}
	t0 := time.UnixMilli(1_800_000_000_000)
	l.allow("a", t0)
	l.allow("b", t0.Add(holdWindow-time.Millisecond))
	if got := len(l.hits); got != 2 {
		t.Fatalf("before the sweep: %d IPs, want 2", got)
	}
	l.allow("c", t0.Add(holdWindow))
	if got := slices.Sorted(maps.Keys(l.hits)); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("after the sweep: %v, want [b c]", got)
	}
}
