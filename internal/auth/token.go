// Package auth issues and verifies the signed tokens that let a buyer look up a purchase.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

// Tokens signs "purchaseID.expiryUnixMilli". The key length is checked at startup
// by the config loader; Verify still fails closed on an empty key.
type Tokens struct{ Key []byte }

// Strict rejects non-zero trailing bits; the decoder also skips \r and \n, which Verify rejects
// itself, so each valid token has exactly one accepted spelling.
var enc = base64.RawURLEncoding.Strict()

func (t Tokens) sign(payload string) []byte {
	m := hmac.New(sha256.New, t.Key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func (t Tokens) Issue(purchaseID string, exp time.Time) string {
	p := purchaseID + "." + strconv.FormatInt(exp.UnixMilli(), 10)
	return enc.EncodeToString([]byte(p)) + "." + enc.EncodeToString(t.sign(p))
}

// Verify returns the purchase ID of a token that is well signed and not expired at now.
func (t Tokens) Verify(tok string, now time.Time) (string, error) {
	if len(t.Key) == 0 {
		return "", ErrInvalidToken
	}
	left, right, ok := strings.Cut(tok, ".")
	if !ok || strings.ContainsAny(tok, "\r\n") || strings.Contains(right, ".") {
		return "", ErrInvalidToken
	}
	payload, err := enc.DecodeString(left)
	if err != nil {
		return "", ErrInvalidToken
	}
	sig, err := enc.DecodeString(right)
	if err != nil || !hmac.Equal(sig, t.sign(string(payload))) {
		return "", ErrInvalidToken
	}
	// Purchase IDs are base64url and contain no '.', so the first one splits id from expiry.
	id, expStr, ok := strings.Cut(string(payload), ".")
	if !ok || id == "" {
		return "", ErrInvalidToken
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", ErrInvalidToken
	}
	if now.UnixMilli() > exp {
		return "", ErrExpiredToken
	}
	return id, nil
}
