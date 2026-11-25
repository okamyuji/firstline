package inventory

import (
	"context"

	"github.com/okamyuji/firstline/internal/port"
)

type db struct{}

// NewDB returns the inventory that leaves every decision to the
// `remaining >= ?` condition on event_sales.
func NewDB() port.Inventory { return db{} }

func (db) Add(context.Context, string, int) error     { return nil }
func (db) Reserve(context.Context, string, int) error { return nil }
func (db) Release(context.Context, string, int) error { return nil }

// Remaining is 0 because the count lives only in event_sales.remaining.
func (db) Remaining(context.Context, string) (int, error) { return 0, nil }
