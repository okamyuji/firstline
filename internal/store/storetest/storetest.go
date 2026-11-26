// Package storetest holds the store tests that every dialect must pass.
package storetest

import (
	"database/sql"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/migrate"
	"github.com/okamyuji/firstline/internal/store"
)

// Factory returns a migrated store backed by an empty database.
type Factory func(t *testing.T) *store.Store

// Names lists every named statement a dialect must provide.
var Names = slices.Concat([]string{
	"insert_organizer", "get_organizer_by_email",
	"insert_event", "insert_event_sales", "get_event", "list_public_events", "list_events_by_organizer", "get_sales",
	"insert_purchase", "hold_seats", "count_active_holds", "record_payment", "get_purchase", "set_state",
	"release_seats", "sell_seats", "count_refund",
	"advance_ticket_number", "get_ticket_base", "assign_ticket",
	"insert_billing",
	"insert_orphan", "list_refunding_orphans", "set_orphan_refunded",
	"list_due_holds", "list_refunding",
	"insert_mail", "list_unsent_mail", "mark_mail_sent",
	"inventory_counts",
	"insert_session", "get_session", "touch_session", "delete_session", "delete_expired_sessions", "delete_old_access_logs",
	"insert_access_log",
}, transferNames())

func transferNames() []string {
	var out []string
	for _, t := range migrate.Tables {
		out = append(out, "export_"+t, "import_"+t)
	}
	return out
}

// dialectOnly lists the files only some dialects have, so the name set leaves them out.
var dialectOnly = []string{"setval_mail_outbox", "check_durability"}

// Run runs every shared test against a fresh store from f.
func Run(t *testing.T, f Factory) {
	cases := []struct {
		name string
		fn   func(*testing.T, *env)
	}{
		{"names", testNames},
		{"organizer", testOrganizer},
		{"events and sales", testEvents},
		{"purchase and holds", testPurchase},
		{"hold_seats boundary", testHoldSeats},
		{"set_state", testSetState},
		{"record_payment", testRecordPayment},
		{"seat counters", testSeatCounters},
		{"ticket numbers", testTickets},
		{"billing", testBilling},
		{"orphan refunds", testOrphans},
		{"due and refunding", testDue},
		{"mail", testMail},
		{"inventory_counts", testInventory},
		{"sessions", testSessions},
		{"access log", testAccessLog},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.fn(t, &env{t: t, s: f(t)})
		})
	}
}

type env struct {
	t *testing.T
	s *store.Store
}

func (e *env) exec(name string, args ...any) int64 {
	e.t.Helper()
	res, err := e.s.Exec(e.t.Context(), e.s.Write, name, args...)
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		e.t.Fatalf("%s rows affected: %v", name, err)
	}
	return n
}

func (e *env) sel(dest any, name string, args ...any) {
	e.t.Helper()
	if err := e.s.Select(e.t.Context(), e.s.Write, dest, name, args...); err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func eqSlice[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %+v, want %+v", what, got, want)
	}
}

const (
	org     = "org1"
	evt     = "evt1"
	start   = int64(2_000_000)
	saleAt  = int64(1_000_000)
	price   = int64(5000)
	nextNum = 1
)

func (e *env) seedEvent(id, organizer string, startsAt int64, capacity int) {
	e.t.Helper()
	e.exec("insert_event", id, organizer, "題名 "+id, "Title "+id, startsAt, startsAt+1000, capacity, price, saleAt, nextNum)
	e.exec("insert_event_sales", id, capacity)
}

func (e *env) seedPurchase(id, eventID, email string, qty int, expiresAt int64) {
	e.t.Helper()
	e.exec("insert_purchase", id, eventID, email, "ja", qty, int64(qty)*price, expiresAt, int64(10))
}

