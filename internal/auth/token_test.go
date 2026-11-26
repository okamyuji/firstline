package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	testKey = []byte("0123456789-not-a-real-key-0123456789")
	exp     = time.UnixMilli(1_800_000_000_000)
)

// forge signs payload with key, so tests can build well-signed but malformed tokens.
func forge(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func TestTokenRoundTrip(t *testing.T) {
	tk := Tokens{Key: testKey}
	id, err := tk.Verify(tk.Issue("abc_DEF-123", exp), exp.Add(-time.Hour))
	if err != nil || id != "abc_DEF-123" {
		t.Fatalf("got %q, %v", id, err)
	}
}

func TestVerifyExpiryBoundary(t *testing.T) {
	tk := Tokens{Key: testKey}
	tok := tk.Issue("p1", exp)
	if _, err := tk.Verify(tok, exp); err != nil {
		t.Errorf("at expiry: %v", err)
	}
	if _, err := tk.Verify(tok, exp.Add(time.Millisecond)); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("1ms after expiry: got %v, want ErrExpiredToken", err)
	}
}

func TestVerifyRejectsAnyChangedSignatureByte(t *testing.T) {
	tk := Tokens{Key: testKey}
	tok := tk.Issue("p1", exp)
	dot := strings.IndexByte(tok, '.')
	for i := dot + 1; i < len(tok); i++ {
		b := []byte(tok)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		if _, err := tk.Verify(string(b), exp.Add(-time.Hour)); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("byte %d changed: got %v", i, err)
		}
	}
}

func TestVerifyRejectsChangedPayload(t *testing.T) {
	tk := Tokens{Key: testKey}
	other := tk.Issue("p2", exp)
	tok := tk.Issue("p1", exp)
	swapped := other[:strings.IndexByte(other, '.')] + tok[strings.IndexByte(tok, '.'):]
	if _, err := tk.Verify(swapped, exp.Add(-time.Hour)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("got %v", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	tok := Tokens{Key: testKey}.Issue("p1", exp)
	if _, err := (Tokens{Key: []byte("another-not-a-real-key-another-key")}).Verify(tok, exp); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("got %v", err)
	}
}

func TestEmptyKeyFailsClosed(t *testing.T) {
	// A token forged with the empty key must not verify under the empty key.
	tok := forge(nil, "p1."+strconv.FormatInt(exp.UnixMilli(), 10))
	for _, k := range [][]byte{nil, {}} {
		if _, err := (Tokens{Key: k}).Verify(tok, exp); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("key %v: got %v", k, err)
		}
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	tk := Tokens{Key: testKey}
	ms := strconv.FormatInt(exp.UnixMilli(), 10)
	tests := map[string]string{
		"empty":              "",
		"no dot":             "abcdef",
		"two dots":           "a.b.c",
		"bad base64 left":    "!!!.AAAA",
		"bad base64 right":   "AAAA.!!!",
		"empty parts":        ".",
		"payload no dot":     forge(testKey, "p1"+ms),
		"empty id":           forge(testKey, "."+ms),
		"non numeric exp":    forge(testKey, "p1.abc"),
		"newline in payload": tk.Issue("p1", exp)[:20] + "\n" + tk.Issue("p1", exp)[20:],
		"newline in sig":     withNewlineInSig(tk.Issue("p1", exp)),
		"sig trailing bits":  withSigTrailingBit(tk.Issue("p1", exp)),
		"empty exp":          forge(testKey, "p1."),
	}
	for name, tok := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := tk.Verify(tok, exp); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("got %v, want ErrInvalidToken", err)
			}
		})
	}
}

func FuzzVerify(f *testing.F) {
	tk := Tokens{Key: testKey}
	f.Add(tk.Issue("p1", exp))
	f.Add(tk.Issue("p1", exp)[:20] + "\r" + tk.Issue("p1", exp)[20:])
	f.Add("")
	f.Add(".")
	f.Add("a.b")
	f.Add(forge(testKey, "p1.abc"))
	f.Add(forge(testKey, "."))
	f.Add(withSigTrailingBit(tk.Issue("p1", exp)))
	f.Fuzz(func(t *testing.T, tok string) {
		id, err := tk.Verify(tok, exp)
		if err == nil && id == "" {
			t.Errorf("accepted %q with empty id", tok)
		}
		if err == nil && tok != tk.Issue(id, exp) {
			t.Errorf("accepted non-canonical %q", tok)
		}
	})
}

func withNewlineInSig(tok string) string {
	i := strings.IndexByte(tok, '.') + 5
	return tok[:i] + "\n" + tok[i:]
}

// withSigTrailingBit flips the lowest bit of the last character. A 32-byte
// signature leaves 2 unused bits there, so only a strict decoder rejects it.
func withSigTrailingBit(tok string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, tok[len(tok)-1])
	return tok[:len(tok)-1] + string(alphabet[last^1])
}
