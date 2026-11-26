package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// serveEnv returns the variables for a serve on free local ports with a
// self-signed certificate valid for 30 days.
func serveEnv(t *testing.T) (map[string]string, *x509.Certificate) {
	t.Helper()
	dir := shortDir(t)
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	cert := writeCert(t, certFile, keyFile, 1, time.Now().Add(30*24*time.Hour+time.Hour))
	return map[string]string{
		"FIRSTLINE_DB_PATH": filepath.Join(dir, "db"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l"),
		"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "admin.sock"), "FIRSTLINE_TLS_CERT": certFile, "FIRSTLINE_TLS_KEY": keyFile,
		"FIRSTLINE_ADDR": freeAddr(t), "FIRSTLINE_ADMIN_ADDR": freeAddr(t), "FIRSTLINE_METRICS": "none",
	}, cert
}

func waitHealthy(t *testing.T, adminAddr string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v", err)
		default:
		}
		resp, err := http.Get("http://" + adminAddr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never became healthy: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func status(t *testing.T, c *http.Client, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestServeEndToEnd(t *testing.T) {
	requireUnixSockets(t)
	over, cert := serveEnv(t)
	term := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- serveWith(testEnv(over, nil, io.Discard), term, make(chan os.Signal)) }()
	waitHealthy(t, over["FIRSTLINE_ADMIN_ADDR"], done)
	checkRoutes(t, over, cert)
	checkHealth(t, "http://"+over["FIRSTLINE_ADMIN_ADDR"])
	checkSocket(t, over)

	term <- syscall.SIGTERM
	if err := <-done; err != nil {
		t.Fatalf("serve after SIGTERM: %v", err)
	}
}

// checkRoutes asserts /organizers and /healthz are absent where the spec says.
func checkRoutes(t *testing.T, over map[string]string, cert *x509.Certificate) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	https := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	base, admin := "https://"+over["FIRSTLINE_ADDR"], "http://"+over["FIRSTLINE_ADMIN_ADDR"]
	if got := status(t, https, http.MethodGet, base+"/"); got != http.StatusOK {
		t.Errorf("GET / = %d; want 200", got)
	}
	if got := status(t, https, http.MethodPost, base+"/organizers"); got != http.StatusNotFound {
		t.Errorf("HTTPS POST /organizers = %d; want 404", got)
	}
	if got := status(t, http.DefaultClient, http.MethodPost, admin+"/organizers"); got != http.StatusNotFound {
		t.Errorf("admin POST /organizers = %d; want 404", got)
	}
	if got := status(t, https, http.MethodGet, base+"/healthz"); got != http.StatusNotFound {
		t.Errorf("HTTPS GET /healthz = %d; want 404", got)
	}
}

// checkSocket covers create-organizer through the socket and the lock serve holds.
func checkSocket(t *testing.T, over map[string]string) {
	t.Helper()
	fi, err := os.Stat(over["FIRSTLINE_ADMIN_SOCK"])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("admin socket mode = %v; want 0600", fi.Mode().Perm())
	}

	if code, stderr := runCreate(t, over, "long enough pw\n"); code != 0 {
		t.Errorf("create-organizer via socket: exit %d (%s)", code, stderr)
	}
	if code, _ := runCreate(t, over, "long enough pw\n"); code != 1 {
		t.Errorf("duplicate create-organizer via socket: exit %d; want 1", code)
	}
	var stderr strings.Builder
	if code := run(context.Background(), []string{"serve"}, testEnv(over, nil, &stderr)); code != 1 || !strings.Contains(stderr.String(), "lock") {
		t.Errorf("second serve: exit %d, %q; want 1 and a lock error", code, stderr.String())
	}
}

