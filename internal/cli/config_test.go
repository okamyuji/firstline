package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/payment/fake"
)

var validKey = strings.Repeat("k", 32)

// envOf returns a getenv over the required variables plus overrides.
func envOf(over map[string]string) func(string) string {
	m := map[string]string{"FIRSTLINE_TOKEN_KEY": validKey, "FIRSTLINE_BASE_URL": "https://tickets.example"}
	maps.Copy(m, over)
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Store: "sqlite", DBPath: "/var/lib/firstline/firstline.db", Inventory: "memory", Addr: ":443",
		TLSCert: "/etc/firstline/tls/fullchain.pem", TLSKey: "/etc/firstline/tls/privkey.pem",
		TokenKey: []byte(validKey), BaseURL: "https://tickets.example", ErrorReporters: []string{"log"},
		Metrics: "emf", EMFAddr: "127.0.0.1:25888", AdminAddr: "127.0.0.1:9090",
		AdminSock: "/run/firstline/admin.sock", LockPath: "/var/lib/firstline/firstline.lock",
		HoldRatePerMin: 10, FakePaymentDelay: 3 * time.Second, FakePaymentOutcome: fake.Complete,
	}
	assertConfig(t, c, want)
}

func TestLoadConfigReadsEveryVariable(t *testing.T) {
	c, err := LoadConfig(envOf(map[string]string{
		"FIRSTLINE_STORE": "postgres", "FIRSTLINE_DB_PATH": "/d.db", "FIRSTLINE_DSN": "postgres://h/db",
		"FIRSTLINE_INVENTORY": "db", "FIRSTLINE_ADDR": ":8443",
		"FIRSTLINE_TLS_CERT": "/c.pem", "FIRSTLINE_TLS_KEY": "/k.pem",
		"FIRSTLINE_TOKEN_KEY": validKey + "x", "FIRSTLINE_BASE_URL": "https://b.example",
		"FIRSTLINE_ERROR_REPORTER": "log,log", "FIRSTLINE_METRICS": "none", "FIRSTLINE_EMF_ADDR": "1.2.3.4:1",
		"FIRSTLINE_ADMIN_ADDR": "[::1]:9", "FIRSTLINE_ADMIN_SOCK": "/s.sock", "FIRSTLINE_LOCK_PATH": "/l.lock",
		"FIRSTLINE_HOLD_RATE_PER_MIN": "0", "FIRSTLINE_FAKE_PAYMENT_DELAY": "250ms",
		"FIRSTLINE_FAKE_PAYMENT_OUTCOME": "fail",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertConfig(t, c, Config{
		Store: "postgres", DBPath: "/d.db", DSN: "postgres://h/db", Inventory: "db", Addr: ":8443",
		TLSCert: "/c.pem", TLSKey: "/k.pem", TokenKey: []byte(validKey + "x"), BaseURL: "https://b.example",
		ErrorReporters: []string{"log", "log"}, Metrics: "none", EMFAddr: "1.2.3.4:1", AdminAddr: "[::1]:9",
		AdminSock: "/s.sock", LockPath: "/l.lock", HoldRatePerMin: 0,
		FakePaymentDelay: 250 * time.Millisecond, FakePaymentOutcome: fake.Fail,
	})
}

func assertConfig(t *testing.T, got, want Config) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config =\n%+v\nwant\n%+v", got, want)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"31-byte token key", "FIRSTLINE_TOKEN_KEY", validKey[:31]},
		{"missing token key", "FIRSTLINE_TOKEN_KEY", ""},
		{"empty base URL", "FIRSTLINE_BASE_URL", ""},
		{"http base URL", "FIRSTLINE_BASE_URL", "http://tickets.example"},
		{"base URL with trailing slash", "FIRSTLINE_BASE_URL", "https://tickets.example/"},
		{"unknown store", "FIRSTLINE_STORE", "oracle"},
		{"unknown inventory", "FIRSTLINE_INVENTORY", "redis"},
		{"unknown error reporter", "FIRSTLINE_ERROR_REPORTER", "log,sentry"},
		{"unknown metrics", "FIRSTLINE_METRICS", "prometheus"},
		{"non-numeric hold rate", "FIRSTLINE_HOLD_RATE_PER_MIN", "ten"},
		{"negative hold rate", "FIRSTLINE_HOLD_RATE_PER_MIN", "-1"},
		{"bad payment delay", "FIRSTLINE_FAKE_PAYMENT_DELAY", "soon"},
		{"unknown payment outcome", "FIRSTLINE_FAKE_PAYMENT_OUTCOME", "maybe"},
		{"address without port", "FIRSTLINE_ADDR", "443"},
		{"EMF address without port", "FIRSTLINE_EMF_ADDR", "127.0.0.1"},
		{"admin address without port", "FIRSTLINE_ADMIN_ADDR", "127.0.0.1"},
		{"admin address on every interface", "FIRSTLINE_ADMIN_ADDR", ":9090"},
		{"admin address on a public IP", "FIRSTLINE_ADMIN_ADDR", "0.0.0.0:9090"},
		{"admin address by name", "FIRSTLINE_ADMIN_ADDR", "example.com:9090"},
		{"socket path too long for sun_path", "FIRSTLINE_ADMIN_SOCK", "/" + strings.Repeat("s", 107)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(envOf(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Errorf("LoadConfig error = %v; want one naming %s", err, tc.key)
			}
		})
	}
}

func TestLoadConfigAcceptsLoopbackAdminAndTheLongestSocketPath(t *testing.T) {
	for _, over := range []map[string]string{
		{"FIRSTLINE_ADMIN_ADDR": "127.0.0.2:9090", "FIRSTLINE_ADMIN_SOCK": "/" + strings.Repeat("s", 106)},
		{"FIRSTLINE_ADMIN_ADDR": "[::1]:9090", "FIRSTLINE_ADDR": "[::]:443"},
	} {
		if _, err := LoadConfig(envOf(over)); err != nil {
			t.Errorf("LoadConfig(%v) = %v; want nil", over, err)
		}
	}
}

func TestLoadConfigAccepts32ByteKeyAndZeroRate(t *testing.T) {
	c, err := LoadConfig(envOf(map[string]string{"FIRSTLINE_TOKEN_KEY": validKey, "FIRSTLINE_HOLD_RATE_PER_MIN": "0"}))
	if err != nil || len(c.TokenKey) != 32 || c.HoldRatePerMin != 0 {
		t.Errorf("LoadConfig = %d-byte key, rate %d, %v; want 32, 0, nil", len(c.TokenKey), c.HoldRatePerMin, err)
	}
}

func TestErrorReporterSendsToEveryNamedReporter(t *testing.T) {
	c, err := LoadConfig(envOf(map[string]string{"FIRSTLINE_ERROR_REPORTER": "log,log"}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	newReporter(c.ErrorReporters, slog.New(slog.NewJSONHandler(&buf, nil))).Report(context.Background(), errors.New("boom"))
	if n := strings.Count(buf.String(), `"boom"`); n != 2 {
		t.Errorf("log lines naming the error = %d; want 2:\n%s", n, buf.String())
	}
}

func TestDBTarget(t *testing.T) {
	sqliteCfg := Config{Store: "sqlite", DBPath: "/p.db", DSN: "dsn"}
	pgCfg := Config{Store: "postgres", DBPath: "/p.db", DSN: "dsn"}
	if sqliteCfg.dbTarget() != "/p.db" || pgCfg.dbTarget() != "dsn" {
		t.Errorf("dbTarget = %q, %q; want /p.db, dsn", sqliteCfg.dbTarget(), pgCfg.dbTarget())
	}
	if sqliteCfg.sqliteFile() != "/p.db" || pgCfg.sqliteFile() != "" {
		t.Errorf("sqliteFile = %q, %q; want /p.db and none", sqliteCfg.sqliteFile(), pgCfg.sqliteFile())
	}
}

// The example file is copied to /etc/firstline/env as is, so it must start and
// hold every default it claims to show.
func TestEnvExampleLoadsAndShowsTheDefaults(t *testing.T) {
	b, err := os.ReadFile("../../deploy/firstline.env.example")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for line := range strings.Lines(string(b)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(k, "#") {
			m[k] = v
		}
	}
	got, err := LoadConfig(func(k string) string { return m[k] })
	if err != nil {
		t.Fatalf("LoadConfig(example) = %v", err)
	}
	defaults, err := LoadConfig(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	got.TokenKey, got.BaseURL = defaults.TokenKey, defaults.BaseURL
	assertConfig(t, got, defaults)
}
