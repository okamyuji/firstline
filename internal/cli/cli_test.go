package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testEnv(over map[string]string, stdin io.Reader, stderr io.Writer) env {
	return env{getenv: envOf(over), stdin: stdin, stderr: stderr, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}

func TestRunRejectsUnknownOrMissingSubcommand(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, testEnv(nil, nil, &stderr)); code != 1 || !strings.Contains(stderr.String(), "usage") {
			t.Errorf("run(%q) = %d, %q; want 1 and usage", args, code, stderr.String())
		}
	}
}

func TestRunEntryPoint(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	var stderr bytes.Buffer
	if code := Run([]string{"bogus"}, envOf(nil), strings.NewReader(""), io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "usage") {
		t.Errorf("Run = %d, %q; want 1 and usage", code, stderr.String())
	}
}

func TestSubcommandsFailOnBadConfig(t *testing.T) {
	for _, cmd := range []string{"serve", "create-organizer", "reconcile", "migrate-data"} {
		var stderr bytes.Buffer
		code := run(context.Background(), []string{cmd}, testEnv(map[string]string{"FIRSTLINE_TOKEN_KEY": "short"}, strings.NewReader(""), &stderr))
		if code != 1 || !strings.Contains(stderr.String(), "FIRSTLINE_TOKEN_KEY") {
			t.Errorf("%s: exit %d, %q; want 1 naming FIRSTLINE_TOKEN_KEY", cmd, code, stderr.String())
		}
	}
}
