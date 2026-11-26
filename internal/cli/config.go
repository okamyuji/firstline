package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/observe/logreporter"
	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/mysql"
	"github.com/okamyuji/firstline/internal/store/postgres"
	"github.com/okamyuji/firstline/internal/store/sqlite"
)

const minTokenKeyBytes = 32

// Config is the environment of every subcommand, as the settings table lists it.
type Config struct {
	Store, DBPath, DSN, Inventory string
	Addr, TLSCert, TLSKey         string
	TokenKey                      []byte
	BaseURL                       string
	ErrorReporters                []string
	Metrics, EMFAddr, AdminAddr   string
	AdminSock, LockPath           string
	HoldRatePerMin                int
	FakePaymentDelay              time.Duration
	FakePaymentOutcome            fake.Outcome
}

// The allowed values of each enumerated setting, keyed by the text in the
// environment, so validation and construction share one table.
var (
	storeOpeners = map[string]func(context.Context, string) (*store.Store, error){
		"sqlite": sqlite.Open, "postgres": postgres.Open, "mysql": mysql.Open,
	}
	inventories = map[string]func() port.Inventory{"memory": inventory.NewMemory, "db": inventory.NewDB}
	reporters   = map[string]func(*slog.Logger) port.ErrorReporter{"log": logreporter.New}
	outcomes    = map[string]fake.Outcome{"complete": fake.Complete, "fail": fake.Fail}
	metrics     = map[string]bool{"emf": true, "none": false}
)

// LoadConfig reads the FIRSTLINE_* variables through getenv and reports every
// invalid one at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := func(k, def string) string { return cmp.Or(getenv(k), def) }
	c := Config{
		Store: env("FIRSTLINE_STORE", "sqlite"), DBPath: env("FIRSTLINE_DB_PATH", "/var/lib/firstline/firstline.db"),
		DSN: getenv("FIRSTLINE_DSN"), Inventory: env("FIRSTLINE_INVENTORY", "memory"), Addr: env("FIRSTLINE_ADDR", ":443"),
		TLSCert:  env("FIRSTLINE_TLS_CERT", "/etc/firstline/tls/fullchain.pem"),
		TLSKey:   env("FIRSTLINE_TLS_KEY", "/etc/firstline/tls/privkey.pem"),
		TokenKey: []byte(getenv("FIRSTLINE_TOKEN_KEY")), BaseURL: getenv("FIRSTLINE_BASE_URL"),
		ErrorReporters: strings.Split(env("FIRSTLINE_ERROR_REPORTER", "log"), ","),
		Metrics:        env("FIRSTLINE_METRICS", "emf"), EMFAddr: env("FIRSTLINE_EMF_ADDR", "127.0.0.1:25888"),
		AdminAddr: env("FIRSTLINE_ADMIN_ADDR", "127.0.0.1:9090"), AdminSock: env("FIRSTLINE_ADMIN_SOCK", "/run/firstline/admin.sock"),
		LockPath: env("FIRSTLINE_LOCK_PATH", "/var/lib/firstline/firstline.lock"),
	}
	outcome := env("FIRSTLINE_FAKE_PAYMENT_OUTCOME", "complete")
	c.FakePaymentOutcome = outcomes[outcome]
	var rateErr, delayErr error
	c.HoldRatePerMin, rateErr = nonNegative("FIRSTLINE_HOLD_RATE_PER_MIN", env("FIRSTLINE_HOLD_RATE_PER_MIN", "10"))
	c.FakePaymentDelay, delayErr = time.ParseDuration(env("FIRSTLINE_FAKE_PAYMENT_DELAY", "3s"))
	errs := []error{
		oneOf("FIRSTLINE_STORE", c.Store, storeOpeners),
		oneOf("FIRSTLINE_INVENTORY", c.Inventory, inventories),
		oneOf("FIRSTLINE_METRICS", c.Metrics, metrics),
		oneOf("FIRSTLINE_FAKE_PAYMENT_OUTCOME", outcome, outcomes),
		tokenKey(c.TokenKey), baseURL(c.BaseURL), rateErr,
	}
	for _, r := range c.ErrorReporters {
		errs = append(errs, oneOf("FIRSTLINE_ERROR_REPORTER", r, reporters))
	}
	if delayErr != nil {
		errs = append(errs, fmt.Errorf("FIRSTLINE_FAKE_PAYMENT_DELAY: %w", delayErr))
	}
	return c, errors.Join(errs...)
}

func oneOf[V any](name, v string, allowed map[string]V) error {
	if _, ok := allowed[v]; !ok {
		return fmt.Errorf("%s: unknown value %q", name, v)
	}
	return nil
}

func nonNegative(name, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: %q is not a non-negative integer", name, v)
	}
	return n, nil
}

// tokenKey never echoes the key itself.
func tokenKey(k []byte) error {
	if len(k) < minTokenKeyBytes {
		return fmt.Errorf("FIRSTLINE_TOKEN_KEY: %d bytes; at least %d are required", len(k), minTokenKeyBytes)
	}
	return nil
}

func baseURL(u string) error {
	if !strings.HasPrefix(u, "https://") || strings.HasSuffix(u, "/") {
		return fmt.Errorf("FIRSTLINE_BASE_URL: %q must start with https:// and not end with /", u)
	}
	return nil
}

// dbTarget is what the store's Open takes: a file path for SQLite, else the DSN.
func (c Config) dbTarget() string {
	if c.Store == "sqlite" {
		return c.DBPath
	}
	return c.DSN
}

// sqliteFile is the SQLite file whose sizes /healthz reports, or "" for a server store.
func (c Config) sqliteFile() string {
	if c.Store == "sqlite" {
		return c.DBPath
	}
	return ""
}

// fanout sends each report to every configured reporter.
type fanout []port.ErrorReporter

func (f fanout) Report(ctx context.Context, err error, attrs ...slog.Attr) {
	for _, r := range f {
		r.Report(ctx, err, attrs...)
	}
}

// newReporter takes names LoadConfig has validated.
func newReporter(names []string, log *slog.Logger) port.ErrorReporter {
	f := make(fanout, 0, len(names))
	for _, n := range names {
		f = append(f, reporters[n](log))
	}
	return f
}

// openStore opens the configured store and applies the idempotent schema.
func openStore(ctx context.Context, c Config) (*store.Store, error) {
	st, err := storeOpeners[c.Store](ctx, c.dbTarget())
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate schema: %w", err), closeStore(st))
	}
	return st, nil
}

// closeStore closes ReadDB, then WriteDB, as shutdown step 4 orders.
func closeStore(st *store.Store) error {
	return errors.Join(st.Read.Close(), st.Write.Close())
}
