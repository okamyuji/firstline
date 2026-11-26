package observe

import (
	"context"
	"log/slog"
)

type requestIDKey struct{}

// WithRequestID returns ctx carrying the request id id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request id in ctx, or "" when there is none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestIDHandler adds request_id to every record logged with a context that
// carries one. Only the *Context logging calls pass the context through.
type RequestIDHandler struct{ slog.Handler }

func (h RequestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h RequestIDHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return RequestIDHandler{h.Handler.WithAttrs(as)}
}

func (h RequestIDHandler) WithGroup(name string) slog.Handler {
	return RequestIDHandler{h.Handler.WithGroup(name)}
}
