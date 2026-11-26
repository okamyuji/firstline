package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/uptrace/bun"
)

const (
	orgEmail = "owner@example.com"
	orgPass  = "あいうえおかきくけこさし"
)

// orgEnv swaps Argon2id for a cheap fake that counts verifications, so the
// tests check the call pattern rather than pay 64 MiB per hash.
type orgEnv struct {
	*env
	sv       *app.Service
	verified atomic.Int32
	orgID    string
}

func newOrgEnv(t *testing.T) *orgEnv {
	t.Helper()
	o := &orgEnv{env: newEnv(t, inventory.NewMemory())}
	o.sv = o.svc()
	o.sv.SetPasswordFuncs(
		func(pw string) (string, error) { return "h:" + pw, nil },
		func(hash, pw string) (bool, error) { o.verified.Add(1); return hash == "h:"+pw, nil },
	)
	if err := o.sv.CreateOrganizer(t.Context(), orgEmail, orgPass); err != nil {
		t.Fatal(err)
	}
	if err := o.sv.Store.Read.QueryRowContext(t.Context(), "SELECT id FROM organizers").Scan(&o.orgID); err != nil {
		t.Fatal(err)
	}
	return o
}

// flush waits for every async item sent before it: the writer commits items in queue order.
func (o *orgEnv) flush() {
	o.t.Helper()
	if err := o.sv.Writer.Do(o.t.Context(), func(context.Context, bun.Tx) error { return nil }); err != nil {
		o.t.Fatal(err)
	}
}

func idHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func (o *orgEnv) session(id string, created, seen time.Time) {
	o.exec("INSERT INTO sessions (id_hash, organizer_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
		idHash(id), o.orgID, created.UnixMilli(), seen.UnixMilli())
}

func (o *orgEnv) lastSeen(id string) int64 {
	o.t.Helper()
	var at int64
	if err := o.sv.Store.Read.QueryRowContext(o.t.Context(), "SELECT last_seen_at FROM sessions WHERE id_hash = ?", idHash(id)).
		Scan(&at); err != nil {
		o.t.Fatal(err)
	}
	return at
}

func (o *orgEnv) fillLimiter() {
	for range runtime.NumCPU() {
		if err := o.sv.Limiter.Acquire(o.t.Context()); err != nil {
			o.t.Fatal(err)
		}
	}
}

func shortCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestCreateOrganizerValidatesInput(t *testing.T) {
	o := newOrgEnv(t)
	for _, c := range []struct{ email, pw string }{
		{"Alice <a@example.com>", orgPass},
		{"<a@example.com>", orgPass},
		{"not-an-address", orgPass},
		{" a@example.com", orgPass},
		{"a@example.com", "あいうえおかきくけこさ"},
	} {
		if err := o.sv.CreateOrganizer(t.Context(), c.email, c.pw); !errors.Is(err, domain.ErrBadInput) {
			t.Errorf("CreateOrganizer(%q, %q) = %v; want ErrBadInput", c.email, c.pw, err)
		}
	}
	o.wantCount(1, "SELECT COUNT(*) FROM organizers")
}

func TestCreateOrganizerStoresLowerCaseEmailAndHash(t *testing.T) {
	o := newOrgEnv(t)
	if err := o.sv.CreateOrganizer(t.Context(), "Bob@Example.COM", "abcdefghijkl"); err != nil {
		t.Fatal(err)
	}
	o.wantCount(1, "SELECT COUNT(*) FROM organizers WHERE email = 'bob@example.com' AND password_hash = 'h:abcdefghijkl' AND length(id) = 22")
	if err := o.sv.CreateOrganizer(t.Context(), "BOB@example.com", "abcdefghijkl"); !errors.Is(err, domain.ErrDuplicateOrganizer) {
		t.Errorf("second CreateOrganizer differing in case = %v; want ErrDuplicateOrganizer", err)
	}
}

func TestCreateOrganizerHashError(t *testing.T) {
	o := newOrgEnv(t)
	o.sv.SetPasswordFuncs(func(string) (string, error) { return "", errBoom }, nil)
	if err := o.sv.CreateOrganizer(t.Context(), "c@example.com", orgPass); !errors.Is(err, errBoom) {
		t.Errorf("CreateOrganizer = %v; want the hash error", err)
	}
}