type purchaseRow struct {
	ID               string         `bun:"id"`
	EventID          string         `bun:"event_id"`
	Email            string         `bun:"email"`
	Lang             string         `bun:"lang"`
	Quantity         int            `bun:"quantity"`
	Amount           int64          `bun:"amount"`
	State            string         `bun:"state"`
	ExpiresAt        int64          `bun:"expires_at"`
	PaymentID        sql.NullString `bun:"payment_id"`
	CheckoutURL      sql.NullString `bun:"checkout_url"`
	TicketNumberFrom sql.NullInt64  `bun:"ticket_number_from"`
	UpdatedAt        int64          `bun:"updated_at"`
}

type salesRow struct {
	EventID   string `bun:"event_id"`
	Held      int    `bun:"held"`
	Sold      int    `bun:"sold"`
	Remaining int    `bun:"remaining"`
	Refunds   int    `bun:"refunds"`
}

func (e *env) purchase(id string) purchaseRow {
	e.t.Helper()
	var p purchaseRow
	e.sel(&p, "get_purchase", id)
	return p
}

func (e *env) sales(id string) salesRow {
	e.t.Helper()
	var s salesRow
	e.sel(&s, "get_sales", id)
	return s
}

func testNames(t *testing.T, e *env) {
	entries, err := fs.ReadDir(e.s.SQL, "sql")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, en := range entries {
		if n := strings.TrimSuffix(en.Name(), ".sql"); !slices.Contains(dialectOnly, n) {
			got = append(got, n)
		}
	}
	want := append([]string{"schema"}, Names...)
	slices.Sort(got)
	slices.Sort(want)
	eqSlice(t, "sql names", got, want)

	// The driver runs every statement of a multi-statement string, so a stray ";" would run hidden SQL.
	for _, n := range Names {
		b, err := fs.ReadFile(e.s.SQL, "sql/"+n+".sql")
		if err != nil {
			t.Fatal(err)
		}
		if body := strings.TrimSuffix(strings.TrimSpace(withoutComments(string(b))), ";"); strings.Contains(body, ";") {
			t.Errorf("%s.sql holds more than one statement", n)
		}
		// bun replaces every "?", so one in a comment or quote would shift the arguments.
		if q := string(b); strings.Count(q, "?") != store.Placeholders(q) {
			t.Errorf("%s.sql has a \"?\" inside a comment or quoted text", n)
		}
	}
}

func withoutComments(q string) string {
	var b strings.Builder
	for line := range strings.Lines(q) {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
		}
	}
	return b.String()
}

func testOrganizer(t *testing.T, e *env) {
	type row struct {
		ID           string `bun:"id"`
		Email        string `bun:"email"`
		PasswordHash string `bun:"password_hash"`
	}
	e.exec("insert_organizer", org, "a@example.com", "hash-a")
	var r row
	e.sel(&r, "get_organizer_by_email", "a@example.com")
	eq(t, "organizer", r, row{org, "a@example.com", "hash-a"})

	_, err := e.s.Exec(t.Context(), e.s.Write, "insert_organizer", "org2", "a@example.com", "hash-b")
	if err == nil || !e.s.IsUnique(err) {
		t.Errorf("duplicate email error = %v, want a unique violation", err)
	}
	err = e.s.Select(t.Context(), e.s.Write, &r, "get_organizer_by_email", "missing@example.com")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown email error = %v, want sql.ErrNoRows", err)
	}
}

type listRow struct {
	ID          string `bun:"id"`
	TitleJA     string `bun:"title_ja"`
	TitleEN     string `bun:"title_en"`
	StartsAt    int64  `bun:"starts_at"`
	EndsAt      int64  `bun:"ends_at"`
	Price       int64  `bun:"price"`
	SaleStartAt int64  `bun:"sale_starts_at"`
	Remaining   int    `bun:"remaining"`
}

type event struct {
	ID           string `bun:"id"`
	OrganizerID  string `bun:"organizer_id"`
	TitleJA      string `bun:"title_ja"`
	TitleEN      string `bun:"title_en"`
	StartsAt     int64  `bun:"starts_at"`
	EndsAt       int64  `bun:"ends_at"`
	Capacity     int    `bun:"capacity"`
	Price        int64  `bun:"price"`
	SaleStartsAt int64  `bun:"sale_starts_at"`
	NextTicket   int    `bun:"next_ticket_number"`
}

