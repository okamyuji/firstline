package app

import (
	"context"
	"fmt"
)

type inventoryCount struct {
	EventID  string `bun:"event_id"`
	Capacity int    `bun:"capacity"`
	Active   int    `bun:"active"`
}

// RebuildInventory reloads the in-memory counts from WriteDB at startup.
func (s *Service) RebuildInventory(ctx context.Context) error {
	var rows []inventoryCount
	if err := s.Store.Select(ctx, s.Store.Write, &rows, "inventory_counts"); err != nil {
		return fmt.Errorf("load inventory counts: %w", err)
	}
	for _, r := range rows {
		if err := s.Inventory.Add(ctx, r.EventID, r.Capacity-r.Active); err != nil {
			return fmt.Errorf("add inventory for event %s: %w", r.EventID, err)
		}
	}
	return nil
}
