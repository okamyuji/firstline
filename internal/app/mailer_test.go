package app_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/i18n"
	"github.com/okamyuji/firstline/internal/inventory"
	mailfake "github.com/okamyuji/firstline/internal/mail/fake"
	"github.com/okamyuji/firstline/internal/port"
)

const baseURL = "https://tickets.example"

var (
	tokens   = auth.Tokens{Key: []byte("not-a-real-key-not-a-real-key-xx")}
	tr       = i18n.New()
	tokenExp = eventAt.Add(time.Hour).Add(30 * 24 * time.Hour)
)

type mailEnv struct {
	*env
	svc    *app.Service
	mailer *mailfake.Fake
}

func newMailEnv(t *testing.T) *mailEnv {
	t.Helper()
	e := newEnv(t, inventory.NewMemory())
	return &mailEnv{env: e, mailer: mailfake.New()}
}

// start builds the Service after the test changed deps.
func (m *mailEnv) start() *mailEnv {
	m.svc = m.env.svc()
	return m
}

func (m *mailEnv) holdAs(email, lang string, qty int) string {
	m.t.Helper()
	res, err := m.svc.Hold(m.t.Context(), app.HoldInput{EventID: "e1", Email: email, Lang: lang, Quantity: qty})
	if err != nil {
		m.t.Fatal(err)
	}
	return res.PurchaseID
}

func (m *mailEnv) send() (int, error) {
	return m.svc.SendMail(m.t.Context(), m.mailer, tr, tokens, baseURL)
}

func (m *mailEnv) mustSend(want int) {
	m.t.Helper()
	if n, err := m.send(); err != nil || n != want {
		m.t.Fatalf("SendMail = %d, %v; want %d, nil", n, err, want)
	}
}

func (m *mailEnv) mailID(purchaseID, kind string) int64 {
	m.t.Helper()
	var id int64
	if err := m.s.Write.QueryRowContext(m.t.Context(),
		"SELECT id FROM mail_outbox WHERE purchase_id = ? AND kind = ?", purchaseID, kind).Scan(&id); err != nil {
		m.t.Fatal(err)
	}
	return id
}

func (m *mailEnv) sentAt(purchaseID, kind string) sql.NullInt64 {
	m.t.Helper()
	var at sql.NullInt64
	if err := m.s.Write.QueryRowContext(m.t.Context(),
		"SELECT sent_at FROM mail_outbox WHERE purchase_id = ? AND kind = ?", purchaseID, kind).Scan(&at); err != nil {
		m.t.Fatal(err)
	}
	return at
}

func lookupURL(id string) string {
	return baseURL + "/purchases/" + id + "?token=" + tokens.Issue(id, tokenExp)
}

func checkoutPayment(e *env) port.Payment {
	return stubPayment{Payment: e.pay, start: func(_ context.Context, id string, _ int) (string, string, error) {
		return "pay-" + id, "https://pay.example/" + id, nil
	}}
}

func TestSendMailHoldNoticeInBuyerLanguage(t *testing.T) {
	for _, tc := range []struct{ lang, title, deadline string }{
		{"ja", "j", "2027-01-15 17:10"},
		{"en", "e", "2027-01-15 17:10 +09:00"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			m := newMailEnv(t)
			m.deps.NewID = func() string { return "p1" }
			m.deps.Payment = checkoutPayment(m.env)
			m.start()
			id := m.holdAs("a@x", tc.lang, 2)
			m.mustSend(1)

			want := port.Mail{To: "a@x", Lang: tc.lang, Subject: tr.T(tc.lang, i18n.MailHoldSubject),
				Body: tr.T(tc.lang, i18n.MailHoldBody, tc.title, 2, tc.deadline, lookupURL(id)) +
					tr.T(tc.lang, i18n.MailHoldCheckout, "https://pay.example/p1")}
			if got := m.mailer.Sent(); len(got) != 1 || got[0] != want {
				t.Fatalf("sent = %+v\nwant %+v", got, want)
			}
			if at := m.sentAt(id, "hold"); at != (sql.NullInt64{Int64: now.UnixMilli(), Valid: true}) {
				t.Errorf("sent_at = %+v, want %d", at, now.UnixMilli())
			}
			m.mustSend(0)
		})
	}
}