func testEvents(t *testing.T, e *env) {
	const now = int64(1000)
	// Events with the same starts_at go in neither id order nor its reverse, so only the id tie-break sorts them.
	e.seedEvent("late", org, start+500, 7)
	e.seedEvent("early2", org, start, 9)
	e.seedEvent("other", "org2", start, 4)
	e.seedEvent("early", org, start, 10)
	e.seedEvent("early3", org, start, 8)
	e.seedEvent("past", org, 100, 3)
	e.seedEvent("at-now", org, now, 5)

	var ev event
	e.sel(&ev, "get_event", "early")
	eq(t, "get_event", ev, event{"early", org, "題名 early", "Title early", start, start + 1000, 10, price, saleAt, nextNum})
	eq(t, "get_sales", e.sales("early"), salesRow{"early", 0, 0, 10, 0})

	ids := func(rows []listRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}
	var pub []listRow
	e.sel(&pub, "list_public_events", now)
	eqSlice(t, "public order (starts_at, id; started at or before now excluded)", ids(pub),
		[]string{"early", "early2", "early3", "other", "late"})
	eq(t, "public row", pub[0], listRow{"early", "題名 early", "Title early", start, start + 1000, price, saleAt, 10})

	var mine []listRow
	e.sel(&mine, "list_events_by_organizer", org)
	eqSlice(t, "organizer events", ids(mine), []string{"past", "at-now", "early", "early2", "early3", "late"})
	eq(t, "organizer remaining", mine[0].Remaining, 3)
}

func testPurchase(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 10)
	e.seedEvent("evt2", org, start, 10)
	e.seedPurchase("p1", evt, "a@example.com", 2, 600)
	e.seedPurchase("p2", evt, "b@example.com", 1, 600)
	e.seedPurchase("p3", "evt2", "a@example.com", 1, 600)
	e.seedPurchase("p4", evt, "a@example.com", 1, 600)
	e.exec("set_state", "released", int64(11), "p4", "held")

	eq(t, "get_purchase", e.purchase("p1"), purchaseRow{
		ID: "p1", EventID: evt, Email: "a@example.com", Lang: "ja", Quantity: 2, Amount: 2 * price,
		State: "held", ExpiresAt: 600, UpdatedAt: 10,
	})
	var n int
	e.sel(&n, "count_active_holds", evt, "a@example.com")
	eq(t, "active holds (same event and email, held only)", n, 1)

	// The comparison is exact in every dialect, so callers store and query the email lower-cased.
	e.seedPurchase("p5", evt, "A@Example.com", 1, 600)
	e.sel(&n, "count_active_holds", evt, "a@example.com")
	eq(t, "active holds ignore a differently cased email", n, 1)
	e.sel(&n, "count_active_holds", evt, "A@Example.com")
	eq(t, "active holds match the exact email", n, 1)
}

func testHoldSeats(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 4)
	eq(t, "hold 3 of 4", e.exec("hold_seats", 3, 3, evt, 3), 1)
	eq(t, "sales after hold", e.sales(evt), salesRow{evt, 3, 0, 1, 0})
	eq(t, "hold 2 of 1 remaining", e.exec("hold_seats", 2, 2, evt, 2), 0)
	eq(t, "sales unchanged by short hold", e.sales(evt), salesRow{evt, 3, 0, 1, 0})
	eq(t, "hold exactly remaining", e.exec("hold_seats", 1, 1, evt, 1), 1)
	eq(t, "sales when full", e.sales(evt), salesRow{evt, 4, 0, 0, 0})
}

func testSetState(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 4)
	e.seedPurchase("p1", evt, "a@example.com", 1, 600)
	eq(t, "wrong old state", e.exec("set_state", "expired", int64(20), "p1", "purchased"), 0)
	p := e.purchase("p1")
	eq(t, "state after mismatch", p.State, "held")
	eq(t, "updated_at after mismatch", p.UpdatedAt, 10)
	eq(t, "matching old state", e.exec("set_state", "expired", int64(20), "p1", "held"), 1)
	p = e.purchase("p1")
	eq(t, "state after change", p.State, "expired")
	eq(t, "updated_at after change", p.UpdatedAt, 20)
	eq(t, "unknown id", e.exec("set_state", "expired", int64(20), "nope", "held"), 0)
}

