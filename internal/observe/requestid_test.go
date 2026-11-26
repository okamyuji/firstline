package observe_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/observe"
)

func TestRequestIDHandlerAddsTheIDFromTheContext(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(observe.RequestIDHandler{Handler: slog.NewTextHandler(&buf, nil)})
	ctx := observe.WithRequestID(t.Context(), "r1")

	log.InfoContext(ctx, "plain")
	log.With("k", "v").InfoContext(ctx, "with attrs")
	log.WithGroup("g").InfoContext(ctx, "with group", "x", 1)
	log.InfoContext(context.Background(), "no id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines[:2] {
		if !strings.Contains(l, " request_id=r1") {
			t.Errorf("line lacks the id: %s", l)
		}
	}
	if !strings.Contains(lines[2], "g.request_id=r1") {
		t.Errorf("grouped line lacks the id: %s", lines[2])
	}
	if strings.Contains(lines[3], "request_id") {
		t.Errorf("line without an id in the context has one: %s", lines[3])
	}
	if observe.RequestID(context.Background()) != "" || observe.RequestID(ctx) != "r1" {
		t.Error("RequestID does not read back the stored id")
	}
}
