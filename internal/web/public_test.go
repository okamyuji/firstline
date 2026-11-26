package web_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/i18n"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/observe"
	"github.com/okamyuji/firstline/internal/observe/logreporter"
	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	fsqlite "github.com/okamyuji/firstline/internal/store/sqlite"
	"github.com/okamyuji/firstline/internal/store/writer"
	"github.com/okamyuji/firstline/internal/web"
	"github.com/uptrace/bun"
)

const (
	csp        = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	htmxConfig = `<meta name="htmx-config" content='{"includeIndicatorStyles": false, "allowEval": false, "selfRequestsOnly": true, "historyRestoreAsHxRequest": false}'>`
	titleJA    = "春の演奏会"
	titleEN    = "Spring Concert"
	price      = 5000
)

var (
	now     = time.UnixMilli(1_800_000_000_000).UTC()
	eventAt = now.Add(24 * time.Hour)
	endsAt  = eventAt.Add(2 * time.Hour)
	tr      = i18n.New()
	// Matches an untranslated key such as "status.held" left in a page.
	rawKey = regexp.MustCompile(`\b(site|nav|action|label|price|list|detail|status|lookup|error)\.[a-z_]+\b`)
)

type env struct {
	t     *testing.T
	s     *store.Store
	w     *writer.Writer
	inv   port.Inventory
	pay   *fake.Fake
	tok   auth.Tokens
	clock func() time.Time
	at    time.Time
	logs  *bytes.Buffer
	svc   *app.Service
	h     http.Handler
}

func newEnv(t *testing.T, rate int) *env {
	t.Helper()
	s, err := fsqlite.Open(t.Context(), t.TempDir()+"/f.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
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
	// The payment never completes, so a purchase stays held until a test changes it.
	pay := fake.New(time.Now, func(time.Duration) <-chan time.Time { return nil }, time.Second, fake.Complete)
	t.Cleanup(pay.Stop)
	e := &env{t: t, s: s, w: w, inv: inventory.NewMemory(), pay: pay, tok: auth.Tokens{Key: []byte("not-a-real-key-not-a-real-key-00")},
		at: now, logs: &bytes.Buffer{}}
	e.clock = func() time.Time { return e.at }
	svc := app.New(app.Deps{Store: s, Writer: w, Inventory: e.inv, Payment: pay, Errors: nopReporter{}, Clock: func() time.Time { return e.clock() }})
	e.svc = svc
	e.h = web.NewHandler(web.Deps{
		App: svc, Store: s, Writer: w, I18n: tr, Tokens: e.tok, Clock: func() time.Time { return e.clock() },
		HoldRatePerMin: rate, Errors: logreporter.New(slog.New(observe.RequestIDHandler{Handler: slog.NewTextHandler(e.logs, nil)})),
	})
	e.event("e1", now.Add(-time.Hour), eventAt, 10)
	return e
}

type nopReporter struct{}

func (nopReporter) Report(context.Context, error, ...slog.Attr) {}

func (e *env) event(id string, saleStart, startsAt time.Time, capacity int) {
	e.t.Helper()
	ctx := e.t.Context()
	if _, err := e.s.Exec(ctx, e.s.Write, "insert_event", id, "o", titleJA, titleEN,
		startsAt.UnixMilli(), endsAt.UnixMilli(), capacity, price, saleStart.UnixMilli(), 1); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.s.Exec(ctx, e.s.Write, "insert_event_sales", id, capacity); err != nil {
		e.t.Fatal(err)
	}
	if err := e.inv.Add(ctx, id, capacity); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.s.Write.ExecContext(e.t.Context(), q, args...); err != nil {
		e.t.Fatal(err)
	}
}

type req struct {
	method, target string
	form           url.Values
	body           string
	header         map[string]string
	remote         string
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	body := r.body
	if r.form != nil {
		body = r.form.Encode()
	}
	hr := httptest.NewRequest(r.method, r.target, strings.NewReader(body))
	if r.method == http.MethodPost {
		hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	return rec
}

func (e *env) get(target string, header ...string) *httptest.ResponseRecorder {
	h := map[string]string{}
	for i := 0; i+1 < len(header); i += 2 {
		h[header[i]] = header[i+1]
	}
	return e.do(req{method: http.MethodGet, target: target, header: h})
}

func (e *env) buy(email, qty string, header map[string]string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, target: "/events/e1/purchases",
		form: url.Values{"email": {email}, "quantity": {qty}}, header: header})
}