func testRecordPayment(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 4)
	e.seedPurchase("p1", evt, "a@example.com", 1, 600)
	eq(t, "first record", e.exec("record_payment", "pay1", "https://pay.example/1", "p1"), 1)
	eq(t, "second record with other values", e.exec("record_payment", "pay2", "https://pay.example/2", "p1"), 0)
	p := e.purchase("p1")
	eq(t, "payment_id", p.PaymentID, sql.NullString{String: "pay1", Valid: true})
	eq(t, "checkout_url", p.CheckoutURL, sql.NullString{String: "https://pay.example/1", Valid: true})
	eq(t, "state untouched", p.State, "held")
}

func testSeatCounters(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 10)
	e.seedEvent("evt2", org, start, 10)
	e.exec("hold_seats", 5, 5, evt, 5)
	e.exec("hold_seats", 6, 6, "evt2", 6)
	eq(t, "sell 2", e.exec("sell_seats", 2, 2, evt, 2), 1)
	eq(t, "after sell", e.sales(evt), salesRow{evt, 3, 2, 5, 0})
	eq(t, "sell 4 of 3 held", e.exec("sell_seats", 4, 4, evt, 4), 0)
	eq(t, "release 4 of 3 held", e.exec("release_seats", 4, 4, evt, 4), 0)
	eq(t, "unchanged by over-count", e.sales(evt), salesRow{evt, 3, 2, 5, 0})
	eq(t, "release exactly held", e.exec("release_seats", 3, 3, evt, 3), 1)
	eq(t, "after release", e.sales(evt), salesRow{evt, 0, 2, 8, 0})
	e.exec("hold_seats", 2, 2, evt, 2)
	eq(t, "sell exactly held", e.exec("sell_seats", 2, 2, evt, 2), 1)
	eq(t, "after selling all held", e.sales(evt), salesRow{evt, 0, 4, 6, 0})
	eq(t, "count refund", e.exec("count_refund", evt), 1)
	e.exec("count_refund", evt)
	eq(t, "after two refunds", e.sales(evt), salesRow{evt, 0, 4, 6, 2})
	eq(t, "other event untouched", e.sales("evt2"), salesRow{"evt2", 6, 0, 4, 0})
}

func testTickets(t *testing.T, e *env) {
	// evt sits between two other events so a query that ignores the id picks another row first in either scan order.
	e.seedEvent("evt0", org, start, 10)
	e.seedEvent(evt, org, start, 10)
	e.seedEvent("evt2", org, start, 10)
	e.seedPurchase("p1", evt, "a@example.com", 3, 600)
	e.seedPurchase("p2", evt, "b@example.com", 2, 600)
	base := func(id string, qty int) int {
		e.exec("advance_ticket_number", qty, evt)
		var b int
		e.sel(&b, "get_ticket_base", qty, evt)
		e.exec("assign_ticket", b, int64(30), id)
		return b
	}
	eq(t, "first base", base("p1", 3), 1)
	eq(t, "second base", base("p2", 2), 4)
	p := e.purchase("p1")
	eq(t, "p1 ticket", p.TicketNumberFrom, sql.NullInt64{Int64: 1, Valid: true})
	eq(t, "p1 updated_at", p.UpdatedAt, 30)
	eq(t, "p2 ticket", e.purchase("p2").TicketNumberFrom, sql.NullInt64{Int64: 4, Valid: true})
	var ev event
	e.sel(&ev, "get_event", evt)
	eq(t, "next_ticket_number", ev.NextTicket, 6)
	for _, id := range []string{"evt0", "evt2"} {
		e.sel(&ev, "get_event", id)
		eq(t, id+" next_ticket_number", ev.NextTicket, nextNum)
	}
}

