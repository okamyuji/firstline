//go:build e2e

// Package e2e drives the real firstline binary over HTTPS, the admin listener
// and the admin socket. scripts/e2e.sh runs it, in a container where the host
// forbids binding unix sockets.
package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	organizerEmail = "organizer@example.com"
	buyerEmail     = "buyer@example.com"
	// datetimeLayout is the layout of the event form's datetime-local inputs.
	datetimeLayout = "2006-01-02T15:04"
)

var jst = time.FixedZone("JST", 9*60*60)

func TestPurchaseLookupAndRestart(t *testing.T) {
	bin := buildBinary(t)
	env, cert := serveEnv(t)
	password := rand.Text()

	srv := start(t, bin, env)
	createOrganizer(t, bin, env, password)

	org := newClient(t, cert, env["FIRSTLINE_ADDR"])
	org.login(t, password)
	eventID := org.createEvent(t)

	buyer := newClient(t, cert, env["FIRSTLINE_ADDR"])
	statusPath, token := buyer.hold(t, eventID)
	body := buyer.waitForState(t, statusPath, "Purchased")
	assertTickets(t, body, "1-2")
	assertTickets(t, buyer.lookup(t, token), "1-2")
	org.assertSold(t, eventID, "2")
	checkHealth(t, env["FIRSTLINE_ADMIN_ADDR"])

	srv.stop(t)

	start(t, bin, env)
	again := newClient(t, cert, env["FIRSTLINE_ADDR"])
	again.login(t, password)
	again.assertSold(t, eventID, "2")
	assertTickets(t, buyer.lookup(t, token), "1-2")
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "firstline")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/okamyuji/firstline/cmd/firstline").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// serveEnv returns the variables for a serve on free local ports. The directory
// comes from os.MkdirTemp because macOS caps a unix socket path at 104 bytes.
func serveEnv(t *testing.T) (map[string]string, *x509.Certificate) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	cert := writeCert(t, certFile, keyFile)
	return map[string]string{
		"FIRSTLINE_DB_PATH": filepath.Join(dir, "db"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l"),
		"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "admin.sock"), "FIRSTLINE_TLS_CERT": certFile, "FIRSTLINE_TLS_KEY": keyFile,
		"FIRSTLINE_ADDR": freeAddr(t), "FIRSTLINE_ADMIN_ADDR": freeAddr(t), "FIRSTLINE_METRICS": "none",
		"FIRSTLINE_TOKEN_KEY": rand.Text() + rand.Text(), "FIRSTLINE_BASE_URL": "https://localhost",
		"FIRSTLINE_FAKE_PAYMENT_DELAY": "1s", "FIRSTLINE_FAKE_PAYMENT_OUTCOME": "complete",
	}, cert
}

func writeCert(t *testing.T, certFile, keyFile string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func environ(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

type server struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
	// out is written by one exec goroutine (stdout and stderr share it) and
	// read only after exited is closed.
	out bytes.Buffer
}

// start runs serve and returns once the admin listener answers /healthz and the
// admin socket exists, the last step of startup.
func start(t *testing.T, bin string, env map[string]string) *server {
	t.Helper()
	s := &server{cmd: exec.Command(bin, "serve"), exited: make(chan struct{})}
	s.cmd.Env = environ(env)
	s.cmd.Stdout, s.cmd.Stderr = &s.out, &s.out
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.waitErr = s.cmd.Wait(); close(s.exited) }()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
		if t.Failed() {
			t.Logf("serve output:\n%s", s.out.String())
		}
	})

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-s.exited:
			t.Fatalf("serve exited during startup: %v\n%s", s.waitErr, s.out.String())
		default:
		}
		if healthy(env["FIRSTLINE_ADMIN_ADDR"]) && exists(env["FIRSTLINE_ADMIN_SOCK"]) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("serve did not become ready within 30s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func healthy(adminAddr string) bool {
	resp, err := http.Get("http://" + adminAddr + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stop sends SIGTERM and requires exit status 0 within systemd's TimeoutStopSec.
func (s *server) stop(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.exited:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not exit within 30s of SIGTERM")
	}
	if s.waitErr != nil {
		t.Fatalf("serve after SIGTERM: %v\n%s", s.waitErr, s.out.String())
	}
}

// createOrganizer runs while serve holds the lock, so exit 0 means the request
// went through the admin socket: the direct path would fail on the lock.
func createOrganizer(t *testing.T, bin string, env map[string]string, password string) {
	t.Helper()
	cmd := exec.Command(bin, "create-organizer", "--email", organizerEmail)
	cmd.Env = environ(env)
	cmd.Stdin = strings.NewReader(password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create-organizer: %v\n%s", err, out)
	}
}

type client struct {
	http *http.Client
	base string
}

func newClient(t *testing.T, cert *x509.Certificate, addr string) *client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &client{
		http: &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"},
		}},
		base: "https://" + addr,
	}
}

