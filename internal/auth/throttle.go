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

// Allowed reports whether neither the email nor the IP is locked at now.
func (t *Throttle) Allowed(email, ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !locked(t.email[strings.ToLower(email)], now) && !locked(t.ip[ip], now)
}

func locked(f *failures, now time.Time) bool { return f != nil && now.Before(f.lockedUntil) }

// Fail records a failed login for the email and the IP.
func (t *Throttle) Fail(email, ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fail(t.email, strings.ToLower(email), emailFailLimit, now)
	fail(t.ip, ip, ipFailLimit, now)
}

func fail(m map[string]*failures, key string, limit int, now time.Time) {
	f := m[key]
	if f == nil {
		f = &failures{}
		m[key] = f
	}
	f.fails = append(recent(f.fails, now), now)
	if len(f.fails) >= limit {
		f.lockedUntil = now.Add(throttleWindow)
	}
}

func recent(ts []time.Time, now time.Time) []time.Time {
	return slices.DeleteFunc(ts, func(at time.Time) bool { return now.Sub(at) >= throttleWindow })
}

// Sweep drops failures older than the window and the keys left with none. A
// key with no recent failure is never locked, since a lock ends one window
// after the failure that set it.
func (t *Throttle) Sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range []map[string]*failures{t.email, t.ip} {
		maps.DeleteFunc(m, func(_ string, f *failures) bool {
			f.fails = recent(f.fails, now)
			return len(f.fails) == 0
		})
	}
}
