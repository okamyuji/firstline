package domain

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)

func TestNext(t *testing.T) {
	exp := t0.Add(HoldTTL)
	ns := time.Nanosecond
	tests := []struct {
		name string
		from State
		ev   Event
		f    Facts
		want Decision
	}{
		{"held start failed", StateHeld, EvStartFailed, Facts{}, Decision{To: StateReleased, Changed: true, ReleaseSeat: true}},
		{"held payment failed", StateHeld, EvPaymentFailed, Facts{}, Decision{To: StateReleased, Changed: true, ReleaseSeat: true}},
		{"held completed before expiry", StateHeld, EvPaymentCompleted, Facts{CompletedAt: exp.Add(-ns), ExpiresAt: exp},
			Decision{To: StatePurchased, Changed: true, SellSeat: true, Charge: true, Mail: MailPurchased}},
		{"held completed at expiry", StateHeld, EvPaymentCompleted, Facts{CompletedAt: exp, ExpiresAt: exp},
			Decision{To: StateRefunding, Changed: true, ReleaseSeat: true, Mail: MailRefund}},
		{"held completed after expiry", StateHeld, EvPaymentCompleted, Facts{CompletedAt: exp.Add(ns), ExpiresAt: exp},
			Decision{To: StateRefunding, Changed: true, ReleaseSeat: true, Mail: MailRefund}},
		{"held expire at grace", StateHeld, EvExpire, Facts{Now: exp.Add(ExpiryGrace), ExpiresAt: exp},
			Decision{To: StateExpired, Changed: true, ReleaseSeat: true}},
		{"expired completed", StateExpired, EvPaymentCompleted, Facts{}, Decision{To: StateRefunding, Changed: true, Mail: MailRefund}},
		{"released completed", StateReleased, EvPaymentCompleted, Facts{}, Decision{To: StateRefunding, Changed: true, Mail: MailRefund}},
		{"refunding refund succeeded", StateRefunding, EvRefundSucceeded, Facts{}, Decision{To: StateRefunded, Changed: true, Refunded: true}},
		{"purchased completed", StatePurchased, EvPaymentCompleted, Facts{}, Decision{To: StatePurchased}},
		{"refunding completed", StateRefunding, EvPaymentCompleted, Facts{}, Decision{To: StateRefunding}},
		{"refunded completed", StateRefunded, EvPaymentCompleted, Facts{}, Decision{To: StateRefunded}},
		{"purchased payment failed", StatePurchased, EvPaymentFailed, Facts{}, Decision{To: StatePurchased}},
		{"released payment failed", StateReleased, EvPaymentFailed, Facts{}, Decision{To: StateReleased}},
		{"expired payment failed", StateExpired, EvPaymentFailed, Facts{}, Decision{To: StateExpired}},
		{"refunding payment failed", StateRefunding, EvPaymentFailed, Facts{}, Decision{To: StateRefunding}},
		{"refunded payment failed", StateRefunded, EvPaymentFailed, Facts{}, Decision{To: StateRefunded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Next(tt.from, tt.ev, tt.f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNextExpireBeforeGrace(t *testing.T) {
	exp := t0.Add(HoldTTL)
	_, err := Next(StateHeld, EvExpire, Facts{Now: exp.Add(ExpiryGrace - time.Nanosecond), ExpiresAt: exp})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v, want ErrInvalidTransition", err)
	}
}

func TestNextRejectsUnlistedPairs(t *testing.T) {
	states := []State{StateHeld, StatePurchased, StateReleased, StateExpired, StateRefunding, StateRefunded}
	events := []Event{EvStartFailed, EvPaymentFailed, EvPaymentCompleted, EvExpire, EvRefundSucceeded}
	type pair struct {
		s State
		e Event
	}
	listed := map[pair]bool{
		{StateHeld, EvStartFailed}:           true,
		{StateHeld, EvPaymentFailed}:         true,
		{StateHeld, EvPaymentCompleted}:      true,
		{StateHeld, EvExpire}:                true,
		{StateExpired, EvPaymentCompleted}:   true,
		{StateReleased, EvPaymentCompleted}:  true,
		{StatePurchased, EvPaymentCompleted}: true,
		{StateRefunding, EvPaymentCompleted}: true,
		{StateRefunded, EvPaymentCompleted}:  true,
		{StateRefunding, EvRefundSucceeded}:  true,
		{StatePurchased, EvPaymentFailed}:    true,
		{StateReleased, EvPaymentFailed}:     true,
		{StateExpired, EvPaymentFailed}:      true,
		{StateRefunding, EvPaymentFailed}:    true,
		{StateRefunded, EvPaymentFailed}:     true,
	}
	exp := t0.Add(HoldTTL)
	f := Facts{CompletedAt: exp, ExpiresAt: exp, Now: exp.Add(ExpiryGrace)}
	n := 0
	for _, s := range states {
		for _, e := range events {
			if listed[pair{s, e}] {
				if _, err := Next(s, e, f); err != nil {
					t.Errorf("(%s,%d) listed but error %v", s, e, err)
				}
				continue
			}
			n++
			if _, err := Next(s, e, f); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("(%s,%d): got %v, want ErrInvalidTransition", s, e, err)
			}
		}
	}
	if n != 15 {
		t.Errorf("unlisted pairs = %d, want 15", n)
	}
}

func TestNextUnknownEvent(t *testing.T) {
	if _, err := Next(StateHeld, Event(99), Facts{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
}
