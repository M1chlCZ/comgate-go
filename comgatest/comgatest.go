// Package comgatest provides a deterministic in-memory test double for the
// comgate client.
//
// The Simulator implements comgate.Provider without network access. It
// derives a stable transaction ID from the merchant reference, so the same
// input always returns the same session. Use Complete to mark a payment as
// paid and Cancel to mark it as cancelled.
package comgatest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	comgate "github.com/m1chlcz/comgate-go"
)

const redirectPrefix = "/comgate-sim/payments/"

// Simulator is a deterministic in-memory payment provider for tests. Create
// it with New. Every method is safe for concurrent use.
type Simulator struct {
	mu       sync.Mutex
	payments map[string]comgate.Status
}

var _ comgate.Provider = (*Simulator)(nil)

// New returns an empty Simulator.
func New() *Simulator {
	return &Simulator{payments: make(map[string]comgate.Status)}
}

// Create stores a pending test payment and returns a deterministic session.
// The external reference is derived from the merchant reference. A replay of
// the same reference returns the stored payment without a reset. Create
// accepts only input with Test set to true. Invalid input returns
// comgate.ErrProtocol.
func (simulator *Simulator) Create(
	_ context.Context,
	input comgate.CreateInput,
) (comgate.Session, error) {
	if simulator == nil || input.Validate() != nil || !input.Test {
		return comgate.Session{}, comgate.ErrProtocol
	}
	digest := sha256.Sum256([]byte(input.Reference))
	externalReference := "SIM-" + strings.ToUpper(hex.EncodeToString(digest[:12]))
	status := comgate.Status{
		ExternalReference: externalReference,
		Reference:         input.Reference,
		Amount:            input.Amount,
		Currency:          input.Currency,
		Status:            comgate.StatusPending,
		RawStatus:         "PENDING",
		Test:              true,
	}
	simulator.mu.Lock()
	if _, exists := simulator.payments[externalReference]; !exists {
		simulator.payments[externalReference] = status
	}
	simulator.mu.Unlock()
	return comgate.Session{
		ExternalReference: externalReference,
		RedirectURL:       redirectPrefix + externalReference + "/complete",
	}, nil
}

// Status returns the stored payment. An unknown transaction ID returns
// comgate.ErrNotFound.
func (simulator *Simulator) Status(
	_ context.Context,
	externalReference string,
) (comgate.Status, error) {
	if simulator == nil {
		return comgate.Status{}, comgate.ErrUnavailable
	}
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	status, ok := simulator.payments[externalReference]
	if !ok {
		return comgate.Status{}, comgate.ErrNotFound
	}
	return status, nil
}

// Complete marks a stored payment as paid and returns the new status. An
// unknown transaction ID returns comgate.ErrNotFound.
func (simulator *Simulator) Complete(externalReference string) (comgate.Status, error) {
	if simulator == nil {
		return comgate.Status{}, comgate.ErrUnavailable
	}
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	status, ok := simulator.payments[externalReference]
	if !ok {
		return comgate.Status{}, comgate.ErrNotFound
	}
	status.Status = comgate.StatusPaid
	status.RawStatus = "PAID"
	simulator.payments[externalReference] = status
	return status, nil
}

// Cancel marks a stored payment as cancelled. An unknown transaction ID
// returns comgate.ErrNotFound.
func (simulator *Simulator) Cancel(_ context.Context, externalReference string) error {
	if simulator == nil {
		return comgate.ErrUnavailable
	}
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	status, ok := simulator.payments[externalReference]
	if !ok {
		return comgate.ErrNotFound
	}
	status.Status = comgate.StatusCancelled
	status.RawStatus = "CANCELLED"
	simulator.payments[externalReference] = status
	return nil
}

// Refund validates a test refund request. The Simulator stores no refund
// state. An invalid request returns comgate.ErrProtocol.
func (simulator *Simulator) Refund(_ context.Context, input comgate.RefundInput) error {
	if simulator == nil || !input.Test || input.Validate() != nil {
		return comgate.ErrProtocol
	}
	return nil
}

// Close releases no resources. It exists for the comgate.Provider contract.
func (simulator *Simulator) Close() {}
