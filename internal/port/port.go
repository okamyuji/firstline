package port

import (
	"context"
	"log/slog"
	"time"
)

type Inventory interface {
	Add(ctx context.Context, eventID string, capacity int) error
	Reserve(ctx context.Context, eventID string, quantity int) error
	Release(ctx context.Context, eventID string, quantity int) error
	Remaining(ctx context.Context, eventID string) (int, error)
}

type Payment interface {
	Start(ctx context.Context, purchaseID string, amount int) (paymentID, checkoutURL string, err error)
	Refund(ctx context.Context, paymentID string) error
	Completed(ctx context.Context, since time.Time) ([]Completion, error)
}

type Completion struct {
	PurchaseID string
	PaymentID  string
	Amount     int
	At         time.Time
}

type PaymentNotifier interface {
	Completed(ctx context.Context, c Completion) error
	Failed(ctx context.Context, purchaseID, paymentID string) error
}

type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

type Mail struct{ To, Lang, Subject, Body string }

type ErrorReporter interface {
	Report(ctx context.Context, err error, attrs ...slog.Attr)
}

type MetricsSink interface {
	Emit(ctx context.Context, s HealthSnapshot) error
}

type HealthSnapshot struct {
	CommitWaitP99Ms      float64
	SyncItemsPerSecMax5m int
	QueueLen, QueueCap   int
	QueueUsageAvg5m      float64
	DroppedTotal         int64
	WALBytes, DBBytes    int64
	LastReconcileAt      time.Time
	TLSCertDaysLeft      int
}