// held makes a purchase through the page and returns its id and token.
func (e *env) held(email string) (string, string) {
	e.t.Helper()
	rec := e.buy(email, "2", nil)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("purchase: status %d, body %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimPrefix(loc.Path, "/purchases/"), loc.Query().Get("token")
}

func statusPath(id, tok string) string { return "/purchases/" + id + "?token=" + url.QueryEscape(tok) }

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, code, rec.Body)
	}
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(rec.Body.String(), s) {
			t.Errorf("body lacks %q; body:\n%s", s, rec.Body)
		}
	}
}

func lackBody(t *testing.T, rec *httptest.ResponseRecorder, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("body has %q; body:\n%s", s, rec.Body)
		}
	}
}

// flush waits until every async item queued so far has committed: the queue is FIFO.
func (e *env) flush() {
	e.t.Helper()
	if err := e.w.Do(e.t.Context(), func(context.Context, bun.Tx) error { return nil }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) row(q string, dest ...any) {
	e.t.Helper()
	if err := e.s.Read.QueryRowContext(e.t.Context(), q).Scan(dest...); err != nil {
		e.t.Fatal(err)
	}
}

func noStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestEveryResponseHasSecurityAndLanguageHeaders(t *testing.T) {
	e := newEnv(t, 1)
	e.buy("a@example.com", "0", nil) // uses up the limit of 1 so the next purchase gets 429
	cases := []struct {
		name string
		r    req
		code int
	}{
		{"list", req{method: "GET", target: "/"}, 200},
		{"detail", req{method: "GET", target: "/events/e1"}, 200},
		{"lookup", req{method: "GET", target: "/lookup"}, 200},
		{"static", req{method: "GET", target: "/static/app.css"}, 200},
		{"unknown path", req{method: "GET", target: "/nope"}, 404},
		{"unknown event", req{method: "GET", target: "/events/nope"}, 404},
		{"cross origin", req{method: "POST", target: "/events/e1/purchases", header: map[string]string{"Origin": "https://evil.example"}}, 403},
		{"rate limited", req{method: "POST", target: "/events/e1/purchases"}, 429},
		{"NUL in the path", req{method: "GET", target: "/events/%00"}, 400},
		{"wrong method", req{method: "DELETE", target: "/lookup"}, 405},
		{"status with a token", req{method: "GET", target: "/purchases/x?token=t"}, 404},
	}
	for _, lang := range []string{"ja", "en"} {
		for _, c := range cases {
			t.Run(lang+"/"+c.name, func(t *testing.T) {
				r := c.r
				r.header = map[string]string{}
				for k, v := range c.r.header {
					r.header[k] = v
				}
				if lang == "en" {
					r.header["Accept-Language"] = "en-US,en;q=0.9"
				}
				rec := e.do(r)
				wantCode(t, rec, c.code)
				h := rec.Header()
				if got := h.Get("Content-Security-Policy"); got != csp {
					t.Errorf("CSP = %q", got)
				}
				if got := h.Get("Vary"); got != "Accept-Language, HX-Request" {
					t.Errorf("Vary = %q", got)
				}
				if got := h.Get("Content-Language"); got != lang {
					t.Errorf("Content-Language = %q, want %q", got, lang)
				}
				if got := h.Get("Strict-Transport-Security"); got != "max-age=63072000" {
					t.Errorf("Strict-Transport-Security = %q", got)
				}
				if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("X-Content-Type-Options = %q", got)
				}
				// Status and lookup URLs carry the token in the query.
				if got := h.Get("Referrer-Policy"); got != "no-referrer" {
					t.Errorf("Referrer-Policy = %q", got)
				}
			})
		}
	}
}

func TestCrossOriginPurchaseIsForbidden(t *testing.T) {
	e := newEnv(t, 10)
	rec := e.buy("a@example.com", "1", map[string]string{"Origin": "https://evil.example"})
	wantCode(t, rec, http.StatusForbidden)
	var n int
	e.row("SELECT COUNT(*) FROM purchases", &n)
	if n != 0 {
		t.Errorf("purchases = %d after a cross-origin POST, want 0", n)
	}
	wantCode(t, e.buy("a@example.com", "1", map[string]string{"Origin": "http://example.com"}), http.StatusSeeOther)
}