func checkHealth(t *testing.T, admin string) {
	t.Helper()
	resp, err := http.Get(admin + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		QueueCap        int        `json:"queue_cap"`
		TLSCertDaysLeft int        `json:"tls_cert_days_left"`
		LastReconcileAt *time.Time `json:"last_reconcile_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if h.QueueCap != 8192 || h.TLSCertDaysLeft != 30 || h.LastReconcileAt == nil {
		t.Errorf("healthz = %+v; want queue_cap 8192, 30 days left and a startup reconcile time", h)
	}
}

func TestServeFailsWithoutCertificate(t *testing.T) {
	over, _ := serveEnv(t)
	over["FIRSTLINE_TLS_CERT"] = filepath.Join(t.TempDir(), "missing.pem")
	if err := serveWith(testEnv(over, nil, io.Discard), make(chan os.Signal), make(chan os.Signal)); err == nil {
		t.Fatal("serve without a certificate succeeded")
	}
}

func TestServeFailsWhenStoreCannotOpen(t *testing.T) {
	over, _ := serveEnv(t)
	over["FIRSTLINE_DB_PATH"] = filepath.Join(t.TempDir(), "missing", "db")
	if err := serveWith(testEnv(over, nil, io.Discard), make(chan os.Signal), make(chan os.Signal)); err == nil {
		t.Fatal("serve with an unopenable store succeeded")
	}
}

func TestServeKeepsARegularFileAtTheSocketPath(t *testing.T) {
	over, _ := serveEnv(t)
	if err := os.WriteFile(over["FIRSTLINE_ADMIN_SOCK"], []byte("precious data"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := serveWith(testEnv(over, nil, io.Discard), make(chan os.Signal), make(chan os.Signal))
	if err == nil || !strings.Contains(err.Error(), "FIRSTLINE_ADMIN_SOCK") {
		t.Errorf("serve = %v; want an error naming FIRSTLINE_ADMIN_SOCK", err)
	}
	if b, _ := os.ReadFile(over["FIRSTLINE_ADMIN_SOCK"]); string(b) != "precious data" {
		t.Errorf("the file at the socket path now holds %q", b)
	}
	if _, err := os.Stat(over["FIRSTLINE_DB_PATH"]); !os.IsNotExist(err) {
		t.Errorf("the database was opened before the check: %v", err)
	}
}

func TestStartUnixKeepsARegularFile(t *testing.T) {
	path := filepath.Join(shortDir(t), "admin.sock")
	if err := os.WriteFile(path, []byte("precious data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startUnix(newServer(nil, slog.New(slog.DiscardHandler)), path)(make(chan error, 1)); err == nil {
		t.Error("startUnix over a regular file succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != "precious data" {
		t.Errorf("the file at the socket path now holds %q", b)
	}
}

func TestRemoveStaleSocketReportsStatErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(filepath.Join(file, "admin.sock")); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("removeStaleSocket under a file = %v; want ENOTDIR", err)
	}
	if err := removeStaleSocket(filepath.Join(t.TempDir(), "none.sock")); err != nil {
		t.Errorf("removeStaleSocket of a missing path = %v; want nil", err)
	}
}

// openedWiring opens the real parts of serve against a temporary SQLite.
func openedWiring(t *testing.T, metrics string) (*wiring, *recordingReporter) {
	t.Helper()
	over, _ := serveEnv(t)
	over["FIRSTLINE_METRICS"] = metrics
	cfg, err := LoadConfig(envOf(over))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := loadCert(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		t.Fatal(err)
	}
	rep := &recordingReporter{}
	wr := newWiring(cfg, cs, rep, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err := wr.open(t.Context()); err != nil {
		t.Fatal(err)
	}
	return wr, rep
}

func TestWiredJobsRunAgainstTheRealService(t *testing.T) {
	wr, rep := openedWiring(t, "none")
	l := wr.lifecycle()
	if err := l.rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := wr.st.Write.ExecContext(t.Context(), "INSERT INTO access_logs (method, path, status, duration_ms, at) VALUES ('GET', '/old', 200, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	for _, j := range l.jobs() {
		j.run(t.Context())
	}
	if !wr.svc.LastReconcileAt().After(time.Now().Add(-time.Minute)) {
		t.Errorf("last reconcile %v; want one from the reconcile job", wr.svc.LastReconcileAt())
	}
	l.stopPayment()
	l.stopWriter()
	var old int
	if err := wr.st.Read.QueryRowContext(t.Context(), "SELECT count(*) FROM access_logs WHERE path = '/old'").Scan(&old); err != nil || old != 0 {
		t.Errorf("old access log rows %d (%v); want the daily purge to delete them", old, err)
	}
	if err := l.closeStore(); err != nil {
		t.Fatal(err)
	}
	if len(rep.errs) != 0 || len(l.loops) != 0 {
		t.Errorf("errors %v, loops %d; want none with metrics off", rep.errs, len(l.loops))
	}
}

func TestEMFLoopRunsOnlyWhenMetricsAreEMF(t *testing.T) {
	wr, _ := openedWiring(t, "emf")
	l := wr.lifecycle()
	if len(l.loops) != 1 {
		t.Fatalf("loops = %d; want 1 with metrics emf", len(l.loops))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l.loops[0](ctx)
	l.stopPayment()
	l.stopWriter()
	_ = l.closeStore()
}