func testBilling(t *testing.T, e *env) {
	e.exec("insert_billing", "p1", "pay1", "charge", 5000, int64(40))
	e.exec("insert_billing", "p1", "pay1", "refund", 5000, int64(50))
	type row struct {
		PurchaseID string `bun:"purchase_id"`
		PaymentID  string `bun:"payment_id"`
		Kind       string `bun:"kind"`
		Amount     int64  `bun:"amount"`
		At         int64  `bun:"at"`
	}
	var rows []row
	if err := e.s.Write.NewSelect().TableExpr("billing_logs").
		ColumnExpr("purchase_id, payment_id, kind, amount, at").OrderExpr("at").Scan(t.Context(), &rows); err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "billing rows", rows, []row{{"p1", "pay1", "charge", 5000, 40}, {"p1", "pay1", "refund", 5000, 50}})
}

type orphanRow struct {
	PaymentID  string `bun:"payment_id"`
	PurchaseID string `bun:"purchase_id"`
	Amount     int64  `bun:"amount"`
}

func testOrphans(t *testing.T, e *env) {
	eq(t, "first insert", e.exec("insert_orphan", "payB", "pur1", 5000, int64(60)), 1)
	eq(t, "duplicate insert", e.exec("insert_orphan", "payB", "pur-other", 9999, int64(61)), 0)
	e.exec("insert_orphan", "payC", "pur3", 4000, int64(62))
	e.exec("insert_orphan", "payA", "pur2", 3000, int64(62))

	var rows []orphanRow
	e.sel(&rows, "list_refunding_orphans")
	eqSlice(t, "refunding orphans", rows, []orphanRow{{"payA", "pur2", 3000}, {"payB", "pur1", 5000}, {"payC", "pur3", 4000}})

	eq(t, "mark refunded", e.exec("set_orphan_refunded", int64(70), "payA"), 1)
	eq(t, "mark refunded twice", e.exec("set_orphan_refunded", int64(71), "payA"), 0)
	rows = nil
	e.sel(&rows, "list_refunding_orphans")
	eqSlice(t, "orphans left", rows, []orphanRow{{"payB", "pur1", 5000}, {"payC", "pur3", 4000}})
}

func testDue(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 10)
	e.seedPurchase("due-b", evt, "b@example.com", 2, 1000)
	e.seedPurchase("due-a", evt, "a@example.com", 1, 1000)
	e.seedPurchase("early", evt, "c@example.com", 1, 500)
	e.seedPurchase("late", evt, "d@example.com", 1, 1001)
	e.seedPurchase("bought", evt, "e@example.com", 1, 400)
	e.exec("set_state", "purchased", int64(11), "bought", "held")
	e.seedPurchase("refund2", evt, "f@example.com", 1, 400)
	e.seedPurchase("refund3", evt, "i@example.com", 1, 400)
	e.seedPurchase("refund1", evt, "g@example.com", 1, 400)
	e.seedPurchase("nopay", evt, "h@example.com", 1, 400)
	for _, id := range []string{"refund2", "refund3", "refund1", "nopay"} {
		e.exec("set_state", "refunding", int64(11), id, "held")
	}
	e.exec("record_payment", "pay-r2", "", "refund2")
	e.exec("record_payment", "pay-r3", "", "refund3")
	e.exec("record_payment", "pay-r1", "", "refund1")
	// Purchases in every other state carry a payment and an expired deadline too; neither list may take them.
	for _, st := range []string{"released", "expired", "refunded"} {
		e.seedPurchase(st, evt, st+"@example.com", 1, 400)
		e.exec("set_state", st, int64(11), st, "held")
	}
	for _, id := range []string{"bought", "due-a", "released", "expired", "refunded"} {
		e.exec("record_payment", "pay-"+id, "", id)
	}

	type due struct {
		ID        string `bun:"id"`
		EventID   string `bun:"event_id"`
		Quantity  int    `bun:"quantity"`
		ExpiresAt int64  `bun:"expires_at"`
	}
	var rows []due
	e.sel(&rows, "list_due_holds", int64(1000))
	eqSlice(t, "due holds (expires_at <= cutoff, by expires_at then id)", rows,
		[]due{{"early", evt, 1, 500}, {"due-a", evt, 1, 1000}, {"due-b", evt, 2, 1000}})

	type refunding struct {
		ID        string `bun:"id"`
		PaymentID string `bun:"payment_id"`
		Amount    int64  `bun:"amount"`
	}
	var ref []refunding
	e.sel(&ref, "list_refunding")
	eqSlice(t, "refunding purchases with a payment", ref, []refunding{{"refund1", "pay-r1", price}, {"refund2", "pay-r2", price}, {"refund3", "pay-r3", price}})
}

