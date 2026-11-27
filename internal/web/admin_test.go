package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	orgEmail    = "host@example.com"
	orgPassword = "correct horse battery"
	cookieName  = "__Host-firstline_session"
)

func (e *env) organizer() {
	e.t.Helper()
	if err := e.svc.CreateOrganizer(e.t.Context(), orgEmail, orgPassword); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) login(password string) *httptest.ResponseRecorder {
	return e.do(req{method: http.MethodPost, target: "/admin/login",
		form: url.Values{"email": {orgEmail}, "password": {password}}})
}

// session creates the organizer, logs in and returns the Cookie header value.
func (e *env) session() string {
	e.t.Helper()
	e.organizer()
	rec := e.login(orgPassword)
	wantCode(e.t, rec, http.StatusSeeOther)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c.Name + "=" + c.Value
		}
	}
	e.t.Fatal("no session cookie")
	return ""
}

func (e *env) as(cookie, method, target string, form url.Values) *httptest.ResponseRecorder {
	return e.do(req{method: method, target: target, form: form, header: map[string]string{"Cookie": cookie}})
}

func eventForm() url.Values {
	return url.Values{
		"title_ja": {"夏の演奏会"}, "title_en": {"Summer Concert"},
		"starts_at": {"2027-02-01T19:00"}, "ends_at": {"2027-02-01T21:00"},
		"sale_starts_at": {"2027-01-20T10:00"}, "capacity": {"30"}, "price": {"4000"},
	}
}

func TestAdminWithoutSessionRedirectsToLogin(t *testing.T) {
	e := newEnv(t, 0)
	for _, target := range []string{"/admin/events", "/admin/events/new", "/admin/events/e1"} {
		rec := e.get(target)
		wantCode(t, rec, http.StatusSeeOther)
		if got := rec.Header().Get("Location"); got != "/admin/login" {
			t.Errorf("%s: Location = %q", target, got)
		}
	}
	rec := e.do(req{method: http.MethodPost, target: "/admin/events", form: eventForm()})
	wantCode(t, rec, http.StatusSeeOther)
}

func TestAdminLoginPageHasForm(t *testing.T) {
	e := newEnv(t, 0)
	rec := e.get("/admin/login")
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `name="email"`, `name="password"`, `action="/admin/login"`, "主催者ログイン")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestAdminLoginSetsHardenedCookie(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	rec := e.login(orgPassword)
	wantCode(t, rec, http.StatusSeeOther)
	if got := rec.Header().Get("Location"); got != "/admin/events" {
		t.Errorf("Location = %q", got)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %v", cs)
	}
	want := http.Cookie{Name: cookieName, Value: cs[0].Value, Path: "/", MaxAge: 12 * 3600,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	got := *cs[0]
	got.Raw, got.RawExpires, got.Unparsed = "", "", nil
	if got.Value == "" || !reflect.DeepEqual(got, want) {
		t.Errorf("cookie = %+v, want %+v", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestAdminLoginWrongPasswordShowsGenericMessage(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	rec := e.login("wrong password!!")
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, "メールアドレスまたはパスワードが違います。")
	if len(rec.Result().Cookies()) != 0 {
		t.Error("cookie set on failed login")
	}
}

func TestAdminLoginIsThrottledAfterFiveFailures(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	for range 5 {
		wantCode(t, e.login("wrong password!!"), http.StatusOK)
	}
	rec := e.login(orgPassword)
	wantCode(t, rec, http.StatusTooManyRequests)
	wantBody(t, rec, "15分後にやり直してください")
}

func TestAdminLoginReturns503WhenNoArgonSlotIsFree(t *testing.T) {
	e := newEnv(t, 0)
	e.organizer()
	for range runtime.GOMAXPROCS(0) {
		if err := e.svc.Limiter.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	hr := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader(url.Values{"email": {orgEmail}, "password": {orgPassword}}.Encode())).WithContext(ctx)
	hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	wantCode(t, rec, http.StatusServiceUnavailable)
}

func TestAdminLoginRejectsMalformedForm(t *testing.T) {
	e := newEnv(t, 0)
	rec := e.do(req{method: http.MethodPost, target: "/admin/login", body: "email=%ff&password=x",
		header: map[string]string{}})
	wantCode(t, rec, http.StatusBadRequest)
}

func TestAdminEventOfAnotherOrganizerIs404(t *testing.T) {
	e := newEnv(t, 0) // e1 belongs to organizer "o"
	c := e.session()
	wantCode(t, e.as(c, http.MethodGet, "/admin/events/e1", nil), http.StatusNotFound)
	wantCode(t, e.as(c, http.MethodGet, "/admin/events/missing", nil), http.StatusNotFound)
}

func TestAdminCreateEventThenSalesShowsCapacityAndRemaining(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	rec := e.as(c, http.MethodPost, "/admin/events", eventForm())
	wantCode(t, rec, http.StatusSeeOther)
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/events/") {
		t.Fatalf("Location = %q", loc)
	}
	sales := e.as(c, http.MethodGet, loc, nil)
	wantCode(t, sales, http.StatusOK)
	wantBody(t, sales, "夏の演奏会", `<dd id="capacity">30</dd>`, `<dd id="remaining">30</dd>`,
		`<dd id="held">0</dd>`, `<dd id="sold">0</dd>`, `<dd id="refunds">0</dd>`)

	list := e.as(c, http.MethodGet, "/admin/events", nil)
	wantCode(t, list, http.StatusOK)
	wantBody(t, list, "夏の演奏会", loc, "2027-01-20 10:00", "残数 30")
	lackBody(t, list, titleJA)
}

func TestAdminCreateEventReadsTimesAsJST(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	rec := e.as(c, http.MethodPost, "/admin/events", eventForm())
	wantCode(t, rec, http.StatusSeeOther)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/admin/events/")
	var starts int64
	if err := e.s.Read.QueryRowContext(t.Context(), "SELECT starts_at FROM events WHERE id = ?", id).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 2, 1, 19, 0, 0, 0, time.FixedZone("JST", 9*3600)).UnixMilli()
	if starts != want {
		t.Errorf("starts_at = %d, want %d", starts, want)
	}
}

func TestAdminCreateEventRejectsBadInputWithForm(t *testing.T) {
	cases := map[string]func(url.Values){
		"capacity zero":       func(v url.Values) { v.Set("capacity", "0") },
		"capacity not number": func(v url.Values) { v.Set("capacity", "x") },
		"price not number":    func(v url.Values) { v.Set("price", "") },
		"starts not a time":   func(v url.Values) { v.Set("starts_at", "tomorrow") },
		"ends not a time":     func(v url.Values) { v.Set("ends_at", "") },
		"sale not a time":     func(v url.Values) { v.Set("sale_starts_at", "x") },
		"ends before starts":  func(v url.Values) { v.Set("ends_at", "2027-02-01T18:00") },
		"empty title":         func(v url.Values) { v.Set("title_en", "") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, 0)
			c := e.session()
			form := eventForm()
			mutate(form)
			rec := e.as(c, http.MethodPost, "/admin/events", form)
			wantCode(t, rec, http.StatusBadRequest)
			wantBody(t, rec, "入力に誤りがあります", `name="title_ja"`, "夏の演奏会")
			var n int
			if err := e.s.Read.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM events WHERE organizer_id <> 'o'").Scan(&n); err != nil || n != 0 {
				t.Errorf("events created: n=%d err=%v", n, err)
			}
		})
	}
}

