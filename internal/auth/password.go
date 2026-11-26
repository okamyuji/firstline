package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrBadHash means a stored hash is not in the PHC form HashPassword writes today.
var ErrBadHash = errors.New("unsupported password hash")

type argonParams struct {
	memory, time uint32 // memory in KiB
	threads      uint8
}

// params is the second recommended option of RFC 9106 section 4. It is a
// variable only so the package's tests can lower the cost.
var params = argonParams{memory: 65536, time: 3, threads: 4}

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding.Strict()

func phcPrefix() string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, params.memory, params.time, params.threads)
}

// HashPassword returns pw hashed with Argon2id as a PHC string. The error is
// always nil: crypto/rand.Read crashes the program instead of failing.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, params.time, params.memory, params.threads, keyLen)
	return phcPrefix() + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key), nil
}

// VerifyPassword reports whether pw matches hash. A hash with other parameters
// fails with ErrBadHash rather than being verified at a weaker cost.
func VerifyPassword(hash, pw string) (bool, error) {
	rest, ok := strings.CutPrefix(hash, phcPrefix())
	if !ok {
		return false, ErrBadHash
	}
	s, k, ok := strings.Cut(rest, "$")
	if !ok {
		return false, ErrBadHash
	}
	salt, err := b64.DecodeString(s)
	if err != nil {
		return false, ErrBadHash
	}
	// An empty key would compare equal to an empty derived key for every password.
	want, err := b64.DecodeString(k)
	if err != nil || len(want) != keyLen {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, params.time, params.memory, params.threads, keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Limiter caps concurrent Argon2id computations at GOMAXPROCS, so a burst of
// logins cannot take 64 MiB each without bound. GOMAXPROCS honours a
// container's CPU quota; runtime.NumCPU counts every host CPU.
type Limiter struct{ slots chan struct{} }

func NewLimiter() *Limiter { return &Limiter{slots: make(chan struct{}, runtime.GOMAXPROCS(0))} }

// Acquire waits for a free slot until ctx is done; the HTTP layer turns the error into 503.
func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot taken by a successful Acquire.
func (l *Limiter) Release() { <-l.slots }
