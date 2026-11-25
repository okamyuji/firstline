package fake

import (
	"context"
	"testing"

	"github.com/okamyuji/firstline/internal/port"
)

var _ port.Mailer = (*Fake)(nil)

func TestSendRecordsMailsInOrder(t *testing.T) {
	f := New()
	a, b := port.Mail{To: "a@example.com", Subject: "1"}, port.Mail{To: "b@example.com", Subject: "2"}
	for _, m := range []port.Mail{a, b} {
		if err := f.Send(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	got := f.Sent()
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("Sent() = %v", got)
	}
}

func TestSentReturnsCopy(t *testing.T) {
	f := New()
	_ = f.Send(context.Background(), port.Mail{To: "a@example.com"})
	f.Sent()[0].To = "changed"
	if f.Sent()[0].To != "a@example.com" {
		t.Fatal("Sent() exposes internal slice")
	}
}

func TestFailNextFailsExactlyNTimesWithoutRecording(t *testing.T) {
	f := New()
	f.FailNext(2)
	for i := range 2 {
		if err := f.Send(context.Background(), port.Mail{Subject: "x"}); err == nil {
			t.Fatalf("send %d: want error", i)
		}
	}
	if len(f.Sent()) != 0 {
		t.Fatal("failed sends were recorded")
	}
	if err := f.Send(context.Background(), port.Mail{Subject: "ok"}); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent()) != 1 {
		t.Fatalf("Sent() = %v", f.Sent())
	}
}
