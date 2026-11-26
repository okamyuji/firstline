// Package logreporter reports errors as ERROR log records.
package logreporter

import (
	"context"
	"log/slog"

	"github.com/okamyuji/firstline/internal/port"
)

type reporter struct{ l *slog.Logger }

// New returns an ErrorReporter that writes one ERROR record per report; the
// CloudWatch metric filter counts records by level.
func New(l *slog.Logger) port.ErrorReporter { return reporter{l} }

func (r reporter) Report(ctx context.Context, err error, attrs ...slog.Attr) {
	args := make([]any, 0, len(attrs)+1)
	if err != nil {
		args = append(args, slog.String("error", err.Error()))
	}
	for _, a := range attrs {
		args = append(args, a)
	}
	r.l.ErrorContext(ctx, "reported error", args...)
}
