package auth

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	throttleWindow = 15 * time.Minute
	emailFailLimit = 5
	ipFailLimit    = 20
)

type failures struct {
	fails       []time.Time
	lockedUntil time.Time
	inFlight    int
}

// Throttle counts failed logins in memory per lower-cased email and per IP.
// Reaching a limit within the window locks that key for the window, counted
// from the failure that reached it.
type Throttle struct {
	mu        sync.Mutex
	email, ip map[string]*failures
}

func NewThrottle() *Throttle {
	return &Throttle{email: map[string]*failures{}, ip: map[string]*failures{}}
}

// Attempt is one login admitted by Begin; exactly one of Succeed or Fail ends it.
type Attempt struct {
	t         *Throttle
	email, ip string
}

// Begin admits a login unless the email or the IP is locked, or its recent
// failures plus the attempts still in flight already reach its limit. Counting
// in-flight attempts under the same lock as the check is what keeps concurrent
// wrong passwords from all being verified before the first failure is recorded.
func (t *Throttle) Begin(email, ip string, now time.Time) (Attempt, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	email = strings.ToLower(email)
	if full(t.email[email], emailFailLimit, now) || full(t.ip[ip], ipFailLimit, now) {
		return Attempt{}, false
	}
	entry(t.email, email).inFlight++
	entry(t.ip, ip).inFlight++
	return Attempt{t: t, email: email, ip: ip}, true
}

func entry(m map[string]*failures, key string) *failures {
	f := m[key]
	if f == nil {
		f = &failures{}
		m[key] = f
	}
	return f
}

// full also drops the failures that left the window, since recent reuses the slice.
func full(f *failures, limit int, now time.Time) bool {
	if f == nil {
		return false
	}
	f.fails = recent(f.fails, now)
	return now.Before(f.lockedUntil) || len(f.fails)+f.inFlight >= limit
}

// Succeed ends the attempt without counting a failure.
func (a Attempt) Succeed() {
	a.t.mu.Lock()
	defer a.t.mu.Unlock()
	a.t.email[a.email].inFlight--
	a.t.ip[a.ip].inFlight--
}

// Fail ends the attempt as a failed login for the email and the IP.
func (a Attempt) Fail(now time.Time) {
	a.t.mu.Lock()
	defer a.t.mu.Unlock()
	fail(a.t.email[a.email], emailFailLimit, now)
	fail(a.t.ip[a.ip], ipFailLimit, now)
}

func fail(f *failures, limit int, now time.Time) {
	f.inFlight--
	f.fails = append(recent(f.fails, now), now)
	if len(f.fails) >= limit {
		f.lockedUntil = now.Add(throttleWindow)
	}
}

func recent(ts []time.Time, now time.Time) []time.Time {
	return slices.DeleteFunc(ts, func(at time.Time) bool { return now.Sub(at) >= throttleWindow })
}

// Sweep drops failures older than the window and the keys left with none and
// no attempt in flight. A key with no recent failure is never locked, since a
// lock ends one window after the failure that set it.
func (t *Throttle) Sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range []map[string]*failures{t.email, t.ip} {
		maps.DeleteFunc(m, func(_ string, f *failures) bool {
			f.fails = recent(f.fails, now)
			return len(f.fails) == 0 && f.inFlight == 0
		})
	}
}
