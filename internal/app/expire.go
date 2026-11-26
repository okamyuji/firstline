package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/okamyuji/firstline/internal/domain"
)

// ExpireDue expires the held purchases whose expires_at is ExpiryGrace or more
// in the past. It returns how many items applied and the joined item errors.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	var rows []dueHold
	cutoff := s.Clock().Add(-domain.ExpiryGrace).UnixMilli()
	if err := s.Store.Select(ctx, s.Store.Read, &rows, "list_due_holds", cutoff); err != nil {
		return 0, fmt.Errorf("list due holds: %w", err)
	}
	return each(rows, func(r dueHold) error {
		return s.apply(ctx, r.ID, domain.EvExpire, domain.Facts{})
	})
}

type dueHold struct {
	ID        string `bun:"id"`
	EventID   string `bun:"event_id"`
	Quantity  int    `bun:"quantity"`
	ExpiresAt int64  `bun:"expires_at"`
}

// each runs f for every item, keeps going past errors and counts the successes.
func each[T any](items []T, f func(T) error) (int, error) {
	n := 0
	var errs []error
	for _, it := range items {
		if err := f(it); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
