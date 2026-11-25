// Package inventory holds the Inventory implementations.
package inventory

import (
	"context"
	"sync"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/port"
)

type seats struct {
	mu                  sync.Mutex
	remaining, capacity int
}

type memory struct {
	mu     sync.RWMutex
	events map[string]*seats
}

// NewMemory returns the in-process pre-check. event_sales.remaining stays the
// authority; this only rejects sold-out purchases without a database round trip.
func NewMemory() port.Inventory {
	return &memory{events: map[string]*seats{}}
}

func (m *memory) get(id string) (*seats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.events[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return s, nil
}

func (m *memory) Add(_ context.Context, id string, capacity int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[id] = &seats{remaining: capacity, capacity: capacity}
	return nil
}

func (m *memory) Reserve(_ context.Context, id string, q int) error {
	if q <= 0 {
		return domain.ErrBadQuantity
	}
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remaining < q {
		return domain.ErrSoldOut
	}
	s.remaining -= q
	return nil
}

func (m *memory) Release(_ context.Context, id string, q int) error {
	if q <= 0 {
		return domain.ErrBadQuantity
	}
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remaining = min(s.remaining+q, s.capacity)
	return nil
}

func (m *memory) Remaining(_ context.Context, id string) (int, error) {
	s, err := m.get(id)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remaining, nil
}
