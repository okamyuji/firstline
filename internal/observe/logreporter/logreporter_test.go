package logreporter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/observe/logreporter"
)

func TestReportWritesOneErrorJSONLine(t *testing.T) {
	var buf bytes.Buffer
	r := logreporter.New(slog.New(slog.NewJSONHandler(&buf, nil)))

	r.Report(context.Background(), errors.New("boom"), slog.String("order", "o1"))

	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("want one line, got %d: %q", n, buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["level"] != "ERROR" || rec["msg"] != "reported error" || rec["error"] != "boom" || rec["order"] != "o1" {
		t.Fatalf("unexpected record: %v", rec)
	}
}

func TestReportToleratesNilError(t *testing.T) {
	var buf bytes.Buffer
	logreporter.New(slog.New(slog.NewJSONHandler(&buf, nil))).Report(context.Background(), nil)
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("got %q", buf.String())
	}
}