func testMail(t *testing.T, e *env) {
	e.seedEvent(evt, org, start, 10)
	e.seedEvent("evt2", org, start+1, 10)
	e.seedPurchase("p-z", evt, "z@example.com", 2, 600)
	e.seedPurchase("p-a", evt, "a@example.com", 1, 700)
	e.seedPurchase("p-o", "evt2", "o@example.com", 1, 800)
	e.exec("record_payment", "pay-a", "https://pay.example/a", "p-a")
	e.exec("insert_mail", "p-z", "held", "en")
	e.exec("insert_mail", "p-a", "held", "ja")
	e.exec("insert_mail", "p-z", "purchased", "en")
	e.exec("insert_mail", "p-o", "held", "ja")

	type mailRow struct {
		ID               int64          `bun:"id"`
		PurchaseID       string         `bun:"purchase_id"`
		Kind             string         `bun:"kind"`
		Lang             string         `bun:"lang"`
		State            string         `bun:"state"`
		PaymentID        sql.NullString `bun:"payment_id"`
		CheckoutURL      sql.NullString `bun:"checkout_url"`
		Email            string         `bun:"email"`
		Quantity         int            `bun:"quantity"`
		Amount           int64          `bun:"amount"`
		ExpiresAt        int64          `bun:"expires_at"`
		TicketNumberFrom sql.NullInt64  `bun:"ticket_number_from"`
		TitleJA          string         `bun:"title_ja"`
		TitleEN          string         `bun:"title_en"`
		StartsAt         int64          `bun:"starts_at"`
		EndsAt           int64          `bun:"ends_at"`
	}
	var rows []mailRow
	e.sel(&rows, "list_unsent_mail")
	if len(rows) != 4 {
		t.Fatalf("unsent mail = %d rows, want 4", len(rows))
	}
	ids := []int64{rows[0].ID, rows[1].ID, rows[2].ID, rows[3].ID}
	if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Errorf("mail ids = %v, want strictly ascending", ids)
	}
	eq(t, "other event's mail", [4]any{rows[3].PurchaseID, rows[3].TitleJA, rows[3].StartsAt, rows[3].EndsAt}, [4]any{"p-o", "題名 evt2", start + 1, start + 1001})
	eq(t, "first mail", rows[0], mailRow{
		ID: ids[0], PurchaseID: "p-z", Kind: "held", Lang: "en", State: "held", Email: "z@example.com",
		Quantity: 2, Amount: 2 * price, ExpiresAt: 600, TitleJA: "題名 evt1", TitleEN: "Title evt1", StartsAt: start, EndsAt: start + 1000,
	})
	eq(t, "second mail", rows[1], mailRow{
		ID: ids[1], PurchaseID: "p-a", Kind: "held", Lang: "ja", State: "held",
		PaymentID:   sql.NullString{String: "pay-a", Valid: true},
		CheckoutURL: sql.NullString{String: "https://pay.example/a", Valid: true},
		Email:       "a@example.com", Quantity: 1, Amount: price, ExpiresAt: 700,
		TitleJA: "題名 evt1", TitleEN: "Title evt1", StartsAt: start, EndsAt: start + 1000,
	})

	eq(t, "mark sent", e.exec("mark_mail_sent", int64(80), ids[1]), 1)
	rows = nil
	e.sel(&rows, "list_unsent_mail")
	if len(rows) != 3 || rows[0].ID != ids[0] || rows[1].ID != ids[2] || rows[2].ID != ids[3] {
		t.Errorf("unsent after mark = %+v, want ids %d, %d and %d", rows, ids[0], ids[2], ids[3])
	}
}

