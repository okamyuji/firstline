package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/i18n"
)

const (
	maxEmailBytes = 254
	maxTokenBytes = 512
)

// bun rejects a column the struct lacks, so this covers every column of
// get_event and list_public_events.
type eventRow struct {
	ID               string `bun:"id"`
	OrganizerID      string `bun:"organizer_id"`
	TitleJA          string `bun:"title_ja"`
	TitleEN          string `bun:"title_en"`
	StartsAt         int64  `bun:"starts_at"`
	EndsAt           int64  `bun:"ends_at"`
	Capacity         int    `bun:"capacity"`
	Price            int    `bun:"price"`
	SaleStartsAt     int64  `bun:"sale_starts_at"`
	NextTicketNumber int    `bun:"next_ticket_number"`
	Remaining        int    `bun:"remaining"`
}

type salesRow struct {
	EventID   string `bun:"event_id"`
	Held      int    `bun:"held"`
	Sold      int    `bun:"sold"`
	Remaining int    `bun:"remaining"`
	Refunds   int    `bun:"refunds"`
}

type purchaseRow struct {
	ID               string         `bun:"id"`
	EventID          string         `bun:"event_id"`
	Email            string         `bun:"email"`
	Lang             string         `bun:"lang"`
	Quantity         int            `bun:"quantity"`
	Amount           int            `bun:"amount"`
	State            string         `bun:"state"`
	ExpiresAt        int64          `bun:"expires_at"`
	PaymentID        sql.NullString `bun:"payment_id"`
	CheckoutURL      sql.NullString `bun:"checkout_url"`
	TicketNumberFrom sql.NullInt64  `bun:"ticket_number_from"`
	UpdatedAt        int64          `bun:"updated_at"`
}

type eventView struct {
	ID, Title, Date, Price, SaleStart string
	Remaining, Capacity               int
	// Open matches the sale window app.Hold enforces; SoldOut is checked only inside it.
	Open, SoldOut bool
}

type purchaseView struct {
	ID, State, Tickets, Deadline, CheckoutURL, PollURL string
	Quantity                                           int
	Held                                               bool
}

var stateKeys = map[domain.State]string{
	domain.StateHeld:      i18n.StatusHeld,
	domain.StatePurchased: i18n.StatusPurchased,
	domain.StateReleased:  i18n.StatusFailed,
	domain.StateExpired:   i18n.StatusExpired,
	domain.StateRefunding: i18n.StatusRefunding,
	domain.StateRefunded:  i18n.StatusRefunded,
}

func (v view) event(e eventRow, now time.Time) eventView {
	title := e.TitleJA
	if v.Lang == "en" {
		title = e.TitleEN
	}
	at := now.UnixMilli()
	return eventView{
		ID: e.ID, Title: title, Date: app.JSTTime(v.Lang, e.StartsAt),
		Price: v.T(i18n.PriceYen, app.Thousands(e.Price)), Remaining: e.Remaining, Capacity: e.Capacity, SaleStart: app.JSTTime(v.Lang, e.SaleStartsAt),
		Open: at >= e.SaleStartsAt && at < e.StartsAt, SoldOut: e.Remaining <= 0,
	}
}

func (v view) purchase(p purchaseRow, token string) purchaseView {
	pv := purchaseView{
		ID: p.ID, State: v.T(stateKeys[domain.State(p.State)]), Quantity: p.Quantity,
		Held: p.State == string(domain.StateHeld), PollURL: statusURL(p.ID, token),
		Deadline: app.JSTTime(v.Lang, p.ExpiresAt), CheckoutURL: p.CheckoutURL.String,
	}
	if p.TicketNumberFrom.Valid {
		pv.Tickets = app.TicketRange(p.TicketNumberFrom.Int64, p.Quantity)
	}
	return pv
}

func statusURL(id, token string) string {
	return "/purchases/" + url.PathEscape(id) + "?token=" + url.QueryEscape(token)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	v := s.view(r)
	now := s.Clock()
	var rows []eventRow
	if err := s.Store.Select(r.Context(), s.Store.Read, &rows, "list_public_events", now.UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, e := range rows {
		v.Events = append(v.Events, v.event(e, now))
	}
	v.Title = v.T(i18n.ListHeading)
	s.render(w, r, http.StatusOK, "list", "layout", v)
}

func (s *server) loadEvent(ctx context.Context, id string) (eventRow, error) {
	var e eventRow
	if err := s.Store.Select(ctx, s.Store.Read, &e, "get_event", id); err != nil {
		return e, notFound(err)
	}
	var sales salesRow
	if err := s.Store.Select(ctx, s.Store.Read, &sales, "get_sales", id); err != nil {
		return e, notFound(err)
	}
	e.Remaining = sales.Remaining
	return e, nil
}

func (s *server) detail(w http.ResponseWriter, r *http.Request) {
	s.showDetail(w, r, http.StatusOK, "")
}

func (s *server) showDetail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	e, err := s.loadEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v := s.view(r)
	v.Event = v.event(e, s.Clock())
	v.Title, v.Error = v.Event.Title, msg
	s.render(w, r, status, "detail", "layout", v)
}

