package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/i18n"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/uptrace/bun"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const (
	mailFailureReport = 10
	// holdNoticeWait is how long a hold notice waits for payment_id, counted from the hold's creation.
	holdNoticeWait = time.Minute
	tokenLifetime  = 30 * 24 * time.Hour
	jstOffset      = 9 * 60 * 60
)

// A fixed zone, so the deadline does not depend on the tz database being installed.
var jst = time.FixedZone("JST", jstOffset)

// bun rejects a column the struct lacks, so this mirrors every column of list_unsent_mail.
type outboxMail struct {
	ID               int64          `bun:"id"`
	PurchaseID       string         `bun:"purchase_id"`
	Kind             string         `bun:"kind"`
	Lang             string         `bun:"lang"`
	State            string         `bun:"state"`
	PaymentID        sql.NullString `bun:"payment_id"`
	CheckoutURL      sql.NullString `bun:"checkout_url"`
	Email            string         `bun:"email"`
	Quantity         int            `bun:"quantity"`
	Amount           int            `bun:"amount"`
	ExpiresAt        int64          `bun:"expires_at"`
	TicketNumberFrom sql.NullInt64  `bun:"ticket_number_from"`
	TitleJA          string         `bun:"title_ja"`
	TitleEN          string         `bun:"title_en"`
	StartsAt         int64          `bun:"starts_at"`
	EndsAt           int64          `bun:"ends_at"`
}

type mailText struct {
	tr      i18n.Translator
	tok     auth.Tokens
	baseURL string
}

// SendMail sends the unsent mail_outbox rows in id order and returns how many
// it sent. A failed send is retried on the next run and reported through
// ErrorReporter at its tenth consecutive failure, so it is not in the returned
// error; that error carries listing, composing and sent_at failures.
func (s *Service) SendMail(ctx context.Context, m port.Mailer, tr i18n.Translator, tok auth.Tokens, baseURL string) (int, error) {
	var rows []outboxMail
	if err := s.Store.Select(ctx, s.Store.Read, &rows, "list_unsent_mail"); err != nil {
		return 0, fmt.Errorf("list unsent mail: %w", err)
	}
	w := mailText{tr, tok, baseURL}
	now := s.Clock()
	sent := 0
	_, err := each(rows, func(r outboxMail) error {
		ok, err := s.sendOne(ctx, m, w, r, now)
		if ok {
			sent++
		}
		return err
	})
	return sent, err
}

func (s *Service) sendOne(ctx context.Context, m port.Mailer, w mailText, r outboxMail, now time.Time) (bool, error) {
	if r.Kind == string(domain.MailHold) {
		// A hold notice after the payment failed or completed would ask for a payment that no longer counts.
		if r.State != string(domain.StateHeld) {
			return false, s.markSent(ctx, r.ID)
		}
		created := time.UnixMilli(r.ExpiresAt).Add(-domain.HoldTTL)
		if r.PaymentID.String == "" && !now.After(created.Add(holdNoticeWait)) {
			return false, nil // wait for payment_id so the notice can carry the checkout link
		}
	}
	mail, err := w.compose(r)
	if err != nil {
		return false, err
	}
	if err := m.Send(ctx, mail); err != nil {
		s.mailFailed(ctx, r.ID, err)
		return false, nil
	}
	return true, s.markSent(ctx, r.ID)
}

func (s *Service) markSent(ctx context.Context, id int64) error {
	s.mailMu.Lock()
	delete(s.mailFailures, id)
	s.mailMu.Unlock()
	err := s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := s.Store.Exec(ctx, tx, "mark_mail_sent", s.Clock().UnixMilli(), id)
		return err
	})
	if err != nil {
		return fmt.Errorf("mark mail %d sent: %w", id, err)
	}
	return nil
}

func (s *Service) mailFailed(ctx context.Context, id int64, err error) {
	s.mailMu.Lock()
	s.mailFailures[id]++
	n := s.mailFailures[id]
	s.mailMu.Unlock()
	if n == mailFailureReport {
		s.Errors.Report(ctx, fmt.Errorf("mail %d failed %d times in a row: %w", id, n, err), slog.Int64("mail_id", id))
	}
}

func (w mailText) compose(r outboxMail) (port.Mail, error) {
	lang := r.Lang
	title := r.TitleJA
	if lang == "en" {
		title = r.TitleEN
	}
	url := w.baseURL + "/purchases/" + r.PurchaseID + "?token=" + w.tok.Issue(r.PurchaseID, time.UnixMilli(r.EndsAt).Add(tokenLifetime))
	mail := port.Mail{To: r.Email, Lang: lang}
	switch domain.MailKind(r.Kind) {
	case domain.MailHold:
		mail.Subject = w.tr.T(lang, i18n.MailHoldSubject)
		mail.Body = w.tr.T(lang, i18n.MailHoldBody, title, r.Quantity, jstTime(lang, r.ExpiresAt), url)
		if r.CheckoutURL.String != "" {
			mail.Body += w.tr.T(lang, i18n.MailHoldCheckout, r.CheckoutURL.String)
		}
	case domain.MailPurchased:
		mail.Subject = w.tr.T(lang, i18n.MailPurchasedSubject, title)
		mail.Body = w.tr.T(lang, i18n.MailPurchasedBody, title, jstTime(lang, r.StartsAt), ticketRange(r), url)
	case domain.MailRefund:
		mail.Subject = w.tr.T(lang, i18n.MailRefundSubject, title)
		mail.Body = w.tr.T(lang, i18n.MailRefundBody, title, message.NewPrinter(language.English).Sprintf("%d", r.Amount), url)
	default:
		return port.Mail{}, fmt.Errorf("mail %d: unknown kind %q", r.ID, r.Kind)
	}
	return mail, nil
}

// jstTime writes unix ms in Japan time; English readers get the offset spelled out.
func jstTime(lang string, ms int64) string {
	layout := "2006-01-02 15:04"
	if lang == "en" {
		layout += " -07:00"
	}
	return time.UnixMilli(ms).In(jst).Format(layout)
}

func ticketRange(r outboxMail) string {
	from := r.TicketNumberFrom.Int64
	if r.Quantity == 1 {
		return strconv.FormatInt(from, 10)
	}
	return fmt.Sprintf("%d-%d", from, from+int64(r.Quantity)-1)
}
