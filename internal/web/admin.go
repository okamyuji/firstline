package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/i18n"
)

const (
	sessionCookie = "__Host-firstline_session"
	// Matches the fixed session lifetime in app.Session.
	sessionCookieAge = 12 * 60 * 60
	datetimeLayout   = "2006-01-02T15:04"
)

func (s *server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", noStore(s.loginForm))
	mux.HandleFunc("POST /admin/login", noStore(s.login))
	mux.HandleFunc("POST /admin/logout", noStore(s.withOrganizer(s.logout)))
	mux.HandleFunc("GET /admin/events", noStore(s.withOrganizer(s.adminEvents)))
	mux.HandleFunc("GET /admin/events/new", noStore(s.withOrganizer(s.newEventForm)))
	mux.HandleFunc("POST /admin/events", noStore(s.withOrganizer(s.createEvent)))
	mux.HandleFunc("GET /admin/events/{id}", noStore(s.withOrganizer(s.adminSales)))
}

func noStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

// withOrganizer sends a request without a live session to the login page.
func (s *server) withOrganizer(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		org, err := s.App.Session(r.Context(), c.Value)
		if errors.Is(err, domain.ErrNoSession) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r, org)
	}
}

func (s *server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.showLogin(w, r, http.StatusOK, "")
}

func (s *server) showLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	v := s.view(r)
	v.Title, v.Error = v.T(i18n.AdminLoginHeading), msg
	s.render(w, r, status, "admin_login", "layout", v)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		code, msg := s.errorText(r, s.view(r), err)
		s.showLogin(w, r, code, msg)
		return
	}
	id, err := s.App.Login(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("password"), clientIP(r))
	if err != nil {
		code, msg := s.loginError(r, err)
		s.showLogin(w, r, code, msg)
		return
	}
	http.SetCookie(w, sessionCookieFor(id, sessionCookieAge))
	http.Redirect(w, r, "/admin/events", http.StatusSeeOther)
}

// loginError keeps a wrong email and a wrong password indistinguishable: both
// re-show the form with status 200.
func (s *server) loginError(r *http.Request, err error) (int, string) {
	v := s.view(r)
	switch {
	case errors.Is(err, domain.ErrBadCredentials):
		return http.StatusOK, v.T(i18n.ErrBadCredentials)
	case errors.Is(err, domain.ErrTooManyAttempts):
		return http.StatusTooManyRequests, v.T(i18n.ErrTooManyAttempts)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, v.T(i18n.ErrInternal)
	}
	return s.errorText(r, v, err)
}

func sessionCookieFor(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ string) {
	c, _ := r.Cookie(sessionCookie) // withOrganizer already required it
	if err := s.App.Logout(r.Context(), c.Value); err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, sessionCookieFor("", -1))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *server) adminEvents(w http.ResponseWriter, r *http.Request, org string) {
	v := s.view(r)
	now := s.Clock()
	var rows []eventRow
	if err := s.Store.Select(r.Context(), s.Store.Read, &rows, "list_events_by_organizer", org); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, e := range rows {
		v.Events = append(v.Events, v.event(e, now))
	}
	v.Title = v.T(i18n.AdminEventsHead)
	s.render(w, r, http.StatusOK, "admin_events", "layout", v)
}

func (s *server) newEventForm(w http.ResponseWriter, r *http.Request, _ string) {
	s.showNewEvent(w, r, http.StatusOK, "")
}

func (s *server) showNewEvent(w http.ResponseWriter, r *http.Request, status int, msg string) {
	v := s.view(r)
	v.Form = r.PostForm
	v.Title, v.Error = v.T(i18n.AdminCreateHead), msg
	s.render(w, r, status, "admin_new", "layout", v)
}

func (s *server) createEvent(w http.ResponseWriter, r *http.Request, org string) {
	id, err := s.create(w, r, org)
	if err != nil {
		code, msg := s.errorText(r, s.view(r), err)
		s.showNewEvent(w, r, code, msg)
		return
	}
	http.Redirect(w, r, "/admin/events/"+id, http.StatusSeeOther)
}

func (s *server) create(w http.ResponseWriter, r *http.Request, org string) (string, error) {
	if err := parseForm(w, r); err != nil {
		return "", err
	}
	// Organizers type event times in Japan time, the zone of every screen.
	jst := time.FixedZone("JST", 9*60*60)
	in := app.EventInput{TitleJa: r.PostForm.Get("title_ja"), TitleEn: r.PostForm.Get("title_en")}
	var err error
	for _, f := range []struct {
		dst  *time.Time
		name string
	}{{&in.StartsAt, "starts_at"}, {&in.EndsAt, "ends_at"}, {&in.SaleStartsAt, "sale_starts_at"}} {
		if *f.dst, err = time.ParseInLocation(datetimeLayout, r.PostForm.Get(f.name), jst); err != nil {
			return "", domain.ErrBadInput
		}
	}
	if in.Capacity, err = strconv.Atoi(r.PostForm.Get("capacity")); err != nil {
		return "", domain.ErrBadInput
	}
	if in.Price, err = strconv.Atoi(r.PostForm.Get("price")); err != nil {
		return "", domain.ErrBadInput
	}
	return s.App.CreateEvent(r.Context(), org, in)
}

// adminSales answers 404 for another organizer's event, the same as for a missing one.
func (s *server) adminSales(w http.ResponseWriter, r *http.Request, org string) {
	var e eventRow
	err := s.Store.Select(r.Context(), s.Store.Read, &e, "get_event", r.PathValue("id"))
	if err == nil && e.OrganizerID != org {
		err = sql.ErrNoRows
	}
	v := s.view(r)
	if err == nil {
		err = s.Store.Select(r.Context(), s.Store.Read, &v.Sales, "get_sales", e.ID)
	}
	if err != nil {
		s.fail(w, r, notFound(err))
		return
	}
	e.Remaining = v.Sales.Remaining
	v.Event = v.event(e, s.Clock())
	v.Title = v.Event.Title
	s.render(w, r, http.StatusOK, "admin_sales", "layout", v)
}
