// Package cli implements the firstline subcommands. It lives outside package
// main so that gremlins resolves its import path and mutates it.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// env is the process's outside world, replaced in tests.
type env struct {
	getenv func(string) string
	stdin  io.Reader
	stderr io.Writer
	log    *slog.Logger
}

const usage = "usage: firstline serve | create-organizer --email <address> | reconcile --since <RFC 3339 time> | migrate-data --from <store> --to <store>"

var commands = map[string]func(context.Context, env, []string) int{
	"serve":            serveCmd,
	"create-organizer": withConfig(createOrganizerCmd),
	"reconcile":        withConfig(reconcileCmd),
	"migrate-data":     withConfig(migrateCmd),
}

// Run executes the subcommand in args and returns the process exit status.
// Logs are JSON lines on stdout; CLI messages go to stderr.
func Run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	slog.SetDefault(log)
	return run(context.Background(), args, env{getenv: getenv, stdin: stdin, stderr: stderr, log: log})
}

func run(ctx context.Context, args []string, e env) int {
	if len(args) > 0 {
		if cmd, ok := commands[args[0]]; ok {
			return cmd(ctx, e, args[1:])
		}
	}
	_, _ = fmt.Fprintln(e.stderr, usage)
	return 1
}

func withConfig(f func(context.Context, env, Config, []string) int) func(context.Context, env, []string) int {
	return func(ctx context.Context, e env, args []string) int {
		cfg, err := LoadConfig(e.getenv)
		if err != nil {
			return exitCode(e.stderr, "config", err)
		}
		return f(ctx, e, cfg, args)
	}
}

// exitCode prints err and maps it to the exit status: 0 for nil, else 1.
func exitCode(w io.Writer, cmd string, err error) int {
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(w, "%s: %v\n", cmd, err)
	return 1
}
