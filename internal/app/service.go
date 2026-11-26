package app

import (
	"crypto/rand"
	"encoding/base64"

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

// New builds a Service; a nil NewID means random purchase IDs.
func New(d Deps) *Service {
	if d.NewID == nil {
		d.NewID = newID
	}
	return &Service{d.Store, d.Writer, d.Inventory, d.Payment, d.Errors, d.Clock, d.NewID}
}

// newID returns 16 random bytes as unpadded base64url. rand.Read never returns
// an error since Go 1.24; it crashes the program instead.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