func TestAdminNewEventFormListsEveryField(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	rec := e.as(c, http.MethodGet, "/admin/events/new", nil)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, `action="/admin/events"`, `name="title_ja"`, `name="title_en"`, `name="starts_at"`,
		`name="ends_at"`, `name="sale_starts_at"`, `name="capacity"`, `name="price"`, `type="datetime-local"`)
	lackBody(t, rec, "admin.", "label.")
}

func TestAdminLogoutDeletesSessionAndCookie(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	count := func() int {
		var n int
		if err := e.s.Read.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("sessions = %d before logout", count())
	}
	rec := e.as(c, http.MethodPost, "/admin/logout", url.Values{})
	wantCode(t, rec, http.StatusSeeOther)
	if got := rec.Header().Get("Location"); got != "/admin/login" {
		t.Errorf("Location = %q", got)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != cookieName || cs[0].MaxAge >= 0 || !cs[0].Secure || cs[0].Path != "/" {
		t.Errorf("cookies = %+v", cs)
	}
	if count() != 0 {
		t.Errorf("sessions = %d after logout", count())
	}
	wantCode(t, e.as(c, http.MethodGet, "/admin/events", nil), http.StatusSeeOther)
}

func TestAdminExpiredSessionRedirectsToLogin(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	e.at = e.at.Add(13 * time.Hour)
	rec := e.as(c, http.MethodGet, "/admin/events", nil)
	wantCode(t, rec, http.StatusSeeOther)
}

func TestAdminPagesSendNoStore(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	for _, target := range []string{"/admin/events", "/admin/events/new", "/admin/events/e1"} {
		if got := e.as(c, http.MethodGet, target, nil).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", target, got)
		}
	}
	if got := e.get("/admin/events").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("redirect: Cache-Control = %q", got)
	}
}

func TestAdminPostFromAnotherOriginIsForbidden(t *testing.T) {
	e := newEnv(t, 0)
	c := e.session()
	rec := e.do(req{method: http.MethodPost, target: "/admin/events", form: eventForm(),
		header: map[string]string{"Cookie": c, "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}})
	wantCode(t, rec, http.StatusForbidden)
}

func TestAdminEnglishPagesUseEnglishText(t *testing.T) {
	e := newEnv(t, 0)
	rec := e.get("/admin/login", "Accept-Language", "en")
	wantBody(t, rec, "Organizer login", "Password")
	lackBody(t, rec, "admin.login")
}