func TestSendMailLookupTokenVerifiesUntilEventEndPlus30Days(t *testing.T) {
	m := newMailEnv(t).start()
	id := m.holdAs("a@x", "en", 1)
	m.mustSend(1)
	body := m.mailer.Sent()[0].Body
	_, tok, ok := strings.Cut(body, "/purchases/"+id+"?token=")
	if !ok || !strings.Contains(body, baseURL+"/purchases/") {
		t.Fatalf("no lookup URL under %s in %q", baseURL, body)
	}
	tok, _, _ = strings.Cut(tok, "\n")
	if got, err := tokens.Verify(tok, tokenExp); err != nil || got != id {
		t.Errorf("Verify at expiry = %q, %v; want %q", got, err, id)
	}
	if _, err := tokens.Verify(tok, tokenExp.Add(time.Millisecond)); !errors.Is(err, auth.ErrExpiredToken) {
		t.Errorf("Verify after expiry err = %v, want expired", err)
	}
}

func TestSendMailSkipsHoldNoticeOfNonHeldPurchase(t *testing.T) {
	m := newMailEnv(t).start()
	id := m.holdAs("a@x", "en", 1)
	m.mailer.FailNext(3)
	for range 3 {
		m.mustSend(0)
	}
	mid := m.mailID(id, "hold")
	if got := m.svc.MailFailures(mid); got != 3 {
		t.Fatalf("failures = %d, want 3", got)
	}
	if err := m.svc.Failed(t.Context(), id, ""); err != nil {
		t.Fatal(err)
	}
	m.clock = now.Add(time.Second)
	m.mustSend(0)
	if len(m.mailer.Sent()) != 0 {
		t.Errorf("sent %d mails, want 0", len(m.mailer.Sent()))
	}
	if at := m.sentAt(id, "hold"); at.Int64 != m.clock.UnixMilli() {
		t.Errorf("sent_at = %+v, want %d", at, m.clock.UnixMilli())
	}
	if got := m.svc.MailFailures(mid); got != 0 {
		t.Errorf("failures after skip = %d, want 0", got)
	}
}