// The Location carries a lookup token.
func (s *server) purchase(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.holds.allow(clientIP(r), s.Clock()) {
		s.showDetail(w, r, http.StatusTooManyRequests, s.view(r).T(i18n.ErrTooManyHolds))
		return
	}
	loc, err := s.hold(r, w)
	if err != nil {
		code, msg := s.errorText(r, s.view(r), err)
		s.showDetail(w, r, code, msg)
		return
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// hold returns the status URL of the new purchase.
func (s *server) hold(r *http.Request, w http.ResponseWriter) (string, error) {
	if err := parseForm(w, r); err != nil {
		return "", err
	}
	email := strings.TrimSpace(r.PostForm.Get("email"))
	if !validEmail(email) {
		return "", domain.ErrBadInput
	}
	qty, err := strconv.Atoi(r.PostForm.Get("quantity"))
	if err != nil {
		return "", domain.ErrBadQuantity
	}
	// Loaded first: the token expiry needs ends_at, and a failure after Hold would hide a held purchase.
	e, err := s.loadEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		return "", err
	}
	res, err := s.App.Hold(r.Context(), app.HoldInput{EventID: e.ID, Email: email, Lang: lang(r), Quantity: qty})
	if err != nil {
		return "", err
	}
	return statusURL(res.PurchaseID, s.Tokens.Issue(res.PurchaseID, app.TokenExpiry(e.EndsAt))), nil
}

// validEmail requires a bare address: Address == input rejects display names.
func validEmail(email string) bool {
	if len(email) > maxEmailBytes {
		return false
	}
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email
}

// byToken loads the purchase a valid token names. Every way the token can be
// wrong is ErrNotFound, so the caller cannot tell them apart.
func (s *server) byToken(ctx context.Context, token string) (purchaseRow, error) {
	var p purchaseRow
	id, err := s.Tokens.Verify(token, s.Clock())
	if err != nil {
		return p, domain.ErrNotFound
	}
	err = s.Store.Select(ctx, s.Store.Read, &p, "get_purchase", id)
	return p, notFound(err)
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := r.URL.Query().Get("token")
	p, err := s.byToken(r.Context(), token)
	if err == nil && p.ID != r.PathValue("id") {
		err = domain.ErrNotFound
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v := s.view(r)
	v.Purchase = v.purchase(p, token)
	v.Title = v.T(i18n.StatusHeading)
	if r.Header.Get("HX-Request") != "true" {
		s.render(w, r, http.StatusOK, "purchase", "layout", v)
		return
	}
	code := http.StatusOK
	if !v.Purchase.Held {
		code = statusStopPolling
	}
	s.render(w, r, code, "purchase", "status", v)
}

func (s *server) lookupForm(w http.ResponseWriter, r *http.Request) {
	s.showLookup(w, r, http.StatusOK, "")
}

func (s *server) showLookup(w http.ResponseWriter, r *http.Request, status int, key string) {
	w.Header().Set("Cache-Control", "no-store")
	v := s.view(r)
	v.Title = v.T(i18n.LookupHeading)
	if key != "" {
		v.Error = v.T(key)
	}
	s.render(w, r, status, "lookup", "layout", v)
}

// lookup answers a wrong email and a wrong token with the same page, so the
// form cannot be used to learn which one was right.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := parseForm(w, r); err != nil {
		s.showLookup(w, r, http.StatusBadRequest, i18n.ErrBadInput)
		return
	}
	email := strings.TrimSpace(r.PostForm.Get("email"))
	token := strings.TrimSpace(r.PostForm.Get("token"))
	if len(email) > maxEmailBytes || len(token) > maxTokenBytes {
		s.showLookup(w, r, http.StatusBadRequest, i18n.ErrBadInput)
		return
	}
	p, err := s.byToken(r.Context(), token)
	if errors.Is(err, domain.ErrNotFound) || err == nil && p.Email != strings.ToLower(email) {
		s.showLookup(w, r, http.StatusOK, i18n.LookupMismatch)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, statusURL(p.ID, token), http.StatusSeeOther)
}
