// Package web serves the buyer and organizer pages.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/i18n"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/writer"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/htmx.min.js static/app.css
var staticFS embed.FS

const (
	csp        = "default-src 'self'; frame-ancestors 'none'"
	maxBody    = 16 << 10
	holdWindow = time.Minute
	// htmx stops polling when it receives this status.
	statusStopPolling = 286
)

// Deps are the collaborators of the HTTP layer.
type Deps struct {
	App            *app.Service
	Store          *store.Store
	Writer         *writer.Writer
	I18n           i18n.Translator
	Tokens         auth.Tokens
	Clock          app.Clock
	HoldRatePerMin int // 0 disables the limit
	Log            *slog.Logger
}

type server struct {
	Deps
	pages map[string]*template.Template
	holds *holdLimiter
}

// NewHandler returns the routes wrapped as access log, then security headers,
// then cross-origin protection.
func NewHandler(d Deps) http.Handler {
	s := &server{Deps: d, pages: parsePages(), holds: &holdLimiter{limit: d.HoldRatePerMin, hits: map[string][]time.Time{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.list)
	mux.HandleFunc("GET /events/{id}", s.detail)
	mux.HandleFunc("POST /events/{id}/purchases", s.purchase)
	mux.HandleFunc("GET /purchases/{id}", s.status)
	mux.HandleFunc("GET /lookup", s.lookupForm)
	mux.HandleFunc("POST /lookup", s.lookup)
	static, _ := fs.Sub(staticFS, "static") // cannot fail: "static" is a valid path
	mux.Handle("GET /static/", http.StripPrefix("/static", http.FileServerFS(static)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.fail(w, r, domain.ErrNotFound) })
	return s.accessLog(headers(http.NewCrossOriginProtection().Handler(mux)))
}

// parsePages pairs each page with the layout. The templates are embedded, so a
// parse error is a build defect and panics at startup.
func parsePages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, p := range []string{"list", "detail", "purchase", "lookup", "error"} {
		pages[p] = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+p+".html"))
	}
	return pages
}

func lang(r *http.Request) string { return i18n.Negotiate(r.Header.Get("Accept-Language")) }

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Language", lang(r))
		h.Set("Vary", "Accept-Language, HX-Request")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// accessLog records URL.Path only: the query can carry a lookup token.
func (s *server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.Clock()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		method, path, status := r.Method, r.URL.Path, sw.status
		ms := s.Clock().Sub(start).Milliseconds()
		s.Writer.Send(func(ctx context.Context, tx bun.Tx) error {
			_, err := s.Store.Exec(ctx, tx, "insert_access_log", method, path, status, ms, start.UnixMilli())
			return err
		})
	})
}

// holdLimiter is a per-IP sliding window over the last minute of hold attempts.
type holdLimiter struct {
	mu    sync.Mutex
	limit int
	hits  map[string][]time.Time
	swept time.Time
}

func (l *holdLimiter) allow(ip string, now time.Time) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Dropping idle IPs once a minute keeps the map bounded by recent clients.
	if now.Sub(l.swept) >= holdWindow {
		maps.DeleteFunc(l.hits, func(_ string, ts []time.Time) bool { return len(recent(ts, now)) == 0 })
		l.swept = now
	}
	ts := recent(l.hits[ip], now)
	if len(ts) >= l.limit {
		l.hits[ip] = ts
		return false
	}
	l.hits[ip] = append(ts, now)
	return true
}

func recent(ts []time.Time, now time.Time) []time.Time {
	i := slices.IndexFunc(ts, func(t time.Time) bool { return now.Sub(t) < holdWindow })
	if i < 0 {
		return nil
	}
	return ts[i:]
}

// clientIP uses RemoteAddr only: firstline takes connections directly, so
// forwarding headers come from the client and are not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// parseForm caps the body and rejects invalid UTF-8 so it never reaches the DB.
func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		return domain.ErrBadInput
	}
	for k, vs := range r.PostForm {
		if !utf8.ValidString(k) {
			return domain.ErrBadInput
		}
		for _, v := range vs {
			if !utf8.ValidString(v) {
				return domain.ErrBadInput
			}
		}
	}
	return nil
}

type view struct {
	Lang, Title, Error string
	Events             []eventView
	Event              eventView
	Purchase           purchaseView
	tr                 i18n.Translator
}

func (v view) T(key string, args ...any) string { return v.tr.T(v.Lang, key, args...) }

func (s *server) view(r *http.Request) view { return view{Lang: lang(r), tr: s.I18n} }

// render executes into a buffer first so a template error can still become a 500.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, page, name string, v view) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, v); err != nil {
		s.Log.Error("render template", "page", page, "path", r.URL.Path, "err", err)
		http.Error(w, v.T(i18n.ErrInternal), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

var statuses = []struct {
	err  error
	code int
}{
	{domain.ErrNotFound, http.StatusNotFound},
	{domain.ErrBadInput, http.StatusBadRequest},
	{domain.ErrBadQuantity, http.StatusBadRequest},
	{domain.ErrSoldOut, http.StatusConflict},
	{domain.ErrNotOnSale, http.StatusConflict},
	{domain.ErrDuplicateHold, http.StatusConflict},
	{domain.ErrPaymentUnavailable, http.StatusServiceUnavailable},
}

// errorText maps err to a status and the user's message, logging the detail
// of an error that has no message of its own.
func (s *server) errorText(r *http.Request, v view, err error) (int, string) {
	for _, st := range statuses {
		if errors.Is(err, st.err) {
			return st.code, v.T(i18n.ErrorKey(err))
		}
	}
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	return http.StatusInternalServerError, v.T(i18n.ErrInternal)
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	v := s.view(r)
	code, msg := s.errorText(r, v, err)
	v.Title, v.Error = msg, msg
	s.render(w, r, code, "error", "layout", v)
}
