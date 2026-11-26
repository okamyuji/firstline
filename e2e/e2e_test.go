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
	"database/sql"
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

	_ "modernc.org/sqlite"
)

const (
	organizerEmail = "organizer@example.com"
	buyerEmail     = "buyer@example.com"
	// datetimeLayout is the layout of the event form's datetime-local inputs.
	datetimeLayout = "2006-01-02T15:04"
)

var jst = time.FixedZone("JST", 9*60*60)

// fixture is a running serve with an organizer logged in and one event of
// capacity 10 on sale.
type fixture struct {
	bin, password, eventID string
	env                    map[string]string
	cert                   *x509.Certificate
	srv                    *server
	org                    *client
}

func setup(t *testing.T, paymentDelay string) *fixture {
	t.Helper()
	f := &fixture{bin: buildBinary(t), password: rand.Text()}
	f.env, f.cert = serveEnv(t, paymentDelay)
	f.srv = start(t, f.bin, f.env)
	createOrganizer(t, f.bin, f.env, f.password)
	f.org = f.newClient(t)
	f.org.login(t, f.password)
	f.eventID = f.org.createEvent(t)
	return f
}

func (f *fixture) newClient(t *testing.T) *client {
	t.Helper()
	return newClient(t, f.cert, f.env["FIRSTLINE_ADDR"])
}

func TestPurchaseLookupAndRestart(t *testing.T) {
	f := setup(t, "1s")

	buyer := f.newClient(t)
	statusPath, token := buyer.hold(t, f.eventID)
	body := buyer.waitForState(t, statusPath, "Purchased")
	assertTickets(t, body, "1-2")
	assertTickets(t, buyer.lookup(t, token, "Purchased"), "1-2")
	f.org.assertSales(t, f.eventID, map[string]string{"sold": "2", "remaining": "8"})
	checkHealth(t, f.env["FIRSTLINE_ADMIN_ADDR"])

	f.srv.stop(t)

	start(t, f.bin, f.env)
	again := f.newClient(t)
	again.login(t, f.password)
	again.assertSales(t, f.eventID, map[string]string{"sold": "2", "remaining": "8"})
	assertTickets(t, buyer.lookup(t, token, "Purchased"), "1-2")
}