// do sends the request with Accept-Language en, follows redirects, and returns
// the final URL and body. Any status but 200 fails the test.
func (c *client) do(t *testing.T, method, path string, form url.Values) (*url.URL, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Language", "en")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d\n%s", method, path, resp.StatusCode, b)
	}
	return resp.Request.URL, string(b)
}

func (c *client) login(t *testing.T, password string) {
	t.Helper()
	u, body := c.do(t, http.MethodPost, "/admin/login", url.Values{"email": {organizerEmail}, "password": {password}})
	if u.Path != "/admin/events" {
		t.Fatalf("login ended on %s; want /admin/events\n%s", u.Path, body)
	}
}

// createEvent returns the new event's ID from the sales page it redirects to.
func (c *client) createEvent(t *testing.T) string {
	t.Helper()
	now := time.Now().In(jst)
	u, body := c.do(t, http.MethodPost, "/admin/events", url.Values{
		"title_ja": {"E2E公演"}, "title_en": {"E2E show"},
		"starts_at": {now.Add(24 * time.Hour).Format(datetimeLayout)}, "ends_at": {now.Add(26 * time.Hour).Format(datetimeLayout)},
		"sale_starts_at": {now.Add(-time.Hour).Format(datetimeLayout)}, "capacity": {"10"}, "price": {"3000"},
	})
	id, ok := strings.CutPrefix(u.Path, "/admin/events/")
	if !ok || id == "" || id == "new" {
		t.Fatalf("event creation ended on %s\n%s", u.Path, body)
	}
	return id
}

// hold buys two tickets and returns the status page path with its query and the lookup token.
func (c *client) hold(t *testing.T, eventID string) (string, string) {
	t.Helper()
	u, body := c.do(t, http.MethodPost, "/events/"+url.PathEscape(eventID)+"/purchases",
		url.Values{"email": {buyerEmail}, "quantity": {"2"}})
	token := u.Query().Get("token")
	if !strings.HasPrefix(u.Path, "/purchases/") || token == "" {
		t.Fatalf("purchase ended on %s\n%s", u, body)
	}
	return u.RequestURI(), token
}

func (c *client) waitForState(t *testing.T, path, state string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, body := c.do(t, http.MethodGet, path, nil)
		if strings.Contains(body, "<dd>"+state+"</dd>") {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("purchase never reached %q:\n%s", state, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (c *client) lookup(t *testing.T, token string) string {
	t.Helper()
	u, body := c.do(t, http.MethodPost, "/lookup", url.Values{"email": {buyerEmail}, "token": {token}})
	if !strings.HasPrefix(u.Path, "/purchases/") {
		t.Fatalf("lookup ended on %s\n%s", u.Path, body)
	}
	if !strings.Contains(body, "<dd>Purchased</dd>") {
		t.Errorf("lookup page lacks the purchased state:\n%s", body)
	}
	return body
}

func (c *client) assertSold(t *testing.T, eventID, sold string) {
	t.Helper()
	_, body := c.do(t, http.MethodGet, "/admin/events/"+url.PathEscape(eventID), nil)
	if !strings.Contains(body, `<dd id="sold">`+sold+`</dd>`) {
		t.Errorf("sales page does not show %s sold:\n%s", sold, body)
	}
}

func assertTickets(t *testing.T, body, tickets string) {
	t.Helper()
	if !strings.Contains(body, "<dt>Reference number</dt><dd>"+tickets+"</dd>") {
		t.Errorf("status page lacks reference numbers %s:\n%s", tickets, body)
	}
}

func checkHealth(t *testing.T, adminAddr string) {
	t.Helper()
	resp, err := http.Get("http://" + adminAddr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got map[string]any
	if err := json.UnmarshalRead(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"commit_wait_p99_ms", "db_bytes", "dropped_total", "last_reconcile_at", "queue_cap", "queue_len",
		"queue_usage_avg_5m", "sync_items_per_sec_max_5m", "tls_cert_days_left", "wal_bytes",
	}
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, want) {
		t.Errorf("/healthz keys = %v; want %v", keys, want)
	}
	if got["last_reconcile_at"] == nil {
		t.Error("/healthz last_reconcile_at is null after the startup reconcile")
	}
	if days, _ := got["tls_cert_days_left"].(float64); days < 29 || days > 30 {
		t.Errorf("/healthz tls_cert_days_left = %v; want 29 or 30 for a 30-day certificate", got["tls_cert_days_left"])
	}
}
