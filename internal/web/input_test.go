package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNULInPathOrFormIs400WithoutErrorLog(t *testing.T) {
	e := newEnv(t, 10)
	cookie := e.session()
	withNUL := eventForm()
	withNUL.Set("title_ja", "a\x00b")
	for name, r := range map[string]req{
		"event id":       {method: http.MethodGet, target: "/events/%00"},
		"inner event id": {method: http.MethodGet, target: "/events/a%00b"},
		"unknown path":   {method: http.MethodGet, target: "/%00"},
		"purchase id":    {method: http.MethodGet, target: "/purchases/%00?token=x"},
		"login email":    {method: http.MethodPost, target: "/admin/login", form: url.Values{"email": {"a@b.com\x00"}, "password": {"x"}}},
		"form key":       {method: http.MethodPost, target: "/admin/login", form: url.Values{"email\x00": {"a@b.com"}}},
		"event title":    {method: http.MethodPost, target: "/admin/events", form: withNUL, header: map[string]string{"Cookie": cookie}},
	} {
		t.Run(name, func(t *testing.T) {
			e.logs.Reset()
			wantCode(t, e.do(r), http.StatusBadRequest)
			// The writer's OnAsyncError fails the test if the access log insert fails.
			e.flush()
			if strings.Contains(e.logs.String(), "level=ERROR") {
				t.Errorf("ERROR logged: %s", e.logs)
			}
		})
	}
}

func TestAccessLogPathIsCutAtAUTF8Boundary(t *testing.T) {
	for name, tc := range map[string]struct{ path, want string }{
		"short":         {"/" + strings.Repeat("a", 1023), "/" + strings.Repeat("a", 1023)},
		"over":          {"/" + strings.Repeat("a", 1500), "/" + strings.Repeat("a", 1023)},
		"rune at limit": {"/" + strings.Repeat("a", 1021) + "あ", "/" + strings.Repeat("a", 1021)},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, 10)
			e.exec("DELETE FROM access_logs")
			wantCode(t, e.get(tc.path), http.StatusNotFound)
			e.flush()
			var path string
			e.row("SELECT path FROM access_logs", &path)
			if path != tc.want {
				t.Errorf("stored %d bytes; want %d", len(path), len(tc.want))
			}
		})
	}
}
