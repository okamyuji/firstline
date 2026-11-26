package domain

import "errors"

var (
	ErrSoldOut           = errors.New("sold out")
	ErrInvalidTransition = errors.New("invalid purchase transition")
	ErrNotOnSale         = errors.New("event is not on sale")
	ErrBadQuantity       = errors.New("quantity must be between 1 and 4")
	ErrDuplicateHold     = errors.New("an active hold already exists for this email")
	ErrNotFound          = errors.New("not found")
	// ErrPaymentUnavailable means Payment.Start failed. The hold is released, or
	// left to the expiry sweep when the release itself failed.
	ErrPaymentUnavailable = errors.New("payment could not be started")
)