func testInventory(t *testing.T, e *env) {
	e.seedEvent("e-b", org, start, 10)
	e.seedEvent("e-a", org, start, 6)
	e.seedPurchase("p1", "e-b", "a@example.com", 2, 600)
	e.seedPurchase("p2", "e-b", "b@example.com", 3, 600)
	e.seedPurchase("p3", "e-b", "c@example.com", 4, 600)
	e.seedPurchase("p4", "e-b", "d@example.com", 1, 600)
	e.exec("set_state", "purchased", int64(11), "p2", "held")
	e.exec("set_state", "released", int64(11), "p3", "held")
	e.exec("set_state", "refunding", int64(11), "p4", "held")

	type count struct {
		EventID  string `bun:"event_id"`
		Capacity int    `bun:"capacity"`
		Active   int    `bun:"active"`
	}
	var rows []count
	e.sel(&rows, "inventory_counts")
	// The rebuild adds each event on its own, so the statement has no order to keep.
	slices.SortFunc(rows, func(a, b count) int { return strings.Compare(a.EventID, b.EventID) })
	eqSlice(t, "inventory", rows, []count{{"e-a", 6, 0}, {"e-b", 10, 5}})
}

type sessionRow struct {
	IDHash      string `bun:"id_hash"`
	OrganizerID string `bun:"organizer_id"`
	CreatedAt   int64  `bun:"created_at"`
	LastSeenAt  int64  `bun:"last_seen_at"`
}

func testSessions(t *testing.T, e *env) {
	e.exec("insert_session", "h1", org, int64(100), int64(100))
	e.exec("insert_session", "h2", org, int64(100), int64(100))
	var s sessionRow
	e.sel(&s, "get_session", "h1")
	eq(t, "session", s, sessionRow{"h1", org, 100, 100})

	eq(t, "touch", e.exec("touch_session", int64(150), "h1"), 1)
	e.sel(&s, "get_session", "h1")
	eq(t, "last_seen_at", s.LastSeenAt, 150)
	eq(t, "created_at kept", s.CreatedAt, 100)
	e.sel(&s, "get_session", "h2")
	eq(t, "other session untouched", s.LastSeenAt, 100)

	e.exec("insert_session", "old", org, int64(10), int64(900))
	e.exec("insert_session", "idle", org, int64(900), int64(20))
	e.exec("insert_session", "edge", org, int64(50), int64(900))
	e.exec("insert_session", "fresh", org, int64(900), int64(900))
	eq(t, "delete expired", e.exec("delete_expired_sessions", int64(50), int64(20)), 3)
	for _, id := range []string{"old", "idle", "edge"} {
		err := e.s.Select(t.Context(), e.s.Write, &s, "get_session", id)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("session %s error = %v, want sql.ErrNoRows", id, err)
		}
	}
	e.sel(&s, "get_session", "fresh")

	eq(t, "delete", e.exec("delete_session", "h1"), 1)
	eq(t, "delete again", e.exec("delete_session", "h1"), 0)
}

func testAccessLog(t *testing.T, e *env) {
	e.exec("insert_access_log", "GET", "/events/x", 200, int64(12), int64(90))
	type row struct {
		Method     string `bun:"method"`
		Path       string `bun:"path"`
		Status     int    `bun:"status"`
		DurationMS int64  `bun:"duration_ms"`
		At         int64  `bun:"at"`
	}
	var rows []row
	if err := e.s.Write.NewSelect().TableExpr("access_logs").
		ColumnExpr("method, path, status, duration_ms, at").Scan(t.Context(), &rows); err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "access logs", rows, []row{{"GET", "/events/x", 200, 12, 90}})

	e.exec("insert_access_log", "GET", "/old", 200, int64(1), int64(89))
	eq(t, "delete old access logs", e.exec("delete_old_access_logs", int64(90)), 1)
	eq(t, "keep the cutoff row", e.exec("delete_old_access_logs", int64(90)), 0)
}
