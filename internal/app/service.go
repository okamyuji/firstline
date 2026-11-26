package app

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"sync/atomic"
	"time"

	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/writer"
)

// Deps are the collaborators of Service.
type Deps struct {
	Store     *store.Store
	Writer    *writer.Writer
	Inventory port.Inventory
	Payment   port.Payment
	Errors    port.ErrorReporter
	Clock     Clock
	NewID     func() string
}

// Service holds the application use cases.
type Service struct {
	Store     *store.Store
	Writer    *writer.Writer
	Inventory port.Inventory
	Payment   port.Payment
	Errors    port.ErrorReporter
	Clock     Clock
	NewID     func() string
	Throttle  *auth.Throttle
	Limiter   *auth.Limiter

	hashPassword   func(pw string) (string, error)
	verifyPassword func(hash, pw string) (bool, error)
	dummyHash      func() (string, error)

	lastReconcile atomic.Pointer[time.Time]
	mailMu        sync.Mutex
	mailFailures  map[int64]int // consecutive send failures per mail_outbox id
}

// New builds a Service; a nil NewID means random IDs.
func New(d Deps) *Service {
	if d.NewID == nil {
		d.NewID = newID
	}
	s := &Service{
		Store: d.Store, Writer: d.Writer, Inventory: d.Inventory, Payment: d.Payment, Errors: d.Errors,
		Clock: d.Clock, NewID: d.NewID, Throttle: auth.NewThrottle(), Limiter: auth.NewLimiter(),
		hashPassword: auth.HashPassword, verifyPassword: auth.VerifyPassword, mailFailures: map[int64]int{},
	}
	// Built on first use with the hash function in effect then, so a login for
	// an unknown email pays the same Argon2id cost as one for a real organizer.
	s.dummyHash = sync.OnceValues(func() (string, error) { return s.hashPassword(randomString(16)) })
	return s
}

// newID returns 16 random bytes as unpadded base64url.
func newID() string { return randomString(16) }

// randomString returns n random bytes as unpadded base64url. rand.Read never
// returns an error since Go 1.24; it crashes the program instead.
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
