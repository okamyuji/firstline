package app_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/writer"
)

const (
	price    = 5000
	capacity = 10
)

var (
	now       = time.UnixMilli(1_800_000_000_000).UTC()
	saleStart = now.Add(-time.Hour)
	eventAt   = now.Add(24 * time.Hour)
	errBoom   = errors.New("boom")
)

type reporter struct {
	mu   sync.Mutex
	errs []error
}

func (r *reporter) Report(_ context.Context, err error, _ ...slog.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *reporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// stubPayment lets a test act on the purchase row between the hold commit and step 3.
type stubPayment struct {
	port.Payment
	start func(ctx context.Context, purchaseID string, amount int) (string, string, error)
}

func (p stubPayment) Start(ctx context.Context, id string, amount int) (string, string, error) {
	return p.start(ctx, id, amount)
}

// releaseFails returns seats to inner but reports an error, so a test sees both the
// seat count and the joined error.
type releaseFails struct{ port.Inventory }

func (r releaseFails) Release(ctx context.Context, id string, q int) error {
	return errors.Join(r.Inventory.Release(ctx, id, q), errBoom)
}

type env struct {
	t     *testing.T
	s     *store.Store
	inv   port.Inventory
	pay   *fake.Fake
	errs  *reporter
	clock time.Time
	deps  app.Deps
}

func newEnv(t *testing.T, inv port.Inventory) *env {
	t.Helper()
	s := openStore(t)
	w, err := writer.New(s.Write, writer.Options{
		QueueCap: 256, BatchMax: 64, BatchWait: time.Millisecond,
		IsIOError:    s.IsIOError,
		OnFatal:      func(err error) { t.Errorf("writer fatal: %v", err) },
		Now:          time.Now,
		After:        time.After,
		OnSyncResult: func(time.Time, time.Duration) {},
		OnAsyncError: func(err error) { t.Errorf("async error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run()
	t.Cleanup(w.Stop)
	// The payment never completes: these tests stop at step 3 of the purchase flow.
	pay := fake.New(time.Now, func(time.Duration) <-chan time.Time { return nil }, time.Second, fake.Complete)
	t.Cleanup(pay.Stop)
	e := &env{t: t, s: s, inv: inv, pay: pay, errs: &reporter{}, clock: now}
	e.deps = app.Deps{
		Store: s, Writer: w, Inventory: inv, Payment: pay, Errors: e.errs,
		Clock: func() time.Time { return e.clock },
	}
	e.event("e1", capacity)
	return e
}

func (e *env) svc() *app.Service { return app.New(e.deps) }

func (e *env) event(id string, capacity int) {
	e.t.Helper()
	ctx := e.t.Context()
	if _, err := e.s.Exec(ctx, e.s.Write, "insert_event", id, "o", "j", "e",
		eventAt.UnixMilli(), eventAt.Add(time.Hour).UnixMilli(), capacity, price, saleStart.UnixMilli(), 1); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.s.Exec(ctx, e.s.Write, "insert_event_sales", id, capacity); err != nil {
		e.t.Fatal(err)
	}
	if err := e.inv.Add(ctx, id, capacity); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) hold(email string, qty int) (app.HoldResult, error) {
	return e.svc().Hold(e.t.Context(), app.HoldInput{EventID: "e1", Email: email, Lang: "en", Quantity: qty})
}

type purchase struct {
	EventID, Email, Lang, State string
	Quantity                    int
	Amount, ExpiresAt           int64
	PaymentID, CheckoutURL      *string
}

func (e *env) purchase(id string) purchase {
	e.t.Helper()
	var p purchase
	err := e.s.Write.QueryRowContext(e.t.Context(),
		"SELECT event_id, email, lang, state, quantity, amount, expires_at, payment_id, checkout_url FROM purchases WHERE id = ?", id).
		Scan(&p.EventID, &p.Email, &p.Lang, &p.State, &p.Quantity, &p.Amount, &p.ExpiresAt, &p.PaymentID, &p.CheckoutURL)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) sales() (held, remaining int) {
	e.t.Helper()
	if err := e.s.Write.QueryRowContext(e.t.Context(), "SELECT held, remaining FROM event_sales WHERE event_id = 'e1'").
		Scan(&held, &remaining); err != nil {
		e.t.Fatal(err)
	}
	return held, remaining
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.s.Write.QueryRowContext(e.t.Context(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) wantRemaining(want int) {
	e.t.Helper()
	got, err := e.inv.Remaining(e.t.Context(), "e1")
	if err != nil || got != want {
		e.t.Errorf("inventory remaining = %d, %v; want %d", got, err, want)
	}
}

func TestHoldCreatesHeldPurchase(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.NewID = func() string { return "p1" }
	res, err := e.hold("  Buyer@Example.COM ", 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.PurchaseID != "p1" {
		t.Errorf("PurchaseID = %q, want p1", res.PurchaseID)
	}
	if !res.ExpiresAt.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("ExpiresAt = %v, want %v", res.ExpiresAt, now.Add(10*time.Minute))
	}
	p := e.purchase("p1")
	if p.PaymentID == nil || *p.PaymentID == "" {
		t.Fatalf("payment_id not recorded: %+v", p)
	}
	want := purchase{EventID: "e1", Email: "buyer@example.com", Lang: "en", State: "held", Quantity: 2,
		Amount: 2 * price, ExpiresAt: now.Add(10 * time.Minute).UnixMilli(), PaymentID: p.PaymentID, CheckoutURL: p.CheckoutURL}
	if p != want {
		t.Errorf("purchase = %+v, want %+v", p, want)
	}
	if held, remaining := e.sales(); held != 2 || remaining != capacity-2 {
		t.Errorf("event_sales held = %d, remaining = %d; want 2, %d", held, remaining, capacity-2)
	}
	if n := e.count("SELECT COUNT(*) FROM mail_outbox WHERE purchase_id = 'p1' AND kind = 'hold' AND lang = 'en'"); n != 1 {
		t.Errorf("hold mail rows = %d, want 1", n)
	}
	e.wantRemaining(capacity - 2)
}

func TestHoldDefaultIDIsRandomBase64URL(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	a, err := e.hold("a@x", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.hold("b@x", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 16 bytes in unpadded base64url are 22 characters.
	if len(a.PurchaseID) != 22 || len(b.PurchaseID) != 22 || a.PurchaseID == b.PurchaseID {
		t.Errorf("ids = %q, %q", a.PurchaseID, b.PurchaseID)
	}
}

func TestHoldQuantityBounds(t *testing.T) {
	for _, tc := range []struct {
		qty  int
		want error
	}{{0, domain.ErrBadQuantity}, {1, nil}, {4, nil}, {5, domain.ErrBadQuantity}} {
		e := newEnv(t, inventory.NewMemory())
		_, err := e.hold("a@x", tc.qty)
		if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("quantity %d: err = %v, want %v", tc.qty, err, tc.want)
		}
		if tc.want != nil {
			e.wantRemaining(capacity)
		}
	}
}

func TestHoldSaleWindow(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want error
	}{
		{saleStart.Add(-time.Millisecond), domain.ErrNotOnSale},
		{saleStart, nil},
		{eventAt.Add(-time.Millisecond), nil},
		{eventAt, domain.ErrNotOnSale},
	} {
		e := newEnv(t, inventory.NewMemory())
		e.clock = tc.at
		_, err := e.hold("a@x", 1)
		if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("at %v: err = %v, want %v", tc.at, err, tc.want)
		}
		if tc.want != nil {
			e.wantRemaining(capacity)
			if held, remaining := e.sales(); held != 0 || remaining != capacity {
				t.Errorf("event_sales held = %d, remaining = %d after a rejected hold", held, remaining)
			}
		}
	}
}

func TestHoldRejectsDuplicateActiveHold(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	if _, err := e.hold("a@x", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hold("A@X", 2); !errors.Is(err, domain.ErrDuplicateHold) {
		t.Fatalf("err = %v, want ErrDuplicateHold", err)
	}
	e.wantRemaining(capacity - 1)
	if held, _ := e.sales(); held != 1 {
		t.Errorf("held = %d, want 1", held)
	}
	if _, err := e.hold("b@x", 1); err != nil {
		t.Errorf("another email: %v", err)
	}
}

func TestHoldUnknownEvent(t *testing.T) {
	e := newEnv(t, inventory.NewDB())
	_, err := e.svc().Hold(t.Context(), app.HoldInput{EventID: "nope", Email: "a@x", Lang: "ja", Quantity: 1})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The DB inventory passes every Reserve, so the event_sales condition must stop the overbooking.
func TestHoldSoldOutInDB(t *testing.T) {
	e := newEnv(t, inventory.NewDB())
	if _, err := e.hold("a@x", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hold("b@x", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hold("c@x", 3); !errors.Is(err, domain.ErrSoldOut) {
		t.Fatalf("err = %v, want ErrSoldOut", err)
	}
	if n := e.count("SELECT COUNT(*) FROM purchases WHERE email = 'c@x'"); n != 0 {
		t.Errorf("sold-out hold left %d purchase rows", n)
	}
}

func TestHoldConcurrentNeverOverbooks(t *testing.T) {
	for name, inv := range map[string]func() port.Inventory{"memory": inventory.NewMemory, "db": inventory.NewDB} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, inv())
			var ok, soldOut atomic.Int32
			var wg sync.WaitGroup
			for i := range 100 {
				wg.Go(func() {
					_, err := e.svc().Hold(context.Background(), app.HoldInput{
						EventID: "e1", Email: string(rune('a'+i%26)) + string(rune('a'+i/26)) + "@x", Lang: "ja", Quantity: 1,
					})
					switch {
					case err == nil:
						ok.Add(1)
					case errors.Is(err, domain.ErrSoldOut):
						soldOut.Add(1)
					default:
						t.Errorf("unexpected error: %v", err)
					}
				})
			}
			wg.Wait()
			if ok.Load() != capacity || soldOut.Load() != 100-capacity {
				t.Errorf("ok = %d, sold out = %d", ok.Load(), soldOut.Load())
			}
			if n := e.count("SELECT COALESCE(SUM(quantity), 0) FROM purchases WHERE event_id = 'e1' AND state = 'held'"); n != capacity {
				t.Errorf("sum of held quantity = %d, want %d", n, capacity)
			}
			if held, remaining := e.sales(); held != capacity || remaining != 0 {
				t.Errorf("event_sales held = %d, remaining = %d", held, remaining)
			}
		})
	}
}

func TestHoldReleaseErrorAfterWriteError(t *testing.T) {
	e := newEnv(t, releaseFails{inventory.NewMemory()})
	e.clock = eventAt
	_, err := e.hold("a@x", 1)
	if !errors.Is(err, domain.ErrNotOnSale) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want ErrNotOnSale and the release error", err)
	}
	e.wantRemaining(capacity)
}

func TestHoldStartFailureReleases(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.NewID = func() string { return "p1" }
	e.pay.FailStart(errBoom)
	_, err := e.hold("a@x", 3)
	if !errors.Is(err, domain.ErrPaymentUnavailable) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want ErrPaymentUnavailable wrapping the start error", err)
	}
	if p := e.purchase("p1"); p.State != "released" || p.PaymentID != nil {
		t.Errorf("purchase = %+v, want released without payment_id", p)
	}
	if held, remaining := e.sales(); held != 0 || remaining != capacity {
		t.Errorf("event_sales held = %d, remaining = %d", held, remaining)
	}
	e.wantRemaining(capacity)
	e.deps.NewID = func() string { return "p2" }
	if _, err := e.hold("a@x", 1); err != nil {
		t.Errorf("hold after release: %v", err)
	}
}

func TestHoldStartFailureReleaseError(t *testing.T) {
	e := newEnv(t, releaseFails{inventory.NewMemory()})
	e.deps.NewID = func() string { return "p1" }
	e.pay.FailStart(errors.New("down"))
	_, err := e.hold("a@x", 1)
	if !errors.Is(err, domain.ErrPaymentUnavailable) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want ErrPaymentUnavailable and the release error", err)
	}
	if p := e.purchase("p1"); p.State != "released" {
		t.Errorf("state = %s, want released", p.State)
	}
	e.wantRemaining(capacity)
}

// A purchase that left held before the release item runs keeps its seats where
// that other transition put them, so neither event_sales nor the inventory moves.
func TestHoldStartFailureAfterStateChange(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.NewID = func() string { return "p1" }
	e.deps.Payment = stubPayment{start: func(ctx context.Context, id string, _ int) (string, string, error) {
		if _, err := e.s.Write.ExecContext(ctx, "UPDATE purchases SET state = 'expired' WHERE id = ?", id); err != nil {
			t.Error(err)
		}
		return "", "", errBoom
	}}
	_, err := e.hold("a@x", 2)
	if !errors.Is(err, domain.ErrPaymentUnavailable) || !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	if held, remaining := e.sales(); held != 2 || remaining != capacity-2 {
		t.Errorf("event_sales held = %d, remaining = %d", held, remaining)
	}
	e.wantRemaining(capacity - 2)
}

func TestHoldRecordsPaymentAndCheckoutURL(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.NewID = func() string { return "p1" }
	var gotAmount int
	e.deps.Payment = stubPayment{start: func(_ context.Context, id string, amount int) (string, string, error) {
		gotAmount = amount
		return "pay_1", "https://pay.example/c/" + id, nil
	}}
	res, err := e.hold("a@x", 3)
	if err != nil {
		t.Fatal(err)
	}
	if gotAmount != 3*price {
		t.Errorf("Start amount = %d, want %d", gotAmount, 3*price)
	}
	if res.CheckoutURL != "https://pay.example/c/p1" {
		t.Errorf("CheckoutURL = %q", res.CheckoutURL)
	}
	p := e.purchase("p1")
	if p.PaymentID == nil || *p.PaymentID != "pay_1" || p.CheckoutURL == nil || *p.CheckoutURL != res.CheckoutURL {
		t.Errorf("purchase = %+v", p)
	}
}

// The payment_id may already be written by a completion notice that beat step 3.
func TestHoldRecordPaymentConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		reports      int
	}{{"same id", "pay_1", 0}, {"different id", "pay_other", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, inventory.NewMemory())
			e.deps.NewID = func() string { return "p1" }
			e.deps.Payment = stubPayment{start: func(ctx context.Context, id string, _ int) (string, string, error) {
				if _, err := e.s.Write.ExecContext(ctx, "UPDATE purchases SET payment_id = ? WHERE id = ?", tc.stored, id); err != nil {
					t.Error(err)
				}
				return "pay_1", "", nil
			}}
			res, err := e.hold("a@x", 1)
			if err != nil || res.PurchaseID != "p1" {
				t.Fatalf("Hold = %+v, %v; want success", res, err)
			}
			if p := e.purchase("p1"); p.State != "held" || *p.PaymentID != tc.stored {
				t.Errorf("purchase = %+v", p)
			}
			if got := e.errs.count(); got != tc.reports {
				t.Errorf("reports = %d, want %d", got, tc.reports)
			}
		})
	}
}
