package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// cheap swaps in tiny Argon2id costs so tests that are not about the cost run
// fast, including under -race and once per gremlins mutant.
func cheap(t *testing.T) {
	t.Helper()
	old := params
	params = argonParams{memory: 8, time: 1, threads: 1}
	t.Cleanup(func() { params = old })
}

func TestHashPasswordUsesSpecParams(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$")
	if !ok {
		t.Fatalf("hash %q does not carry the spec parameters", h)
	}
	salt, key, _ := strings.Cut(rest, "$")
	s, err := base64.RawStdEncoding.DecodeString(salt)
	if err != nil || len(s) != 16 {
		t.Errorf("salt %q decodes to %d bytes, %v; want 16", salt, len(s), err)
	}
	k, err := base64.RawStdEncoding.DecodeString(key)
	if err != nil || len(k) != 32 {
		t.Errorf("key %q decodes to %d bytes, %v; want 32", key, len(k), err)
	}
	// Recomputing with Argon2id itself catches a switch to Argon2i, which keeps the prefix.
	if want := argon2.IDKey([]byte("correct horse battery"), s, 3, 65536, 4, 32); !bytes.Equal(k, want) {
		t.Error("stored key is not Argon2id of the password with the stored salt")
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("VerifyPassword = %v, %v; want true", ok, err)
	}
}

func TestVerifyPassword(t *testing.T) {
	cheap(t)
	h, err := HashPassword("password-1234")
	if err != nil {
		t.Fatal(err)
	}
	for pw, want := range map[string]bool{"password-1234": true, "password-1235": false, "Password-1234": false, "": false} {
		if got, err := VerifyPassword(h, pw); got != want || err != nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want %v", pw, got, err, want)
		}
	}
	h2, _ := HashPassword("password-1234")
	if h2 == h {
		t.Error("two hashes of one password are equal; the salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	cheap(t)
	h, _ := HashPassword("password-1234")
	head, _, _ := strings.CutLast(h, "$")
	head += "$"
	key32 := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	for name, bad := range map[string]string{
		"other version": strings.Replace(h, "v=19", "v=16", 1),
		"other memory":  strings.Replace(h, "m=8,", "m=16,", 1),
		"other time":    strings.Replace(h, "t=1,", "t=2,", 1),
		"other threads": strings.Replace(h, "p=1$", "p=2$", 1),
		"argon2i":       strings.Replace(h, "argon2id", "argon2i", 1),
		"no key":        strings.TrimSuffix(head, "$"),
		"bad salt":      "$argon2id$v=19$m=8,t=1,p=1$!!$" + key32,
		// 16 zero bytes end in "A"; "B" sets a trailing bit a lenient decoder ignores.
		"salt trailing bits": "$argon2id$v=19$m=8,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAB$" + key32,
		"bad key":            head + "!!",
		"short key":          "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 31)),
		"long key":           "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 33)),
		"empty key":          "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$",
		"empty":              "",
	} {
		if ok, err := VerifyPassword(bad, "password-1234"); ok || !errors.Is(err, ErrBadHash) {
			t.Errorf("%s: VerifyPassword(%q) = %v, %v; want false, ErrBadHash", name, bad, ok, err)
		}
	}
}

// The slot count follows GOMAXPROCS, which honours a container's CPU quota;
// runtime.NumCPU reports every host CPU and would let more 64 MiB hashes run.
func TestLimiterAcquireFailsWhenFullUntilRelease(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	l := NewLimiter()
	if err := l.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := l.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire on a full limiter = %v; want DeadlineExceeded", err)
	}
	l.Release()
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if err := l.Acquire(ctx2); err != nil {
		t.Errorf("Acquire after Release = %v; want nil", err)
	}
}
