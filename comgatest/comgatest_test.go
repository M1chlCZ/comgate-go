package comgatest_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	comgate "github.com/m1chlcz/comgate-go"
	"github.com/m1chlcz/comgate-go/comgatest"
)

func TestSimulatorIsDeterministicAndExplicitlyCompletes(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	input := validInput()
	first, err := simulator.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := simulator.Create(context.Background(), input)
	if err != nil || second != first {
		t.Fatalf("deterministic Create = %#v/%#v, %v", first, second, err)
	}
	if first.RedirectURL != "/comgate-sim/payments/"+
		first.ExternalReference+"/complete" {
		t.Fatalf("simulator redirect = %q", first.RedirectURL)
	}
	pending, err := simulator.Status(context.Background(), first.ExternalReference)
	if err != nil || pending.Status != comgate.StatusPending {
		t.Fatalf("pending status = %#v, %v", pending, err)
	}
	paid, err := simulator.Complete(first.ExternalReference)
	if err != nil || paid.Status != comgate.StatusPaid || paid.Reference != input.Reference {
		t.Fatalf("paid status = %#v, %v", paid, err)
	}
	if _, err := simulator.Create(context.Background(), input); err != nil {
		t.Fatalf("replay Create: %v", err)
	}
	replayed, err := simulator.Status(context.Background(), first.ExternalReference)
	if err != nil || replayed.Status != comgate.StatusPaid {
		t.Fatalf("replayed paid status = %#v, %v", replayed, err)
	}
}

func TestSimulatorRequiresTestPayments(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	input := validInput()
	input.Test = false
	if _, err := simulator.Create(
		context.Background(), input,
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("non-test Create error = %v", err)
	}
	invalid := validInput()
	invalid.Currency = "EUR"
	if _, err := simulator.Create(
		context.Background(), invalid,
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("invalid Create error = %v", err)
	}
}

func TestSimulatorMethods(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	first, err := simulator.Methods(context.Background())
	if err != nil {
		t.Fatalf("Methods: %v", err)
	}
	second, err := simulator.Methods(context.Background())
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic Methods = %#v/%#v, %v", first, second, err)
	}
	ids := make(map[string]bool)
	for _, method := range first {
		if method.ID == "" || method.Group == "" || method.GroupLabel == "" ||
			method.Name == "" || method.Logo == "" {
			t.Fatalf("incomplete method = %#v", method)
		}
		ids[method.ID] = true
	}
	if !ids[comgate.MethodCardAll] || !ids[comgate.MethodBankOnly] {
		t.Fatalf("method IDs = %#v", ids)
	}
	first[0].ID = "MUTATED"
	third, err := simulator.Methods(context.Background())
	if err != nil || third[0].ID == "MUTATED" {
		t.Fatalf("Methods shares state = %#v, %v", third, err)
	}
}

func TestSimulatorNotFound(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	if _, err := simulator.Status(
		context.Background(), "SIM-UNKNOWN",
	); !errors.Is(err, comgate.ErrNotFound) {
		t.Fatalf("Status unknown error = %v", err)
	}
	if err := simulator.Cancel(
		context.Background(), "SIM-UNKNOWN",
	); !errors.Is(err, comgate.ErrNotFound) {
		t.Fatalf("Cancel unknown error = %v", err)
	}
	if _, err := simulator.Complete("SIM-UNKNOWN"); !errors.Is(err, comgate.ErrNotFound) {
		t.Fatalf("Complete unknown error = %v", err)
	}
}

func TestSimulatorCancel(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	session, err := simulator.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := simulator.Cancel(context.Background(), session.ExternalReference); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	status, err := simulator.Status(context.Background(), session.ExternalReference)
	if err != nil || status.Status != comgate.StatusCancelled ||
		status.RawStatus != "CANCELLED" {
		t.Fatalf("cancelled status = %#v, %v", status, err)
	}
}

func TestSimulatorRefundValidation(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	if err := simulator.Refund(
		context.Background(), validRefund(),
	); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	nonTest := validRefund()
	nonTest.Test = false
	if err := simulator.Refund(
		context.Background(), nonTest,
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("non-test Refund error = %v", err)
	}
	invalid := validRefund()
	invalid.Reference = "not-a-uuid"
	if err := simulator.Refund(
		context.Background(), invalid,
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("invalid Refund error = %v", err)
	}
	simulator.Close()
}

func TestSimulatorNilSafety(t *testing.T) {
	t.Parallel()

	var simulator *comgatest.Simulator
	if _, err := simulator.Create(
		context.Background(), validInput(),
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("nil Create error = %v", err)
	}
	if _, err := simulator.Status(
		context.Background(), "SIM-UNKNOWN",
	); !errors.Is(err, comgate.ErrUnavailable) {
		t.Fatalf("nil Status error = %v", err)
	}
	if _, err := simulator.Methods(
		context.Background(),
	); !errors.Is(err, comgate.ErrUnavailable) {
		t.Fatalf("nil Methods error = %v", err)
	}
	if err := simulator.Cancel(
		context.Background(), "SIM-UNKNOWN",
	); !errors.Is(err, comgate.ErrUnavailable) {
		t.Fatalf("nil Cancel error = %v", err)
	}
	if _, err := simulator.Complete("SIM-UNKNOWN"); !errors.Is(err, comgate.ErrUnavailable) {
		t.Fatalf("nil Complete error = %v", err)
	}
	if err := simulator.Refund(
		context.Background(), validRefund(),
	); !errors.Is(err, comgate.ErrProtocol) {
		t.Fatalf("nil Refund error = %v", err)
	}
	simulator.Close()
}

func TestSimulatorConcurrentAccess(t *testing.T) {
	t.Parallel()

	simulator := comgatest.New()
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			session, err := simulator.Create(context.Background(), validInput())
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			if _, err := simulator.Status(context.Background(), session.ExternalReference); err != nil {
				t.Errorf("Status: %v", err)
			}
		}()
	}
	wait.Wait()
}

func validInput() comgate.CreateInput {
	return comgate.CreateInput{
		Reference: "018f0000-0000-4000-8000-0000000000c1",
		Amount:    3490, Currency: "CZK", Label: "Order 1",
		Email: "buyer@example.test", Phone: "+15550100",
		FullName: "Jane Doe", Test: true,
	}
}

func validRefund() comgate.RefundInput {
	return comgate.RefundInput{
		ExternalReference: "SIM-" + strings.Repeat("A", 24),
		Reference:         "018f0000-0000-4000-8000-0000000000c1",
		Amount:            3490,
		Test:              true,
	}
}