func TestPagesFollowTheNegotiatedLanguage(t *testing.T) {
	e := newEnv(t, 10)
	for _, target := range []string{"/", "/events/e1"} {
		en := e.get(target, "Accept-Language", "en")
		wantCode(t, en, 200)
		wantBody(t, en, titleEN, `<html lang="en">`, "JPY 5,000")
		lackBody(t, en, titleJA)
		ja := e.get(target)
		wantCode(t, ja, 200)
		wantBody(t, ja, titleJA, `<html lang="ja">`, "5,000円")
		lackBody(t, ja, titleEN)
	}
}

func TestEveryPageHasTheHtmxConfigMeta(t *testing.T) {
	e := newEnv(t, 10)
	id, tok := e.held("a@example.com")
	for _, target := range []string{"/", "/events/e1", "/lookup", statusPath(id, tok), "/events/nope", "/nope"} {
		rec := e.get(target)
		wantBody(t, rec, htmxConfig, `<script src="/static/htmx.min.js"></script>`, `<link rel="stylesheet" href="/static/app.css">`)
		if m := rawKey.FindString(rec.Body.String()); m != "" {
			t.Errorf("%s shows the raw key %q", target, m)
		}
		lackBody(t, rec, "style=", "<style", "<script>")
	}
}

func TestStaticHtmxMatchesItsRecordedHash(t *testing.T) {
	e := newEnv(t, 10)
	rec := e.get("/static/htmx.min.js")
	wantCode(t, rec, 200)
	want, err := os.ReadFile("static/htmx.min.js.sha256")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	if got := hex.EncodeToString(sum[:]); got != strings.TrimSpace(string(want)) {
		t.Errorf("embedded htmx.min.js sha256 = %s, want %s", got, want)
	}
	wantBody(t, rec, `version:"2.0.8"`)
}

func TestListShowsSaleStateAtTheBoundaries(t *testing.T) {
	e := newEnv(t, 10)
	empty := newEnv(t, 10)
	empty.exec("DELETE FROM event_sales")
	empty.exec("DELETE FROM events")
	wantBody(t, empty.get("/"), tr.T("ja", i18n.ListEmpty))

	e.exec("UPDATE events SET title_ja = 'ON' WHERE id = 'e1'")
	e.event("now", now, eventAt, 5)                                        // sale starts exactly now: on sale
	e.event("later", now.Add(time.Millisecond), eventAt.Add(time.Hour), 5) // sale starts 1ms later: not yet
	e.event("gone", now.Add(-2*time.Hour), now, 5)                         // already started: not listed
	e.exec("UPDATE events SET title_ja = id WHERE id IN ('now', 'later', 'gone')")
	e.exec("UPDATE event_sales SET remaining = 0 WHERE event_id = 'now'")
	rec := e.get("/")
	wantCode(t, rec, 200)
	body := rec.Body.String()
	lackBody(t, rec, ">gone<", tr.T("ja", i18n.ListEmpty))
	notYet, soldOut := tr.T("ja", i18n.ListNotOnSale), tr.T("ja", i18n.ListSoldOut)
	row := func(title string) string {
		i := strings.Index(body, ">"+title+"<")
		if i < 0 {
			t.Fatalf("no row for %s:\n%s", title, body)
		}
		i = strings.LastIndex(body[:i], "<li")
		end := strings.Index(body[i:], "</li>")
		return body[i : i+end]
	}
	if r := row("ON"); strings.Contains(r, notYet) || strings.Contains(r, soldOut) || !strings.Contains(r, `href="/events/e1"`) {
		t.Errorf("on-sale row = %q", r)
	}
	if r := row("now"); !strings.Contains(r, soldOut) || strings.Contains(r, notYet) {
		t.Errorf("sold-out row = %q", r)
	}
	if r := row("later"); !strings.Contains(r, notYet) {
		t.Errorf("not-yet row = %q", r)
	}
	if strings.Index(body, ">now<") > strings.Index(body, ">later<") {
		t.Errorf("rows are not in start order")
	}
}

