// Package observe keeps the 5-minute windows behind /healthz and the EMF
// metrics, and serves /healthz.
package observe

import (
	"math"
	"slices"
	"sync"
	"time"
)

const (
	window = 5 * time.Minute
	// shortcut: past this many syncs in 5 minutes (~218/s) the oldest waits
	// fall out early, so p99 covers a shorter span; raise it or switch to a
	// histogram if the sync rate ever reaches that.
	maxWaitSamples = 1 << 16
	windowSeconds  = int64(window / time.Second)
)

type waitSample struct {
	at   int64 // Unix nanoseconds
	wait time.Duration
}

type second struct {
	sec      int64
	syncs    int
	usageSum float64
	usageN   int
}

// Recorder holds the 5-minute windows. All methods are safe for concurrent use.
type Recorder struct {
	mu      sync.Mutex
	waits   []waitSample // ring of the newest maxWaitSamples
	next    int
	seconds [windowSeconds]second
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// bucket returns the bucket for sec, resetting it when it holds an older second.
func (r *Recorder) bucket(sec int64) *second {
	b := &r.seconds[sec%windowSeconds]
	if b.sec != sec {
		*b = second{sec: sec}
	}
	return b
}

// OnSyncResult is the writer.Options.OnSyncResult callback.
func (r *Recorder) OnSyncResult(at time.Time, wait time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := waitSample{at.UnixNano(), wait}
	if len(r.waits) < maxWaitSamples {
		r.waits = append(r.waits, s)
	} else {
		r.waits[r.next] = s
		r.next = (r.next + 1) % maxWaitSamples
	}
	r.bucket(at.Unix()).syncs++
}

// SampleQueue records the writer queue usage at one instant.
func (r *Recorder) SampleQueue(at time.Time, length, capacity int) {
	if capacity <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bucket(at.Unix())
	b.usageSum += float64(length) / float64(capacity)
	b.usageN++
}

// SampleCount is the number of stored wait samples.
func (r *Recorder) SampleCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.waits)
}

// CommitWaitP99Ms is the nearest-rank p99 of waits recorded in the 5 minutes before now.
func (r *Recorder) CommitWaitP99Ms(now time.Time) float64 {
	r.mu.Lock()
	cutoff := now.Add(-window).UnixNano()
	in := make([]time.Duration, 0, len(r.waits))
	for _, s := range r.waits {
		if s.at > cutoff && s.at <= now.UnixNano() {
			in = append(in, s.wait)
		}
	}
	r.mu.Unlock()
	if len(in) == 0 {
		return 0
	}
	slices.Sort(in)
	rank := int(math.Ceil(0.99*float64(len(in)))) - 1
	return float64(in[rank]) / float64(time.Millisecond)
}

// live calls f for each bucket whose second lies in the 5 minutes before now.
func (r *Recorder) live(now time.Time, f func(*second)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := now.Unix()
	for i := range r.seconds {
		if b := &r.seconds[i]; b.sec > n-windowSeconds && b.sec <= n {
			f(b)
		}
	}
}

// SyncItemsPerSecMax is the largest per-second sync count in the 5 minutes before now.
func (r *Recorder) SyncItemsPerSecMax(now time.Time) int {
	most := 0
	r.live(now, func(b *second) { most = max(most, b.syncs) })
	return most
}

// QueueUsageAvg is the mean of the queue usage samples in the 5 minutes before now.
func (r *Recorder) QueueUsageAvg(now time.Time) float64 {
	var sum float64
	var n int
	r.live(now, func(b *second) { sum += b.usageSum; n += b.usageN })
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
