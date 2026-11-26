package cli

import (
	"context"
	"errors"
	"flag"
	"time"

	"github.com/okamyuji/firstline/internal/payment/fake"
)

const refundAttempts = 3

type reconciler interface {
	Reconcile(ctx context.Context, since time.Time) (int, error)
	RetryRefunds(ctx context.Context) (int, error)
}

func reconcileCmd(ctx context.Context, e env, cfg Config, args []string) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	sinceText := fs.String("since", "", "RFC 3339 time to reconcile completed payments from")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	since, err := time.Parse(time.RFC3339, *sinceText)
	if err != nil {
		return exitCode(e.stderr, "reconcile", err)
	}
	pay := fake.New(time.Now, time.After, cfg.FakePaymentDelay, cfg.FakePaymentOutcome)
	defer pay.Stop()
	d, err := openDirect(ctx, cfg, pay, newReporter(cfg.ErrorReporters, e.log), e.log)
	if err != nil {
		return exitCode(e.stderr, "reconcile", err)
	}
	return exitCode(e.stderr, "reconcile", errors.Join(reconcileAndRefund(ctx, d.svc, since), d.close()))
}

// reconcileAndRefund finishes the refunds the reconcile started, so the
// subcommand ends with no refunding rows or reports why.
func reconcileAndRefund(ctx context.Context, r reconciler, since time.Time) error {
	if _, err := r.Reconcile(ctx, since); err != nil {
		return err
	}
	return retry(refundAttempts, func() error { return countless(r.RetryRefunds(ctx)) })
}

// retry calls f up to n times and returns the last error, or nil at the first success.
func retry(n int, f func() error) (err error) {
	for range n {
		if err = f(); err == nil {
			return nil
		}
	}
	return err
}