func TestCreateOrganizerWaitsForLimiter(t *testing.T) {
	o := newOrgEnv(t)
	o.fillLimiter()
	if err := o.sv.CreateOrganizer(shortCtx(t), "c@example.com", orgPass); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CreateOrganizer with a full limiter = %v; want DeadlineExceeded", err)
	}
}

func TestLoginStoresOnlySessionIDHash(t *testing.T) {
	o := newOrgEnv(t)
	id, err := o.sv.Login(t.Context(), "Owner@Example.com", orgPass, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := base64.RawURLEncoding.DecodeString(id); err != nil || len(b) != 32 {
		t.Errorf("session ID %q decodes to %d bytes, %v; want 32", id, len(b), err)
	}
	o.wantCount(1, "SELECT COUNT(*) FROM sessions WHERE id_hash = ? AND organizer_id = ? AND created_at = ? AND last_seen_at = ?",
		idHash(id), o.orgID, now.UnixMilli(), now.UnixMilli())
	o.wantCount(0, "SELECT COUNT(*) FROM sessions WHERE id_hash = ?", id)
	if got, err := o.sv.Session(t.Context(), id); got != o.orgID || err != nil {
		t.Errorf("Session = %q, %v; want %q", got, err, o.orgID)
	}
}

func TestLoginVerifiesOnceForWrongPasswordAndUnknownEmail(t *testing.T) {
	o := newOrgEnv(t)
	for _, email := range []string{orgEmail, "nobody@example.com"} {
		o.verified.Store(0)
		if _, err := o.sv.Login(t.Context(), email, orgPass+"x", "10.0.0.1"); !errors.Is(err, domain.ErrBadCredentials) {
			t.Errorf("Login(%s) = %v; want ErrBadCredentials", email, err)
		}
		if n := o.verified.Load(); n != 1 {
			t.Errorf("Login(%s) verified %d times; want 1", email, n)
		}
	}
	o.wantCount(0, "SELECT COUNT(*) FROM sessions")
}

func TestLoginThrottlesAfterFiveFailures(t *testing.T) {
	o := newOrgEnv(t)
	for range 5 {
		if _, err := o.sv.Login(t.Context(), orgEmail, "wrong-password", "10.0.0.1"); !errors.Is(err, domain.ErrBadCredentials) {
			t.Fatal(err)
		}
	}
	o.verified.Store(0)
	if _, err := o.sv.Login(t.Context(), orgEmail, orgPass, "10.0.0.2"); !errors.Is(err, domain.ErrTooManyAttempts) {
		t.Errorf("6th Login = %v; want ErrTooManyAttempts", err)
	}
	if n := o.verified.Load(); n != 0 {
		t.Errorf("a throttled Login verified %d times; want 0", n)
	}
	o.clock = now.Add(15 * time.Minute)
	if _, err := o.sv.Login(t.Context(), orgEmail, orgPass, "10.0.0.2"); err != nil {
		t.Errorf("Login 15 minutes later = %v; want nil", err)
	}
}

// concurrentWrongLogins sends n wrong-password logins at once and counts the
// verifications; the slow verify keeps every request in flight together.
func (o *orgEnv) concurrentWrongLogins(n int, email, ip func(i int) string) (verified int32, throttled int) {
	o.t.Helper()
	o.sv.SetPasswordFuncs(func(pw string) (string, error) { return "h:" + pw, nil },
		func(hash, pw string) (bool, error) {
			o.verified.Add(1)
			time.Sleep(20 * time.Millisecond)
			return hash == "h:"+pw, nil
		})
	o.verified.Store(0)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, errs[i] = o.sv.Login(o.t.Context(), email(i), "wrong-password", ip(i)) })
	}
	wg.Wait()
	for _, err := range errs {
		switch {
		case errors.Is(err, domain.ErrTooManyAttempts):
			throttled++
		case !errors.Is(err, domain.ErrBadCredentials):
			o.t.Fatalf("concurrent Login = %v", err)
		}
	}
	return o.verified.Load(), throttled
}

