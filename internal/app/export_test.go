package app

import (
	"context"

	"github.com/okamyuji/firstline/internal/domain"
)

// Apply lets the external tests drive transitions whose callers come later.
func (s *Service) Apply(ctx context.Context, purchaseID string, ev domain.Event, f domain.Facts) error {
	return s.apply(ctx, purchaseID, ev, f)
}

// MailFailures reports the consecutive send failures counted for a mail_outbox row.
func (s *Service) MailFailures(id int64) int {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	return s.mailFailures[id]
}