func TestDetailOffersTheFormOnlyWhileOnSale(t *testing.T) {
	e := newEnv(t, 10)
	e.event("starts", now.Add(-time.Hour), now, 5) // starts exactly now: sale over
	e.event("later", now.Add(time.Millisecond), eventAt, 5)
	e.event("soldout", now, eventAt, 5)
	e.exec("UPDATE event_sales SET remaining = 0 WHERE event_id = 'soldout'")
	e.event("one", now, eventAt, 5)
	e.exec("UPDATE event_sales SET remaining = 1 WHERE event_id = 'one'")
	form := `action="/events/%s/purchases"`
	cases := []struct {
		id, label string
		form      bool
	}{
		{"e1", "", true},
		{"one", "", true},
		{"starts", tr.T("ja", i18n.ListNotOnSale), false},
		{"later", tr.T("ja", i18n.ListNotOnSale), false},
		{"soldout", tr.T("ja", i18n.ListSoldOut), false},
	}
	for _, c := range cases {
		rec := e.get("/events/" + c.id)
		wantCode(t, rec, 200)
		has := strings.Contains(rec.Body.String(), strings.Replace(form, "%s", c.id, 1))
		if has != c.form {
			t.Errorf("%s: form shown = %v, want %v", c.id, has, c.form)
		}
		if c.label != "" {
			wantBody(t, rec, c.label)
		}
	}
	wantBody(t, e.get("/events/one"), `<dd>1</dd>`)
}

func TestPurchaseRedirectsToStatusWithAVerifiableToken(t *testing.T) {
	e := newEnv(t, 10)
	rec := e.buy(" Buyer@Example.com ", "2", map[string]string{"Accept-Language": "en"})
	wantCode(t, rec, http.StatusSeeOther)
	noStore(t, rec)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := strings.CutPrefix(loc.Path, "/purchases/")
	if !ok || id == "" {
		t.Fatalf("Location = %s", loc)
	}
	tok := loc.Query().Get("token")
	if got, err := e.tok.Verify(tok, now); err != nil || got != id {
		t.Fatalf("Verify = %q, %v; want %q", got, err, id)
	}
	checkPurchase(t, e, id, tok)
}

func checkPurchase(t *testing.T, e *env, id, tok string) {
	exp := endsAt.Add(app.TokenLifetime)
	if _, err := e.tok.Verify(tok, exp); err != nil {
		t.Errorf("token rejected at its expiry: %v", err)
	}
	if _, err := e.tok.Verify(tok, exp.Add(time.Millisecond)); !errors.Is(err, auth.ErrExpiredToken) {
		t.Errorf("token after expiry: %v", err)
	}
	var email, lang string
	var qty int
	e.row("SELECT email, lang, quantity FROM purchases WHERE id = '"+id+"'", &email, &lang, &qty)
	if email != "buyer@example.com" || lang != "en" || qty != 2 {
		t.Errorf("purchase = %s %s %d", email, lang, qty)
	}
}

