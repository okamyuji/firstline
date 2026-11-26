package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var requestIDField = regexp.MustCompile(`request_id=(\S+)`)

func TestEachRequestTagsItsErrorLineWithItsOwnID(t *testing.T) {
	e := newEnv(t, 0)
	if err := e.s.Read.Close(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		e.logs.Reset()
		wantCode(t, e.get("/events/e1"), http.StatusInternalServerError)
		line := e.logs.String()
		m := requestIDField.FindStringSubmatch(line)
		if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "path=/events/e1") || m == nil {
			t.Fatalf("ERROR line lacks the request id or path: %q", line)
		}
		ids = append(ids, m[1])
	}
	if ids[0] == ids[1] || len(ids[0]) < 16 {
		t.Errorf("request ids %q; want two distinct random ids", ids)
	}
}

func TestWrongMethodIs405WithAllow(t *testing.T) {
	e := newEnv(t, 0)
	for _, c := range []struct{ method, target, allow string }{
		{http.MethodPost, "/events/e1", "GET, HEAD"},
		{http.MethodDelete, "/", "GET, HEAD"},
		{http.MethodPut, "/lookup", "GET, HEAD, POST"},
		{http.MethodGet, "/admin/logout", "POST"},
		{http.MethodGet, "/events/e1/purchases", "POST"},
	} {
		rec := e.do(req{method: c.method, target: c.target})
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s = %d, Allow %q; want 405, %q", c.method, c.target, rec.Code, rec.Header().Get("Allow"), c.allow)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if rec := e.do(req{method: m, target: "/nope"}); rec.Code != http.StatusNotFound || rec.Header().Get("Allow") != "" {
			t.Errorf("%s /nope = %d, Allow %q; want 404 without Allow", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestAdminCreateEventRejectsBlankAndOverlongTitles(t *testing.T) {
	e := newEnv(t, 0)
	cookie := e.session()
	longest := strings.Repeat("題", 200)
	for name, c := range map[string]struct {
		title string
		want  int
	}{
		"blank":       {" \t　 ", http.StatusBadRequest},
		"over cap":    {longest + "a", http.StatusBadRequest},
		"at the cap":  {longest, http.StatusSeeOther},
		"padded text": {" 夏 ", http.StatusSeeOther},
	} {
		for _, field := range []string{"title_ja", "title_en"} {
			f := eventForm()
			f.Set(field, c.title)
			if rec := e.as(cookie, http.MethodPost, "/admin/events", f); rec.Code != c.want {
				t.Errorf("%s %s (%d runes) = %d; want %d", name, field, utf8.RuneCountInString(c.title), rec.Code, c.want)
			}
		}
	}
}

func TestAdminNewEventFormStatesTheServerLimits(t *testing.T) {
	e := newEnv(t, 0)
	rec := e.as(e.session(), http.MethodGet, "/admin/events/new", url.Values{})
	wantBody(t, rec, `name="capacity" required min="1" max="100000"`, `name="price" required min="0" max="10000000"`,
		`name="title_ja" required maxlength="200"`, `name="title_en" required maxlength="200"`)
}