func TestSendMailDefersHoldNoticeWithoutPaymentID(t *testing.T) {
	m := newMailEnv(t).start()
	id := m.holdAs("a@x", "ja", 1)
	if _, err := m.s.Write.ExecContext(t.Context(), "UPDATE purchases SET payment_id = NULL WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now, now.Add(time.Minute)} {
		m.clock = at
		m.mustSend(0)
		if m.sentAt(id, "hold").Valid {
			t.Fatalf("hold notice sent at %v, want deferred", at)
		}
	}
	m.clock = now.Add(time.Minute + time.Millisecond)
	m.mustSend(1)
	want := tr.T("ja", i18n.MailHoldBody, "j", 1, "2027-01-15 17:10", lookupURL(id))
	if got := m.mailer.Sent()[0].Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSendMailReportsTheTenthConsecutiveFailureOnce(t *testing.T) {
	m := newMailEnv(t).start()
	id := m.holdAs("a@x", "en", 1)
	mid := m.mailID(id, "hold")
	m.mailer.FailNext(11)
	for i := 1; i <= 11; i++ {
		m.mustSend(0)
		wantReports := 0
		if i >= 10 {
			wantReports = 1
		}
		if m.errs.count() != wantReports || m.svc.MailFailures(mid) != i {
			t.Fatalf("after failure %d: reports = %d, failures = %d; want %d, %d",
				i, m.errs.count(), m.svc.MailFailures(mid), wantReports, i)
		}
	}
	m.mustSend(1)
	if m.svc.MailFailures(mid) != 0 || m.errs.count() != 1 {
		t.Errorf("after success: failures = %d, reports = %d; want 0, 1", m.svc.MailFailures(mid), m.errs.count())
	}
}

func TestSendMailSkipsAFailedRowAndSendsTheNext(t *testing.T) {
	m := newMailEnv(t).start()
	a := m.holdAs("a@x", "en", 1)
	b := m.holdAs("b@x", "en", 1)
	m.mailer.FailNext(1)
	m.mustSend(1)
	if m.sentAt(a, "hold").Valid || !m.sentAt(b, "hold").Valid {
		t.Errorf("sent_at a = %+v, b = %+v; want only b sent", m.sentAt(a, "hold"), m.sentAt(b, "hold"))
	}
	m.mustSend(1)
	if got := m.mailer.Sent(); len(got) != 2 || got[0].To != "b@x" || got[1].To != "a@x" {
		t.Errorf("sent = %+v, want b then a", got)
	}
}

func TestSendMailPurchasedAndRefundBodies(t *testing.T) {
	m := newMailEnv(t).start()
	a := m.holdAs("a@x", "en", 1)
	b := m.holdAs("b@x", "ja", 2)
	c := m.holdAs("c@x", "en", 3)
	for _, x := range []struct {
		id string
		at time.Time
	}{{a, now}, {b, now}, {c, expiresAt}} {
		p := m.purchase(x.id)
		if err := m.svc.Completed(t.Context(), port.Completion{PurchaseID: x.id, PaymentID: *p.PaymentID, Amount: int(p.Amount), At: x.at}); err != nil {
			t.Fatal(err)
		}
	}
	m.mustSend(3)
	want := []port.Mail{
		{To: "a@x", Lang: "en", Subject: tr.T("en", i18n.MailPurchasedSubject, "e"),
			Body: tr.T("en", i18n.MailPurchasedBody, "e", "2027-01-16 17:00 +09:00", "1", lookupURL(a))},
		{To: "b@x", Lang: "ja", Subject: tr.T("ja", i18n.MailPurchasedSubject, "j"),
			Body: tr.T("ja", i18n.MailPurchasedBody, "j", "2027-01-16 17:00", "2-3", lookupURL(b))},
		{To: "c@x", Lang: "en", Subject: tr.T("en", i18n.MailRefundSubject, "e"),
			Body: tr.T("en", i18n.MailRefundBody, "e", "15,000", lookupURL(c))},
	}
	got := m.mailer.Sent()
	if len(got) != len(want) {
		t.Fatalf("sent %d mails, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mail %d = %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestSendMailReturnsErrorForUnknownKind(t *testing.T) {
	m := newMailEnv(t).start()
	id := m.holdAs("a@x", "en", 1)
	if _, err := m.s.Write.ExecContext(t.Context(), "UPDATE mail_outbox SET kind = 'bogus' WHERE purchase_id = ?", id); err != nil {
		t.Fatal(err)
	}
	if n, err := m.send(); err == nil || n != 0 || len(m.mailer.Sent()) != 0 {
		t.Errorf("n = %d, err = %v, sent = %d; want 0, an error, 0", n, err, len(m.mailer.Sent()))
	}
}

func TestSendMailReturnsListError(t *testing.T) {
	m := newMailEnv(t).start()
	_ = m.s.Read.Close()
	if n, err := m.send(); err == nil || n != 0 {
		t.Errorf("n = %d, err = %v; want 0 and an error", n, err)
	}
}

func TestSendMailRefundAmountCarriesTheCurrency(t *testing.T) {
	for _, tc := range []struct {
		lang   string
		amount int
		want   string
	}{
		{"ja", 999, "999円"}, {"ja", 1000, "1,000円"}, {"ja", 1234567, "1,234,567円"},
		{"en", 999, "JPY 999"}, {"en", 1000, "JPY 1,000"}, {"en", 1234567, "JPY 1,234,567"},
	} {
		m := newMailEnv(t).start()
		id := m.holdAs("a@x", tc.lang, 1)
		p := m.purchase(id)
		if err := m.svc.Completed(t.Context(), port.Completion{PurchaseID: id, PaymentID: *p.PaymentID, Amount: int(p.Amount), At: expiresAt}); err != nil {
			t.Fatal(err)
		}
		m.exec("UPDATE purchases SET amount = ? WHERE id = ?", tc.amount, id)
		m.exec("UPDATE mail_outbox SET sent_at = 0 WHERE kind = 'hold'")
		m.mustSend(1)
		if body := m.mailer.Sent()[0].Body; !strings.Contains(body, tc.want) {
			t.Errorf("%s refund body for %d = %q; want it to contain %q", tc.lang, tc.amount, body, tc.want)
		}
	}
}

// A sent mail whose sent_at write fails is sent again on the next run, so the
// write error must reach the caller instead of vanishing.
func TestSendMailReturnsSentAtWriteErrors(t *testing.T) {
	m := newMailEnv(t).start()
	sent := m.holdAs("a@x", "en", 1)
	skipped := m.holdAs("b@x", "en", 1)
	if err := m.svc.Failed(t.Context(), skipped, ""); err != nil {
		t.Fatal(err)
	}
	m.exec("CREATE TRIGGER fail_sent_at BEFORE UPDATE OF sent_at ON mail_outbox BEGIN SELECT RAISE(ABORT, 'disk says no'); END")
	n, err := m.send()
	if n != 1 || len(m.mailer.Sent()) != 1 {
		t.Errorf("n = %d, sent = %d; want 1, 1", n, len(m.mailer.Sent()))
	}
	for _, id := range []string{sent, skipped} {
		if want := fmt.Sprintf("mark mail %d sent", m.mailID(id, "hold")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to contain %q", err, want)
		}
	}
}
