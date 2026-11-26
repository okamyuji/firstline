package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/okamyuji/firstline/internal/domain"
)

// shortDir returns a directory short enough for a unix socket path: macOS
// caps it at 104 bytes, which t.TempDir with a long test name can exceed.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// requireUnixSockets skips the test only where binding a unix socket is not
// permitted (a sandbox), unless FIRSTLINE_REQUIRE_SOCKET_TESTS=1 makes that a
// failure. Any other listen error fails the test.
func requireUnixSockets(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(shortDir(t), "probe.sock"))
	if err == nil {
		_ = ln.Close()
		return
	}
	if !errors.Is(err, syscall.EPERM) || os.Getenv("FIRSTLINE_REQUIRE_SOCKET_TESTS") == "1" {
		t.Fatalf("unix socket listen: %v", err)
	}
	t.Skipf("binding a unix socket is not permitted here (%v); set FIRSTLINE_REQUIRE_SOCKET_TESTS=1 where it is", err)
}

func TestCheckCreated(t *testing.T) {
	dial := errors.New("dial")
	for _, tc := range []struct {
		status  int
		err     error
		wantErr bool
	}{{http.StatusCreated, nil, false}, {http.StatusConflict, nil, true}, {http.StatusBadRequest, nil, true}, {0, dial, true}} {
		err := checkCreated(tc.status, tc.err)
		if (err != nil) != tc.wantErr || (tc.err != nil && !errors.Is(err, tc.err)) {
			t.Errorf("checkCreated(%d, %v) = %v", tc.status, tc.err, err)
		}
	}
}

type creatorFunc func(ctx context.Context, email, password string) error

func (f creatorFunc) CreateOrganizer(ctx context.Context, email, password string) error {
	return f(ctx, email, password)
}

func TestOrganizerHandlerStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        error
		want       int
	}{
		{"created", `{"email":"a@x.example","password":"long enough pw"}`, nil, http.StatusCreated},
		{"bad input", `{"email":"a","password":"p"}`, domain.ErrBadInput, http.StatusBadRequest},
		{"duplicate", `{"email":"a@x.example","password":"long enough pw"}`, domain.ErrDuplicateOrganizer, http.StatusConflict},
		{"internal", `{"email":"a@x.example","password":"long enough pw"}`, errors.New("disk"), http.StatusInternalServerError},
		{"malformed JSON", `{"email":`, nil, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotEmail, gotPW string
			rep := &recordingReporter{}
			h := organizerHandler(creatorFunc(func(_ context.Context, e, p string) error {
				gotEmail, gotPW = e, p
				return tc.err
			}), reportTo(rep))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/organizers", strings.NewReader(tc.body)))
			if rr.Code != tc.want {
				t.Errorf("status = %d; want %d", rr.Code, tc.want)
			}
			if tc.want == http.StatusCreated && (gotEmail != "a@x.example" || gotPW != "long enough pw") {
				t.Errorf("CreateOrganizer got %q, %q", gotEmail, gotPW)
			}
			if wantReports := btoi(tc.want == http.StatusInternalServerError); len(rep.errs) != wantReports {
				t.Errorf("reported %v; want %d reports", rep.errs, wantReports)
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestOrganizerHandlerServesOnlyPostOrganizers(t *testing.T) {
	h := organizerHandler(creatorFunc(func(context.Context, string, string) error { return nil }), reportTo(&recordingReporter{}))
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/organizers", nil),
		httptest.NewRequest(http.MethodPost, "/healthz", nil),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code == http.StatusCreated {
			t.Errorf("%s %s = 201", r.Method, r.URL.Path)
		}
	}
}

// fakeSocket serves status on a unix socket and records the request body.
func fakeSocket(t *testing.T, status int) (string, <-chan string) {
	t.Helper()
	path := filepath.Join(shortDir(t), "admin.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	body := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		body <- r.Method + " " + r.URL.Path + " " + b.String()
		w.WriteHeader(status)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path, body
}

func runCreate(t *testing.T, over map[string]string, stdin string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"create-organizer", "--email", "a@x.example"}, testEnv(over, strings.NewReader(stdin), &stderr))
	return code, stderr.String()
}

func TestCreateOrganizerViaSocket(t *testing.T) {
	requireUnixSockets(t)
	for _, tc := range []struct {
		status, want int
	}{{http.StatusCreated, 0}, {http.StatusConflict, 1}, {http.StatusBadRequest, 1}} {
		sock, body := fakeSocket(t, tc.status)
		lock := filepath.Join(t.TempDir(), "l")
		held, err := acquireLock(lock)
		if err != nil {
			t.Fatal(err)
		}
		code, stderr := runCreate(t, map[string]string{"FIRSTLINE_ADMIN_SOCK": sock, "FIRSTLINE_LOCK_PATH": lock}, "long enough pw\n")
		_ = held.Close()
		if code != tc.want {
			t.Errorf("status %d: exit = %d; want %d (%s)", tc.status, code, tc.want, stderr)
		}
		if got, want := <-body, `POST /organizers {"email":"a@x.example","password":"long enough pw"}`; got != want {
			t.Errorf("request = %q; want %q", got, want)
		}
	}
}

func TestCreateOrganizerWritesDirectlyWithoutSocket(t *testing.T) {
	dir := shortDir(t)
	over := map[string]string{
		"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "missing.sock"),
		"FIRSTLINE_LOCK_PATH":  filepath.Join(dir, "l"),
		"FIRSTLINE_DB_PATH":    filepath.Join(dir, "db"),
	}
	if code, stderr := runCreate(t, over, "long enough pw"); code != 0 {
		t.Fatalf("exit = %d; want 0 (%s)", code, stderr)
	}
	if code, _ := runCreate(t, over, "long enough pw\n"); code != 1 {
		t.Errorf("duplicate: exit = %d; want 1", code)
	}
	if code, _ := runCreate(t, over, "short\n"); code != 1 {
		t.Errorf("short password: exit = %d; want 1", code)
	}
}

func TestCreateOrganizerFallsBackOnRefusedSocket(t *testing.T) {
	requireUnixSockets(t)
	dir := shortDir(t)
	sock := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	over := map[string]string{"FIRSTLINE_ADMIN_SOCK": sock, "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l"), "FIRSTLINE_DB_PATH": filepath.Join(dir, "db")}
	if code, stderr := runCreate(t, over, "long enough pw\n"); code != 0 {
		t.Fatalf("exit = %d; want 0 (%s)", code, stderr)
	}
}