func TestPurchaseErrorsRenderTheDetailPage(t *testing.T) {
	long := strings.Repeat("a", 254-len("@example.com")) + "@example.com"
	cases := []struct {
		name  string
		form  url.Values
		body  string
		setup func(e *env)
		code  int
		key   string
	}{
		{"sold out", url.Values{"email": {"a@example.com"}, "quantity": {"4"}},
			"", func(e *env) { _ = e.inv.Reserve(context.Background(), "e1", 8) }, 409, i18n.ErrSoldOut},
		{"quantity too large", url.Values{"email": {"a@example.com"}, "quantity": {"5"}}, "", nil, 400, i18n.ErrBadQuantity},
		{"quantity zero", url.Values{"email": {"a@example.com"}, "quantity": {"0"}}, "", nil, 400, i18n.ErrBadQuantity},
		{"quantity not a number", url.Values{"email": {"a@example.com"}, "quantity": {"two"}}, "", nil, 400, i18n.ErrBadQuantity},
		{"no email", url.Values{"quantity": {"1"}}, "", nil, 400, i18n.ErrBadInput},
		{"malformed email", url.Values{"email": {"not an address"}, "quantity": {"1"}}, "", nil, 400, i18n.ErrBadInput},
		{"display name", url.Values{"email": {"A <a@example.com>"}, "quantity": {"1"}}, "", nil, 400, i18n.ErrBadInput},
		{"email over 254 bytes", url.Values{"email": {"a" + long}, "quantity": {"1"}}, "", nil, 400, i18n.ErrBadInput},
		{"invalid UTF-8", nil, "email=a%40example.com&quantity=1&note=%FF", nil, 400, i18n.ErrBadInput},
		{"invalid UTF-8 key", nil, "email=a%40example.com&quantity=1&%FF=x", nil, 400, i18n.ErrBadInput},
		{"body over 16 KiB", nil, "email=a%40example.com&quantity=1&pad=" + strings.Repeat("x", 16<<10), nil, 400, i18n.ErrBadInput},
		{"duplicate hold", url.Values{"email": {"a@example.com"}, "quantity": {"1"}},
			"", func(e *env) { e.held("a@example.com") }, 409, i18n.ErrDuplicateHold},
		{"not on sale", url.Values{"email": {"a@example.com"}, "quantity": {"1"}},
			"", func(e *env) { e.exec("UPDATE events SET sale_starts_at = ?", now.Add(time.Minute).UnixMilli()) }, 409, i18n.ErrNotOnSale},
		{"payment unavailable", url.Values{"email": {"a@example.com"}, "quantity": {"1"}},
			"", func(e *env) { e.pay.FailStart(errors.New("down")) }, 503, i18n.ErrPaymentUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, 0)
			if c.setup != nil {
				c.setup(e)
			}
			rec := e.do(req{method: "POST", target: "/events/e1/purchases", form: c.form, body: c.body})
			wantCode(t, rec, c.code)
			wantBody(t, rec, tr.T("ja", c.key), titleJA, htmxConfig)
		})
	}
	t.Run("email of exactly 254 bytes", func(t *testing.T) {
		e := newEnv(t, 0)
		wantCode(t, e.buy(long, "1", nil), http.StatusSeeOther)
	})
	t.Run("unknown event", func(t *testing.T) {
		e := newEnv(t, 0)
		rec := e.do(req{method: "POST", target: "/events/nope/purchases", form: url.Values{"email": {"a@example.com"}, "quantity": {"1"}}})
		wantCode(t, rec, 404)
		wantBody(t, rec, tr.T("ja", i18n.ErrNotFound))
	})
	t.Run("English error text", func(t *testing.T) {
		e := newEnv(t, 0)
		wantBody(t, e.buy("a@example.com", "9", map[string]string{"Accept-Language": "en"}), tr.T("en", i18n.ErrBadQuantity), titleEN)
	})
}

func TestUnknownErrorsGiveTheGenericTextAndAreLogged(t *testing.T) {
	e := newEnv(t, 0)
	if err := e.s.Read.Close(); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/events/e1")
	wantCode(t, rec, 500)
	wantBody(t, rec, tr.T("ja", i18n.ErrInternal), htmxConfig)
	if !strings.Contains(e.logs.String(), "level=ERROR") || !strings.Contains(e.logs.String(), "path=/events/e1") {
		t.Errorf("log = %q", e.logs)
	}
	e.logs.Reset()
	wantCode(t, e.get("/events/nope"), 500)
	e2 := newEnv(t, 0)
	wantCode(t, e2.get("/events/nope"), 404)
	if e2.logs.Len() != 0 {
		t.Errorf("a not-found error was logged: %s", e2.logs)
	}
}

func TestPurchaseStatusPageAndPartial(t *testing.T) {
	e := newEnv(t, 10)
	id, tok := e.held("a@example.com")
	e.exec("UPDATE purchases SET checkout_url = 'https://pay.example/c/1' WHERE id = ?", id)
	path := statusPath(id, tok)

	full := e.get(path, "Accept-Language", "en")
	wantCode(t, full, 200)
	wantBody(t, full, "<html", tr.T("en", i18n.StatusHeld), tr.T("en", i18n.StatusDeadlineNote),
		`href="https://pay.example/c/1"`, // html/template writes "+" as "&#43;".
		strings.ReplaceAll(app.JSTTime("en", now.Add(10*time.Minute).UnixMilli()), "+", "&#43;"),
		`hx-trigger="every 5s"`, `hx-get="`+path+`"`)
	noStore(t, full)

	part := e.get(path, "HX-Request", "true")
	wantCode(t, part, 200)
	wantBody(t, part, tr.T("ja", i18n.StatusHeld), `id="status"`, `hx-trigger="every 5s"`)
	lackBody(t, part, "<html", "<head")
	noStore(t, part)

	states := []struct {
		state, key string
	}{
		{"purchased", i18n.StatusPurchased},
		{"released", i18n.StatusFailed},
		{"expired", i18n.StatusExpired},
		{"refunding", i18n.StatusRefunding},
		{"refunded", i18n.StatusRefunded},
	}
	for _, s := range states {
		e.exec("UPDATE purchases SET state = ?, ticket_number_from = 7 WHERE id = ?", s.state, id)
		part := e.get(path, "HX-Request", "true")
		wantCode(t, part, 286)
		wantBody(t, part, tr.T("ja", s.key), "<dd>7-8</dd>", `id="status"`)
		lackBody(t, part, "hx-trigger", "pay.example", tr.T("ja", i18n.StatusDeadlineNote))
		noStore(t, part)
		wantCode(t, e.get(path), 200)
	}
	e.exec("UPDATE purchases SET state = 'held', ticket_number_from = NULL WHERE id = ?", id)
	lackBody(t, e.get(path), tr.T("ja", i18n.StatusSeatNumber))
	// htmx sends HX-Request: true; any other value is a full page request.
	wantBody(t, e.get(path, "HX-Request", "false"), "<html")
}

