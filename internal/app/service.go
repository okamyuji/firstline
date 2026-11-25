package app

import (
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/writer"
)

// Deps are the collaborators of Service.
type Deps struct {
	Store     *store.Store
	Writer    *writer.Writer
	Inventory port.Inventory
	Payment   port.Payment
	Errors    port.ErrorReporter
	Clock     Clock
	NewID     func() string
}

// Service holds the application use cases.
type Service struct {
	Store     *store.Store
	Writer    *writer.Writer
	Inventory port.Inventory
	Payment   port.Payment
	Errors    port.ErrorReporter
	Clock     Clock
	NewID     func() string
}

func New(d Deps) *Service {
	return &Service{d.Store, d.Writer, d.Inventory, d.Payment, d.Errors, d.Clock, d.NewID}
}
