package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/port"
)

func TestRetryStopsAtFirstSuccessOrAfterNAttempts(t *testing.T) {
	for _, tc := range []struct {
		failures, wantCalls int
		wantErr             bool
	}{{0, 1, false}, {2, 3, false}, {3, 3, true}, {5, 3, true}} {
		calls := 0
		err := retry(refundAttempts, func() error {
			calls++
			if calls <= tc.failures {
				return errors.New("refund failed")
			}
			return nil
		})
		if calls != tc.wantCalls || (err != nil) != tc.wantErr {
			t.Errorf("%d failures: %d calls, err %v; want %d calls, error %v", tc.failures, calls, err, tc.wantCalls, tc.wantErr)
		}
	}
}

func reconcileEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{"FIRSTLINE_DB_PATH": filepath.Join(dir, "db"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l")}
}

func runReconcile(over map[string]string, args ...string) (int, string) {
	var stderr bytes.Buffer
	code := run(context.Background(), append([]string{"reconcile"}, args...), testEnv(over, strings.NewReader(""), &stderr))
	return code, stderr.String()
}

func TestReconcileSucceedsWithNothingToDo(t *testing.T) {
	if code, stderr := runReconcile(reconcileEnv(t), "--since", "2026-11-26T09:00:00+09:00"); code != 0 {
		t.Errorf("exit = %d; want 0 (%s)", code, stderr)
	}
}

func TestReconcileFailsWhenARefundCannotFinish(t *testing.T) {
	over := reconcileEnv(t)
	cfg, err := LoadConfig(envOf(over))
	if err != nil {
		t.Fatal(err)
	}
	pay := fake.New(time.Now, time.After, time.Hour, fake.Complete)
	defer pay.Stop()
	d, err := openDirect(t.Context(), cfg, pay, &recordingReporter{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// A completion for an unknown purchase leaves a refunding orphan; the new
	// process's fake payment does not know the payment, so the refund fails.
	_ = d.svc.Completed(t.Context(), port.Completion{PurchaseID: "ghost", PaymentID: "pay-ghost", Amount: 100, At: time.Now()})
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runReconcile(over, "--since", "2026-11-26T00:00:00Z"); code != 1 || !strings.Contains(stderr, "pay-ghost") {
		t.Errorf("exit = %d, %q; want 1 naming pay-ghost", code, stderr)
	}
}

func TestReconcileRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{{"--since", "yesterday"}, {}, {"--bogus"}} {
		if code, _ := runReconcile(reconcileEnv(t), args...); code != 1 {
			t.Errorf("reconcile %q: exit = %d; want 1", args, code)
		}
	}
}

func TestReconcileNeedsTheLock(t *testing.T) {
	over := reconcileEnv(t)
	held, err := acquireLock(over["FIRSTLINE_LOCK_PATH"])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if code, _ := runReconcile(over, "--since", "2026-11-26T00:00:00Z"); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
}

type failingReconciler struct{ reconcileErr error }

func (f failingReconciler) Reconcile(context.Context, time.Time) (int, error) {
	return 0, f.reconcileErr
}
func (f failingReconciler) RetryRefunds(context.Context) (int, error) {
	return 0, errors.New("must not run after a failed reconcile")
}

func TestReconcileErrorSkipsRefunds(t *testing.T) {
	err := reconcileAndRefund(context.Background(), failingReconciler{errors.New("provider down")}, time.Time{})
	if err == nil || err.Error() != "provider down" {
		t.Errorf("err = %v; want provider down", err)
	}
}