func TestPurchaseStatusRejectsAWrongToken(t *testing.T) {
	e := newEnv(t, 10)
	id, tok := e.held("a@example.com")
	other, otherTok := e.held("b@example.com")
	ghost := e.tok.Issue("ghost", endsAt)
	cases := map[string]string{
		"no token":             "/purchases/" + id,
		"tampered token":       statusPath(id, tok+"x"),
		"token for another id": statusPath(id, otherTok),
		"token for a ghost":    statusPath("ghost", ghost),
		"path for another id":  statusPath(other, tok),
		"token of another key": statusPath(id, auth.Tokens{Key: []byte("another-key-another-key-another!")}.Issue(id, endsAt)),
	}
	for name, path := range cases {
		rec := e.get(path)
		if rec.Code != 404 {
			t.Errorf("%s: status %d, want 404", name, rec.Code)
		}
		wantBody(t, rec, tr.T("ja", i18n.ErrNotFound))
		noStore(t, rec)
	}
	e.at = endsAt.Add(app.TokenLifetime)
	wantCode(t, e.get(statusPath(id, tok)), 200)
	e.at = e.at.Add(time.Millisecond)
	wantCode(t, e.get(statusPath(id, tok)), 404)
}

func TestLookup(t *testing.T) {
	e := newEnv(t, 10)
	id, tok := e.held("a@example.com")
	_, otherTok := e.held("b@example.com")

	form := e.get("/lookup")
	wantCode(t, form, 200)
	wantBody(t, form, `action="/lookup"`, tr.T("ja", i18n.LookupToken))

	ok := e.do(req{method: "POST", target: "/lookup", form: url.Values{"email": {" A@Example.COM "}, "token": {" " + tok + " "}}})
	wantCode(t, ok, http.StatusSeeOther)
	noStore(t, ok)
	if got, want := ok.Header().Get("Location"), statusPath(id, tok); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	mismatch := func(email, token string) string {
		t.Helper()
		rec := e.do(req{method: "POST", target: "/lookup", form: url.Values{"email": {email}, "token": {token}}})
		wantCode(t, rec, 200)
		wantBody(t, rec, tr.T("ja", i18n.LookupMismatch), `action="/lookup"`)
		noStore(t, rec)
		return rec.Body.String()
	}
	wrongEmail := mismatch("c@example.com", tok)
	wrongToken := mismatch("a@example.com", tok+"x")
	if wrongEmail != wrongToken {
		t.Errorf("a wrong email and a wrong token give different pages:\n%s\n---\n%s", wrongEmail, wrongToken)
	}
	for _, body := range []string{
		mismatch("a@example.com", otherTok),
		mismatch("a@example.com", e.tok.Issue("ghost", endsAt)),
		mismatch("a@example.com", ""),
	} {
		if body != wrongEmail {
			t.Errorf("mismatch pages differ:\n%s", body)
		}
	}

	for name, f := range map[string]url.Values{
		"long email":   {"email": {strings.Repeat("a", 255)}, "token": {tok}},
		"long token":   {"email": {"a@example.com"}, "token": {strings.Repeat("a", 513)}},
		"invalid utf8": {"email": {"a@example.com\xff"}, "token": {tok}},
	} {
		rec := e.do(req{method: "POST", target: "/lookup", form: f})
		if rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
		wantBody(t, rec, tr.T("ja", i18n.ErrBadInput))
	}
	// Both fields at their byte limits are accepted and answered with the mismatch page.
	edge := e.do(req{method: "POST", target: "/lookup", form: url.Values{"email": {strings.Repeat("a", 254)}, "token": {strings.Repeat("a", 512)}}})
	wantCode(t, edge, 200)
	wantBody(t, edge, tr.T("ja", i18n.LookupMismatch))
}