// TestSIGHUPReloadsTheCertificateAndKeepsServing sends the signal certbot's
// deploy hook sends: without serve's handler Go would exit on it, and
// Restart=on-failure would not bring serve back.
func TestSIGHUPReloadsTheCertificateAndKeepsServing(t *testing.T) {
	env, _ := serveEnv(t, "1s")
	srv := start(t, buildBinary(t), env)
	if got := servedSerial(t, env["FIRSTLINE_ADDR"]); got != 1 {
		t.Fatalf("serial before the reload = %d; want 1", got)
	}
	writeCert(t, env["FIRSTLINE_TLS_CERT"], env["FIRSTLINE_TLS_KEY"], 2)
	if err := srv.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for servedSerial(t, env["FIRSTLINE_ADDR"]) != 2 {
		select {
		case <-srv.exited:
			t.Fatalf("serve exited on SIGHUP: %v\n%s", srv.waitErr, srv.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("serve still serves the old certificate 10s after SIGHUP")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy(env["FIRSTLINE_ADMIN_ADDR"]) {
		t.Error("serve stopped answering /healthz after SIGHUP")
	}
	srv.stop(t)
}

// servedSerial is the serial number of the certificate serve presents now.
func servedSerial(t *testing.T, addr string) int64 {
	t.Helper()
	// The test only reads the certificate; it does not trust it.
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

// TestLateCompletionIsRefunded covers a payment that completes after the hold
// expired: expired, then refunding, then refunded by the refund retry.
func TestLateCompletionIsRefunded(t *testing.T) {
	// Long enough to move the deadline and let the expiry job run first.
	f := setup(t, "6s")
	db := openDB(t, f.env["FIRSTLINE_DB_PATH"])

	buyer := f.newClient(t)
	statusPath, token := buyer.hold(t, f.eventID)
	purchaseID := strings.TrimPrefix(strings.SplitN(statusPath, "?", 2)[0], "/purchases/")
	// The test moves the deadline instead of the clock: the hold TTL is a fixed
	// 10 minutes and the binary reads time.Now. Two minutes back passes the
	// expiry job's one-minute grace.
	past := time.Now().Add(-2 * time.Minute).UnixMilli()
	if res, err := db.ExecContext(t.Context(), "UPDATE purchases SET expires_at = ? WHERE id = ? AND state = 'held'", past, purchaseID); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("moved the deadline of %d purchases; want 1 held purchase", n)
	}

	// The refund retry runs every 30 seconds, so refunded can take that long after refunding.
	states := watchStates(t, db, purchaseID, "refunded", 60*time.Second)
	// A 100ms poll can miss held (the expiry job runs every second) and refunding
	// (the refund retry may fire right after the completion). refunded is reached
	// only from refunding, and the single refund billing row below pins it.
	states = slices.DeleteFunc(states, func(s string) bool { return s == "held" || s == "refunding" })
	if want := []string{"expired", "refunded"}; !slices.Equal(states, want) {
		t.Errorf("states other than held and refunding = %v; want %v", states, want)
	}
	var refunds, charges int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(CASE WHEN kind = 'refund' THEN 1 END), COUNT(CASE WHEN kind = 'charge' THEN 1 END) FROM billing_logs WHERE purchase_id = ?",
		purchaseID).Scan(&refunds, &charges); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || charges != 0 {
		t.Errorf("billing rows: %d refund, %d charge; want 1 refund and no charge", refunds, charges)
	}
	f.org.assertSales(t, f.eventID, map[string]string{"held": "0", "sold": "0", "remaining": "10", "refunds": "1"})
	if body := buyer.lookup(t, token, "Refunded"); strings.Contains(body, "Reference number") {
		t.Errorf("a refunded purchase shows reference numbers:\n%s", body)
	}
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// watchStates polls the purchase's state until it reaches final and returns
// each distinct state seen, in order.
func watchStates(t *testing.T, db *sql.DB, purchaseID, final string, limit time.Duration) []string {
	t.Helper()
	var seen []string
	deadline := time.Now().Add(limit)
	for {
		var state string
		if err := db.QueryRowContext(t.Context(), "SELECT state FROM purchases WHERE id = ?", purchaseID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if len(seen) == 0 || seen[len(seen)-1] != state {
			seen = append(seen, state)
		}
		if state == final {
			return seen
		}
		if time.Now().After(deadline) {
			t.Fatalf("purchase did not reach %s within %v; states seen %v", final, limit, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
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
func serveEnv(t *testing.T, paymentDelay string) (map[string]string, *x509.Certificate) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	cert := writeCert(t, certFile, keyFile, 1)
	return map[string]string{
		"FIRSTLINE_DB_PATH": filepath.Join(dir, "db"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l"),
		"FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "admin.sock"), "FIRSTLINE_TLS_CERT": certFile, "FIRSTLINE_TLS_KEY": keyFile,
		"FIRSTLINE_ADDR": freeAddr(t), "FIRSTLINE_ADMIN_ADDR": freeAddr(t), "FIRSTLINE_METRICS": "none",
		"FIRSTLINE_TOKEN_KEY": rand.Text() + rand.Text(), "FIRSTLINE_BASE_URL": "https://localhost",
		"FIRSTLINE_FAKE_PAYMENT_DELAY": paymentDelay, "FIRSTLINE_FAKE_PAYMENT_OUTCOME": "complete",
	}, cert
}

func writeCert(t *testing.T, certFile, keyFile string, serial int64) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"},
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

func (c *client) lookup(t *testing.T, token, state string) string {
	t.Helper()
	u, body := c.do(t, http.MethodPost, "/lookup", url.Values{"email": {buyerEmail}, "token": {token}})
	if !strings.HasPrefix(u.Path, "/purchases/") {
		t.Fatalf("lookup ended on %s\n%s", u.Path, body)
	}
	if !strings.Contains(body, "<dd>"+state+"</dd>") {
		t.Errorf("lookup page lacks the state %q:\n%s", state, body)
	}
	return body
}

// assertSales checks the sales page's counters by their element IDs.
func (c *client) assertSales(t *testing.T, eventID string, want map[string]string) {
	t.Helper()
	_, body := c.do(t, http.MethodGet, "/admin/events/"+url.PathEscape(eventID), nil)
	for id, n := range want {
		if !strings.Contains(body, `<dd id="`+id+`">`+n+`</dd>`) {
			t.Errorf("sales page does not show %s = %s:\n%s", id, n, body)
		}
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
