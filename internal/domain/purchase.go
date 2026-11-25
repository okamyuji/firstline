package domain

import (
	"slices"
	"time"
)

type State string

const (
	StateHeld      State = "held"
	StatePurchased State = "purchased"
	StateReleased  State = "released"
	StateExpired   State = "expired"
	StateRefunding State = "refunding"
	StateRefunded  State = "refunded"
)

type Event int

const (
	EvStartFailed Event = iota
	EvPaymentFailed
	EvPaymentCompleted
	EvExpire
	EvRefundSucceeded
)

type MailKind string

const (
	MailNone      MailKind = ""
	MailHold      MailKind = "hold"
	MailPurchased MailKind = "purchased"
	MailRefund    MailKind = "refund"
)

const (
	HoldTTL = 10 * time.Minute
	// ExpiryGrace lets a completion notice that arrives late by retry still beat the expiry sweep.
	ExpiryGrace = time.Minute
)

type Facts struct {
	CompletedAt time.Time // used only for EvPaymentCompleted
	ExpiresAt   time.Time
	Now         time.Time
}

type Decision struct {
	To          State
	Changed     bool
	ReleaseSeat bool
	SellSeat    bool
	Charge      bool
	Refunded    bool
	Mail        MailKind
}

// Next covers the table rows where a purchase record exists. The orphan payment
// and payment_id rows are not state transitions, so Next does not decide them.
func Next(from State, ev Event, f Facts) (Decision, error) {
	switch from {
	case StateHeld:
		return nextFromHeld(ev, f)
	case StateExpired, StateReleased:
		if ev == EvPaymentCompleted {
			return Decision{To: StateRefunding, Changed: true, Mail: MailRefund}, nil
		}
	case StateRefunding:
		if ev == EvRefundSucceeded {
			return Decision{To: StateRefunded, Changed: true, Refunded: true}, nil
		}
	}
	noop := ev == EvPaymentFailed && slices.Contains([]State{StatePurchased, StateReleased, StateExpired, StateRefunding, StateRefunded}, from) ||
		ev == EvPaymentCompleted && slices.Contains([]State{StatePurchased, StateRefunding, StateRefunded}, from)
	if noop {
		return Decision{To: from}, nil
	}
	return Decision{}, ErrInvalidTransition
}

func nextFromHeld(ev Event, f Facts) (Decision, error) {
	// A zero ExpiresAt is a caller bug; treating it as a deadline would refund a paid purchase.
	if (ev == EvPaymentCompleted || ev == EvExpire) && f.ExpiresAt.IsZero() {
		return Decision{}, ErrInvalidTransition
	}
	switch ev {
	case EvStartFailed, EvPaymentFailed:
		return Decision{To: StateReleased, Changed: true, ReleaseSeat: true}, nil
	case EvPaymentCompleted:
		if f.CompletedAt.Before(f.ExpiresAt) {
			return Decision{To: StatePurchased, Changed: true, SellSeat: true, Charge: true, Mail: MailPurchased}, nil
		}
		return Decision{To: StateRefunding, Changed: true, ReleaseSeat: true, Mail: MailRefund}, nil
	case EvExpire:
		if !f.Now.Before(f.ExpiresAt.Add(ExpiryGrace)) {
			return Decision{To: StateExpired, Changed: true, ReleaseSeat: true}, nil
		}
	}
	return Decision{}, ErrInvalidTransition
}