func TestConcurrentLoginsVerifyAtMostFivePerEmail(t *testing.T) {
	o := newOrgEnv(t)
	verified, throttled := o.concurrentWrongLogins(100,
		func(int) string { return orgEmail }, func(i int) string { return fmt.Sprintf("10.0.%d.1", i) })
	if verified != 5 || throttled != 95 {
		t.Errorf("100 concurrent wrong logins for one email: verified=%d throttled=%d; want 5 and 95", verified, throttled)
	}
}

func TestConcurrentLoginsVerifyAtMostTwentyPerIP(t *testing.T) {
	o := newOrgEnv(t)
	verified, throttled := o.concurrentWrongLogins(100,
		func(i int) string { return fmt.Sprintf("u%d@example.com", i) }, func(int) string { return "10.0.0.1" })
	if verified != 20 || throttled != 80 {
		t.Errorf("100 concurrent wrong logins from one IP: verified=%d throttled=%d; want 20 and 80", verified, throttled)
	}
}

func TestLoginWaitsForLimiter(t *testing.T) {
	o := newOrgEnv(t)
	o.fillLimiter()
	if _, err := o.sv.Login(shortCtx(t), orgEmail, orgPass, "10.0.0.1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Login with a full limiter = %v; want DeadlineExceeded", err)
	}
}

func TestLoginReturnsVerifyError(t *testing.T) {
	o := newOrgEnv(t)
	o.sv.SetPasswordFuncs(func(string) (string, error) { return "", errBoom },
		func(string, string) (bool, error) { return false, errBoom })
	for _, email := range []string{orgEmail, "nobody@example.com"} {
		if _, err := o.sv.Login(t.Context(), email, orgPass, "10.0.0.1"); !errors.Is(err, errBoom) {
			t.Errorf("Login(%s) = %v; want errBoom", email, err)
		}
	}
}

func TestSessionExpiresAtTheBoundaries(t *testing.T) {
	o := newOrgEnv(t)
	o.session("old-but-valid", now.Add(-12*time.Hour+time.Millisecond), now)
	o.session("too-old", now.Add(-12*time.Hour), now)
	o.session("idle-but-valid", now.Add(-time.Hour), now.Add(-2*time.Hour+time.Millisecond))
	o.session("too-idle", now.Add(-time.Hour), now.Add(-2*time.Hour))
	for id, ok := range map[string]bool{"old-but-valid": true, "too-old": false, "idle-but-valid": true, "too-idle": false, "unknown": false} {
		got, err := o.sv.Session(t.Context(), id)
		if ok && (got != o.orgID || err != nil) {
			t.Errorf("Session(%s) = %q, %v; want %q", id, got, err, o.orgID)
		}
		if !ok && (got != "" || !errors.Is(err, domain.ErrNoSession)) {
			t.Errorf("Session(%s) = %q, %v; want ErrNoSession", id, got, err)
		}
	}
}

