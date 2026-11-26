package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
)

// The values differ from each other and from the capacity, so a template that
// shows the wrong field fails.
func TestListAndDetailShowRemainingAndStartTime(t *testing.T) {
	e := newEnv(t, 0)
	e.exec("UPDATE event_sales SET remaining = 3, held = 5, sold = 2 WHERE event_id = 'e1'")
	rec := e.get("/")
	wantBody(t, rec, "残数 3", app.JSTTime("ja", eventAt.UnixMilli()))
	lackBody(t, rec, "残数 10")
	rec = e.get("/events/e1")
	wantBody(t, rec, "<dd>3</dd>", app.JSTTime("ja", eventAt.UnixMilli()))
	lackBody(t, rec, app.JSTTime("ja", endsAt.UnixMilli()))
}

func TestListShowsAnEventStartingOneMillisecondFromNow(t *testing.T) {
	e := newEnv(t, 0)
	e.event("e2", now.Add(-time.Hour), now.Add(time.Millisecond), 5)
	wantBody(t, e.get("/"), `href="/events/e2"`)
}

func TestFormsCarryTheInputConstraints(t *testing.T) {
	e := newEnv(t, 0)
	wantBody(t, e.get("/events/e1"), `type="email" name="email" required maxlength="254" autocomplete="email"`,
		`name="quantity" required min="1" max="4"`)
	wantBody(t, e.get("/admin/login"), `name="email" required maxlength="254" autocomplete="username"`,
		`type="password" name="password" required maxlength="256" autocomplete="current-password"`)
	wantBody(t, e.get("/lookup"), `name="token" required maxlength="512" autocomplete="off"`)
}

func TestNavigationAndStatusFragmentMarkup(t *testing.T) {
	e := newEnv(t, 0)
	wantBody(t, e.get("/"), `<nav><a href="/">`, `<a href="/lookup">`)
	wantBody(t, e.get("/nope"), `<p><a href="/">`)
	id, tok := e.held("a@b.com")
	rec := e.get(statusPath(id, tok), "HX-Request", "true")
	wantBody(t, rec, `hx-swap="outerHTML"`, `hx-trigger="every 5s"`, "<dd>2</dd>")
	// A real server sniffs a body without Content-Type; the recorder does not.
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("fragment Content-Type = %q", ct)
	}
}

func TestStylesheetHidesTheIndicatorUntilARequest(t *testing.T) {
	e := newEnv(t, 0)
	wantBody(t, e.get("/static/app.css"), ".htmx-indicator { opacity: 0; }", ".htmx-request .htmx-indicator")
}

func TestLoginThrottleCountsPerIP(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	post := func(ip, email, pw string) *httptest.ResponseRecorder {
		return e.do(req{method: http.MethodPost, target: "/admin/login", remote: ip + ":1234",
			form: url.Values{"email": {email}, "password": {pw}}})
	}
	for i := range 20 {
		post("10.0.0.1", "nobody"+string(rune('a'+i))+"@example.com", "wrong password!!")
	}
	wantCode(t, post("10.0.0.1", orgEmail, orgPassword), http.StatusTooManyRequests)
	wantCode(t, post("10.0.0.2", orgEmail, orgPassword), http.StatusSeeOther)
}

func TestAdminSalesAndEventsShowEachField(t *testing.T) {
	e := newEnv(t, 0)
	ck := e.session()
	rec := e.as(ck, http.MethodPost, "/admin/events", eventForm())
	wantCode(t, rec, http.StatusSeeOther)
	loc := rec.Header().Get("Location")
	e.exec("UPDATE event_sales SET held = 1, sold = 2, remaining = 3, refunds = 4 WHERE event_id = ?", strings.TrimPrefix(loc, "/admin/events/"))
	wantBody(t, e.as(ck, http.MethodGet, loc, nil), `id="capacity">30<`, `id="held">1<`, `id="sold">2<`,
		`id="remaining">3<`, `id="refunds">4<`, "2027-02-01 19:00")
	wantBody(t, e.as(ck, http.MethodGet, "/admin/events", nil), "2027-02-01 19:00", "2027-01-20 10:00")
}

func TestAdminNewEventFormKeepsEveryValueAfterAnError(t *testing.T) {
	e := newEnv(t, 0)
	f := eventForm()
	f.Set("title_ja", " ")
	rec := e.as(e.session(), http.MethodPost, "/admin/events", f)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, `value="Summer Concert"`, `value="2027-02-01T19:00"`, `value="2027-02-01T21:00"`,
		`value="2027-01-20T10:00"`, `value="30"`, `value="4000"`)
}

func TestPurchaseReadsQuantityFromTheBodyOnly(t *testing.T) {
	e := newEnv(t, 0)
	rec := e.do(req{method: http.MethodPost, target: "/events/e1/purchases?quantity=2",
		form: url.Values{"email": {"a@example.com"}}})
	wantCode(t, rec, http.StatusBadRequest)
}

func TestNoStoreOnLookupLogoutAndCreate(t *testing.T) {
	e := newEnv(t, 0)
	noStore(t, e.get("/lookup"))
	c := e.session()
	noStore(t, e.as(c, http.MethodPost, "/admin/events", eventForm()))
	noStore(t, e.as(c, http.MethodPost, "/admin/logout", url.Values{}))
}

func TestAdminLoginReturns503WhenTheRequestIsCanceled(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	for range runtime.NumCPU() {
		if err := e.svc.Limiter.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	hr := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader(url.Values{"email": {orgEmail}, "password": {orgPassword}}.Encode())).WithContext(ctx)
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	wantCode(t, rec, http.StatusServiceUnavailable)
}

func TestAdminSessionLookupFailureIs500NotLogin(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	if err := e.s.Read.Close(); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.as(c, http.MethodGet, "/admin/events", nil), http.StatusInternalServerError)
}