func TestCreateOrganizerWithoutSocketNeedsTheLock(t *testing.T) {
	dir := shortDir(t)
	lock := filepath.Join(dir, "l")
	held, err := acquireLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	over := map[string]string{"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "missing.sock"), "FIRSTLINE_LOCK_PATH": lock, "FIRSTLINE_DB_PATH": filepath.Join(dir, "db")}
	if code, _ := runCreate(t, over, "long enough pw\n"); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !os.IsNotExist(err) {
		t.Errorf("database was created while the lock was held: %v", err)
	}
}

func TestCreateOrganizerOtherDialErrorWritesNothing(t *testing.T) {
	dir := shortDir(t)
	notDir := filepath.Join(dir, "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	over := map[string]string{"FIRSTLINE_ADMIN_SOCK": filepath.Join(notDir, "admin.sock"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l"), "FIRSTLINE_DB_PATH": filepath.Join(dir, "db")}
	if code, _ := runCreate(t, over, "long enough pw\n"); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !os.IsNotExist(err) {
		t.Errorf("database was created after a dial error: %v", err)
	}
}

func TestCreateOrganizerRejectsBadArguments(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"create-organizer", "--bogus"}, testEnv(nil, strings.NewReader(""), &stderr)); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
}

func TestReadPassword(t *testing.T) {
	for in, want := range map[string]string{"pw\n": "pw", "pw\r\n": "pw", "pw": "pw", "a b\nrest\n": "a b"} {
		got, err := readPassword(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readPassword(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := readPassword(iotest.ErrReader(errors.New("tty gone"))); err == nil {
		t.Error("readPassword with a failing reader succeeded")
	}
}

func TestCreateOrganizerFailsWhenStdinFails(t *testing.T) {
	var stderr bytes.Buffer
	e := testEnv(nil, iotest.ErrReader(errors.New("tty gone")), &stderr)
	if code := run(context.Background(), []string{"create-organizer", "--email", "a@x.example"}, e); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
}

var badPasswords = map[string]string{
	"5000 runes":    strings.Repeat("あ", 5000) + "\n",
	"invalid UTF-8": "long enough \xff\xfe\n",
}

func TestCreateOrganizerRejectsBadPasswordOnTheDirectPath(t *testing.T) {
	for name, pw := range badPasswords {
		t.Run(name, func(t *testing.T) {
			dir := shortDir(t)
			over := map[string]string{
				"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "missing.sock"),
				"FIRSTLINE_LOCK_PATH":  filepath.Join(dir, "l"),
				"FIRSTLINE_DB_PATH":    filepath.Join(dir, "db"),
			}
			if code, _ := runCreate(t, over, pw); code != 1 {
				t.Errorf("exit = %d; want 1", code)
			}
			if _, err := os.Stat(filepath.Join(dir, "db")); !os.IsNotExist(err) {
				t.Errorf("database was written: %v", err)
			}
		})
	}
}

func TestCreateOrganizerRejectsBadPasswordOnTheSocketPath(t *testing.T) {
	requireUnixSockets(t)
	for name, pw := range badPasswords {
		t.Run(name, func(t *testing.T) {
			sock, body := fakeSocket(t, http.StatusCreated)
			if code, _ := runCreate(t, map[string]string{"FIRSTLINE_ADMIN_SOCK": sock}, pw); code != 1 {
				t.Errorf("exit = %d; want 1", code)
			}
			select {
			case got := <-body:
				t.Errorf("serve was asked to create %q", got)
			default:
			}
		})
	}
}

func TestReadPasswordCapsTheLine(t *testing.T) {
	longest := strings.Repeat("a", maxPasswordLine)
	if got, err := readPassword(strings.NewReader(longest + "\r\n")); err != nil || got != longest {
		t.Errorf("a %d-byte line: %d bytes, %v; want it whole", maxPasswordLine, len(got), err)
	}
	for _, in := range []string{longest + "a", longest + "a\n", strings.Repeat("a", 1<<20)} {
		if got, err := readPassword(strings.NewReader(in)); !errors.Is(err, domain.ErrBadInput) {
			t.Errorf("a %d-byte line: %d bytes, %v; want ErrBadInput", len(in), len(got), err)
		}
	}
}

func TestOrganizerHandlerTakesTheLongestPasswordInJSON(t *testing.T) {
	// json.Marshal writes "<" as <, six bytes for one rune.
	pw := strings.Repeat("<", 1024)
	body, err := json.Marshal(organizerInput{Email: "a@x.example", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	h := organizerHandler(creatorFunc(func(_ context.Context, _, p string) error { got = p; return nil }), reportTo(&recordingReporter{}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/organizers", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated || got != pw {
		t.Errorf("status = %d, password %d bytes; want 201 and the whole password", rr.Code, len(got))
	}
}
