package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/uptrace/bun"
)

const (
	minPasswordRunes = 12
	sessionMaxAge    = 12 * time.Hour
	sessionIdle      = 2 * time.Hour
	touchAfter       = 5 * time.Minute
)

type EventInput struct {
	TitleJa, TitleEn               string
	StartsAt, EndsAt, SaleStartsAt time.Time
	Capacity, Price                int
}

type organizerRow struct {
	ID           string `bun:"id"`
	Email        string `bun:"email"`
	PasswordHash string `bun:"password_hash"`
}

type sessionRow struct {
	IDHash      string `bun:"id_hash"`
	OrganizerID string `bun:"organizer_id"`
	CreatedAt   int64  `bun:"created_at"`
	LastSeenAt  int64  `bun:"last_seen_at"`
}

// CreateOrganizer validates the input as create-organizer specifies, hashes
// the password and stores the organizer under the lower-cased email.
func (s *Service) CreateOrganizer(ctx context.Context, email, password string) error {
	// Requiring Address == input rejects the display-name and angle-bracket forms.
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return domain.ErrBadInput
	}
	if utf8.RuneCountInString(password) < minPasswordRunes {
		return domain.ErrBadInput
	}
	hash, err := s.limited(ctx, func() (string, error) { return s.hashPassword(password) })
	if err != nil {
		return err
	}
	err = s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := s.Store.Exec(ctx, tx, "insert_organizer", s.NewID(), strings.ToLower(email), hash)
		return err
	})
	if err != nil && s.Store.IsUnique(err) {
		return domain.ErrDuplicateOrganizer
	}
	return err
}

// limited runs one Argon2id computation inside a Limiter slot.
func (s *Service) limited[T any](ctx context.Context, f func() (T, error)) (T, error) {
	if err := s.Limiter.Acquire(ctx); err != nil {
		var zero T
		return zero, err
	}
	defer s.Limiter.Release()
	return f()
}

// Login checks the password and starts a session. An unknown email is
// verified against a dummy hash so the response time does not reveal it.
func (s *Service) Login(ctx context.Context, email, password, ip string) (string, error) {
	email = strings.ToLower(email)
	if !s.Throttle.Allowed(email, ip, s.Clock()) {
		return "", domain.ErrTooManyAttempts
	}
	var org organizerRow
	err := s.Store.Select(ctx, s.Store.Read, &org, "get_organizer_by_email", email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	ok, err := s.limited(ctx, func() (bool, error) { return s.verify(org, password) })
	if err != nil {
		return "", err
	}
	now := s.Clock()
	if !ok {
		s.Throttle.Fail(email, ip, now)
		return "", domain.ErrBadCredentials
	}
	id := randomString(32)
	err = s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := s.Store.Exec(ctx, tx, "insert_session", sessionHash(id), org.ID, now.UnixMilli(), now.UnixMilli())
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) verify(org organizerRow, password string) (bool, error) {
	if org.ID != "" {
		return s.verifyPassword(org.PasswordHash, password)
	}
	dummy, err := s.dummyHash()
	if err != nil {
		return false, err
	}
	_, err = s.verifyPassword(dummy, password)
	return false, err
}

func sessionHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// Session returns the organizer of a live session. It is expired 12 hours
// after login or 2 hours after the last request, both at the exact boundary.
// last_seen_at is written at most every 5 minutes, as an async item.
func (s *Service) Session(ctx context.Context, sessionID string) (string, error) {
	var row sessionRow
	err := s.Store.Select(ctx, s.Store.Read, &row, "get_session", sessionHash(sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNoSession
	}
	if err != nil {
		return "", err
	}
	now := s.Clock()
	if !now.Before(time.UnixMilli(row.CreatedAt).Add(sessionMaxAge)) ||
		!now.Before(time.UnixMilli(row.LastSeenAt).Add(sessionIdle)) {
		return "", domain.ErrNoSession
	}
	if now.Sub(time.UnixMilli(row.LastSeenAt)) >= touchAfter {
		s.Writer.Send(func(ctx context.Context, tx bun.Tx) error {
			_, err := s.Store.Exec(ctx, tx, "touch_session", now.UnixMilli(), row.IDHash)
			return err
		})
	}
	return row.OrganizerID, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := s.Store.Exec(ctx, tx, "delete_session", sessionHash(sessionID))
		return err
	})
}

// PurgeSessions deletes expired sessions as an async item; the loop calls it hourly.
func (s *Service) PurgeSessions(_ context.Context) {
	now := s.Clock()
	s.Writer.Send(func(ctx context.Context, tx bun.Tx) error {
		_, err := s.Store.Exec(ctx, tx, "delete_expired_sessions",
			now.Add(-sessionMaxAge).UnixMilli(), now.Add(-sessionIdle).UnixMilli())
		return err
	})
}

// CreateEvent stores the event and its event_sales row in one sync item, then
// adds the capacity to the inventory.
func (s *Service) CreateEvent(ctx context.Context, organizerID string, in EventInput) (string, error) {
	if in.TitleJa == "" || in.TitleEn == "" || !in.StartsAt.Before(in.EndsAt) ||
		in.Capacity < 1 || in.Price < 0 || !in.SaleStartsAt.Before(in.StartsAt) {
		return "", domain.ErrBadInput
	}
	id := s.NewID()
	err := s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := s.Store.Exec(ctx, tx, "insert_event", id, organizerID, in.TitleJa, in.TitleEn,
			in.StartsAt.UnixMilli(), in.EndsAt.UnixMilli(), in.Capacity, in.Price, in.SaleStartsAt.UnixMilli(), 1); err != nil {
			return err
		}
		_, err := s.Store.Exec(ctx, tx, "insert_event_sales", id, in.Capacity)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := s.Inventory.Add(ctx, id, in.Capacity); err != nil {
		return "", fmt.Errorf("add inventory for event %s: %w", id, err)
	}
	return id, nil
}
