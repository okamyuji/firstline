package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/okamyuji/firstline/internal/migrate"
	"github.com/okamyuji/firstline/internal/store"
)

func migrateCmd(ctx context.Context, e env, cfg Config, args []string) int {
	fs := flag.NewFlagSet("migrate-data", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	from := fs.String("from", "", "source store: sqlite:<path>, postgres:<dsn> or mysql:<dsn>")
	to := fs.String("to", "", "empty target store in the same form")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	return exitCode(e.stderr, "migrate-data", migrateData(ctx, cfg.LockPath, *from, *to))
}

func migrateData(ctx context.Context, lockPath, from, to string) (err error) {
	lock, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	src, err := openSpec(ctx, from)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { err = errors.Join(err, closeStore(src)) }()
	dst, err := openSpec(ctx, to)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer func() { err = errors.Join(err, closeStore(dst)) }()
	if err := dst.Migrate(ctx); err != nil {
		return fmt.Errorf("create target schema: %w", err)
	}
	return migrate.Run(ctx, src, dst)
}

// openSpec opens "<store>:<path or DSN>". Errors name only the store kind, as
// the DSN can carry a password.
func openSpec(ctx context.Context, spec string) (*store.Store, error) {
	kind, target, _ := strings.Cut(spec, ":")
	open, ok := storeOpeners[kind]
	if !ok {
		return nil, fmt.Errorf("store kind %q: want sqlite:<path>, postgres:<dsn> or mysql:<dsn>", kind)
	}
	return open(ctx, target)
}