func TestHoldRateLimitPerIP(t *testing.T) {
	e := newEnv(t, 10)
	attempt := func(ip string) int {
		return e.do(req{method: "POST", target: "/events/e1/purchases", remote: ip + ":1234",
			form: url.Values{"email": {"a@example.com"}, "quantity": {"0"}}}).Code
	}
	start := e.at
	for i := range 10 {
		if got := attempt("192.0.2.1"); got != 400 {
			t.Fatalf("attempt %d: status %d, want 400", i+1, got)
		}
		e.at = e.at.Add(time.Second)
	}
	rec := e.do(req{method: "POST", target: "/events/e1/purchases", remote: "192.0.2.1:9",
		form: url.Values{"email": {"a@example.com"}, "quantity": {"1"}}})
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, tr.T("ja", i18n.ErrTooManyHolds), titleJA, htmxConfig)
	if got := attempt("192.0.2.2"); got != 400 {
		t.Errorf("another IP: status %d, want 400", got)
	}
	e.at = start.Add(time.Minute - time.Millisecond)
	if got := attempt("192.0.2.1"); got != 429 {
		t.Errorf("just inside the window: status %d, want 429", got)
	}
	e.at = start.Add(time.Minute)
	if got := attempt("192.0.2.1"); got != 400 {
		t.Errorf("first attempt left the window: status %d, want 400", got)
	}
	if got := attempt("192.0.2.1"); got != 429 {
		t.Errorf("second attempt still inside: status %d, want 429", got)
	}
}

func TestHoldRateLimitEdgeCases(t *testing.T) {
	e := newEnv(t, 10)
	// A RemoteAddr without a port still counts by itself.
	for range 10 {
		e.do(req{method: "POST", target: "/events/e1/purchases", remote: "bare", form: url.Values{"quantity": {"0"}}})
	}
	if got := e.do(req{method: "POST", target: "/events/e1/purchases", remote: "bare", form: url.Values{"quantity": {"0"}}}).Code; got != 429 {
		t.Errorf("bare RemoteAddr: status %d, want 429", got)
	}

	off := newEnv(t, 0)
	for i := range 30 {
		rec := off.do(req{method: "POST", target: "/events/e1/purchases", form: url.Values{"email": {"a@example.com"}, "quantity": {"0"}}})
		if rec.Code != 400 {
			t.Fatalf("limit 0, attempt %d: status %d", i+1, rec.Code)
		}
	}
}

func TestAccessLogRecordsThePathWithoutTheQuery(t *testing.T) {
	e := newEnv(t, 10)
	id, tok := e.held("a@example.com")
	e.flush()
	e.exec("DELETE FROM access_logs")
	// Each clock read moves 3ms, so the duration is the gap between the first and last reads.
	tick := e.at
	e.clock = func() time.Time { tick = tick.Add(3 * time.Millisecond); return tick }
	first := e.at.Add(3 * time.Millisecond)
	wantCode(t, e.get(statusPath(id, tok)), 200)
	e.flush()
	var method, path string
	var status int
	var dur, at int64
	e.row("SELECT method, path, status, duration_ms, at FROM access_logs", &method, &path, &status, &dur, &at)
	if method != "GET" || path != "/purchases/"+id || status != 200 {
		t.Errorf("row = %s %s %d", method, path, status)
	}
	if want := tick.Sub(first).Milliseconds(); dur != want || dur <= 0 {
		t.Errorf("duration_ms = %d, want %d", dur, want)
	}
	if at != first.UnixMilli() {
		t.Errorf("at = %d, want %d", at, first.UnixMilli())
	}
}

func TestAccessLogRecordsTheErrorStatus(t *testing.T) {
	e := newEnv(t, 10)
	wantCode(t, e.get("/nope?token=secret"), 404)
	e.flush()
	var path string
	var status int
	e.row("SELECT path, status FROM access_logs", &path, &status)
	if path != "/nope" || status != 404 {
		t.Errorf("row = %s %d", path, status)
	}
}