func TestSessionTouchesAfterFiveMinutes(t *testing.T) {
	o := newOrgEnv(t)
	o.session("fresh", now, now.Add(-5*time.Minute+time.Millisecond))
	o.session("stale", now, now.Add(-5*time.Minute))
	for _, id := range []string{"fresh", "stale"} {
		if _, err := o.sv.Session(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	o.flush()
	if got, want := o.lastSeen("fresh"), now.Add(-5*time.Minute+time.Millisecond).UnixMilli(); got != want {
		t.Errorf("fresh last_seen_at = %d; want unchanged %d", got, want)
	}
	if got := o.lastSeen("stale"); got != now.UnixMilli() {
		t.Errorf("stale last_seen_at = %d; want %d", got, now.UnixMilli())
	}
}

func TestLogoutDeletesTheSession(t *testing.T) {
	o := newOrgEnv(t)
	o.session("keep", now, now)
	id, err := o.sv.Login(t.Context(), orgEmail, orgPass, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.sv.Logout(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := o.sv.Session(t.Context(), id); !errors.Is(err, domain.ErrNoSession) {
		t.Errorf("Session after Logout = %v; want ErrNoSession", err)
	}
	o.wantCount(1, "SELECT COUNT(*) FROM sessions WHERE id_hash = ?", idHash("keep"))
}

func TestPurgeSessionsDeletesOnlyExpiredRows(t *testing.T) {
	o := newOrgEnv(t)
	o.session("old-but-valid", now.Add(-12*time.Hour+time.Millisecond), now)
	o.session("too-old", now.Add(-12*time.Hour), now)
	o.session("idle-but-valid", now.Add(-time.Hour), now.Add(-2*time.Hour+time.Millisecond))
	o.session("too-idle", now.Add(-time.Hour), now.Add(-2*time.Hour))
	o.sv.PurgeSessions(t.Context())
	o.flush()
	o.wantCount(2, "SELECT COUNT(*) FROM sessions")
	o.wantCount(2, "SELECT COUNT(*) FROM sessions WHERE id_hash IN (?, ?)", idHash("old-but-valid"), idHash("idle-but-valid"))
}

func validEvent() app.EventInput {
	start := now.Add(48 * time.Hour)
	return app.EventInput{
		TitleJa: "公演", TitleEn: "Show", StartsAt: start, EndsAt: start.Add(time.Millisecond),
		SaleStartsAt: start.Add(-time.Millisecond), Capacity: 1, Price: 0,
	}
}

func TestCreateEventStoresRowsAndInventory(t *testing.T) {
	o := newOrgEnv(t)
	in := validEvent()
	in.Capacity, in.Price = 30, 4000
	id, err := o.sv.CreateEvent(t.Context(), o.orgID, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := o.inv.Remaining(t.Context(), id); got != 30 || err != nil {
		t.Errorf("Remaining = %d, %v; want 30", got, err)
	}
	o.wantCount(1, `SELECT COUNT(*) FROM events WHERE id = ? AND organizer_id = ? AND title_ja = '公演' AND title_en = 'Show'
		AND starts_at = ? AND ends_at = ? AND capacity = 30 AND price = 4000 AND sale_starts_at = ? AND next_ticket_number = 1`,
		id, o.orgID, in.StartsAt.UnixMilli(), in.EndsAt.UnixMilli(), in.SaleStartsAt.UnixMilli())
	o.wantCount(1, "SELECT COUNT(*) FROM event_sales WHERE event_id = ? AND held = 0 AND sold = 0 AND remaining = 30 AND refunds = 0", id)
}

func TestCreateEventAcceptsBoundaryValues(t *testing.T) {
	o := newOrgEnv(t)
	if _, err := o.sv.CreateEvent(t.Context(), o.orgID, validEvent()); err != nil {
		t.Errorf("CreateEvent at the boundaries = %v; want nil", err)
	}
}

func TestCreateEventValidatesInput(t *testing.T) {
	o := newOrgEnv(t)
	for name, change := range map[string]func(*app.EventInput){
		"empty ja title":  func(in *app.EventInput) { in.TitleJa = "" },
		"empty en title":  func(in *app.EventInput) { in.TitleEn = "" },
		"ends at start":   func(in *app.EventInput) { in.EndsAt = in.StartsAt },
		"zero capacity":   func(in *app.EventInput) { in.Capacity = 0 },
		"negative price":  func(in *app.EventInput) { in.Price = -1 },
		"sale at start":   func(in *app.EventInput) { in.SaleStartsAt = in.StartsAt },
		"sale after ends": func(in *app.EventInput) { in.SaleStartsAt = in.EndsAt },
	} {
		in := validEvent()
		change(&in)
		if _, err := o.sv.CreateEvent(t.Context(), o.orgID, in); !errors.Is(err, domain.ErrBadInput) {
			t.Errorf("%s: CreateEvent = %v; want ErrBadInput", name, err)
		}
	}
	o.wantCount(1, "SELECT COUNT(*) FROM events")
}

func TestCreateEventReturnsInventoryError(t *testing.T) {
	o := newOrgEnv(t)
	o.sv.Inventory = addFails{inventory.NewMemory()}
	if _, err := o.sv.CreateEvent(t.Context(), o.orgID, validEvent()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("CreateEvent = %v; want the Add error", err)
	}
}
