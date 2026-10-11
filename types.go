package comgate

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	transactionPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)
	refundPattern      = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
	)
	nilReference = "00000000-0000-0000-0000-000000000000"
)

// CreateInput describes a payment to create.
type CreateInput struct {
	// Method is the payment method group. Use MethodAll, MethodCardAll or
	// MethodBankOnly. An empty value selects MethodAll in Create.
	Method string
	// Reference is the merchant order reference. It must not be empty and is
	// limited to 100 bytes.
	Reference string
	// Amount is the price in minor units. The minimum is 100 and the maximum
	// is 100000000.
	Amount int64
	// Currency is the ISO 4217 code. Only "CZK" is accepted.
	Currency string
	// Label is the short payment label. It is limited to 16 characters.
	Label string
	// Email is the customer email. It is limited to 254 bytes.
	Email string
	// Phone is the customer phone number. It is limited to 16 bytes.
	Phone string
	// FullName is the customer name. It is limited to 200 characters.
	FullName string
	// Test reports a test payment when true.
	Test bool
	// Preauth requests a card pre-authorization. Only card payment methods
	// support pre-authorization. The provider reports a successful
	// pre-authorization as AUTHORIZED, which Status maps to StatusPending.
	// Confirm the payment with Capture or release it with CancelPreauth.
	Preauth bool
	// InitRecurring marks the payment as the initial transaction of a
	// recurring payment. The first payment runs through the hosted gateway.
	// Later charges use Recurring with the returned transaction ID.
	InitRecurring bool
}

// Validate checks the input fields. It returns ErrProtocol for invalid input.
func (input CreateInput) Validate() error {
	if input.Method != "" && input.Method != MethodAll &&
		input.Method != MethodCardAll && input.Method != MethodBankOnly {
		return ErrProtocol
	}
	for _, value := range []string{
		input.Reference, input.Currency, input.Label, input.Email,
		input.Phone, input.FullName,
	} {
		if strings.TrimSpace(value) != value || value == "" || !utf8.ValidString(value) {
			return ErrProtocol
		}
	}
	if input.Amount < minAmountMinor ||
		input.Amount > maxAmountMinor ||
		input.Currency != currencyCZK ||
		len(input.Reference) > referenceLimit ||
		utf8.RuneCountInString(input.Label) > labelRuneLimit ||
		len(input.Email) > emailLimit ||
		len(input.Phone) > phoneLimit ||
		utf8.RuneCountInString(input.FullName) > fullNameRuneLimit {
		return ErrProtocol
	}
	return nil
}

// RefundInput describes a refund request.
type RefundInput struct {
	// ExternalReference is the provider transaction ID. Use the
	// Session.ExternalReference value from Create.
	ExternalReference string
	// Reference is the merchant refund reference. It must be a canonical
	// UUID, for example "018f0000-0000-4000-8000-0000000000c1".
	Reference string
	// Amount is the refund amount in minor units. It must be more than zero
	// and at most 100000000.
	Amount int64
	// Test reports a test refund when true.
	Test bool
}

// Validate checks the input fields. It returns ErrProtocol for invalid input.
func (input RefundInput) Validate() error {
	if !validTransactionID(input.ExternalReference) ||
		!validRefundReference(input.Reference) ||
		input.Amount <= 0 ||
		input.Amount > maxAmountMinor {
		return ErrProtocol
	}
	return nil
}

// CaptureInput describes the confirmation of a pre-authorized card payment.
type CaptureInput struct {
	// ExternalReference is the provider transaction ID of the pre-authorized
	// payment. Use the Session.ExternalReference value from Create.
	ExternalReference string
	// Amount is the amount to debit in minor units. Zero requests the full
	// pre-authorized amount and omits the amount from the request. A value
	// from 1 to 100000000 requests a partial capture.
	Amount int64
}

// Validate checks the input fields. It returns ErrProtocol for invalid input.
func (input CaptureInput) Validate() error {
	if !validTransactionID(input.ExternalReference) ||
		input.Amount < 0 ||
		input.Amount > maxAmountMinor {
		return ErrProtocol
	}
	return nil
}

// RecurringInput describes a charge of a stored recurring payment.
type RecurringInput struct {
	// InitRecurringID is the provider transaction ID of the initial
	// recurring payment. Use the Session.ExternalReference value from a
	// Create call with CreateInput.InitRecurring.
	InitRecurringID string
	// Reference is the merchant order reference. It must not be empty and is
	// limited to 100 bytes.
	Reference string
	// Amount is the price in minor units. The minimum is 1000 (10 CZK) and
	// the maximum is 100000000.
	Amount int64
	// Currency is the ISO 4217 code. Only "CZK" is accepted.
	Currency string
	// Label is the short payment label. It is limited to 16 characters.
	Label string
	// Test reports a test charge when true.
	Test bool
}

// Validate checks the input fields. It returns ErrProtocol for invalid input.
func (input RecurringInput) Validate() error {
	for _, value := range []string{input.Reference, input.Currency, input.Label} {
		if strings.TrimSpace(value) != value || value == "" || !utf8.ValidString(value) {
			return ErrProtocol
		}
	}
	if !validTransactionID(input.InitRecurringID) ||
		input.Amount < recurringMinAmountMinor ||
		input.Amount > maxAmountMinor ||
		input.Currency != currencyCZK ||
		len(input.Reference) > referenceLimit ||
		utf8.RuneCountInString(input.Label) > labelRuneLimit {
		return ErrProtocol
	}
	return nil
}

// RecurringCharge is the result of a recurring charge.
type RecurringCharge struct {
	// ExternalReference is the provider transaction ID of the new charge.
	// Read its state with Status.
	ExternalReference string
}

// Session is a created payment session.
type Session struct {
	// ExternalReference is the provider transaction ID.
	ExternalReference string
	// RedirectURL is the hosted-gateway URL for the customer. The client
	// accepts only URLs on the Comgate payment hosts.
	RedirectURL string
}

// Status is the current state of a payment.
type Status struct {
	// ExternalReference is the provider transaction ID.
	ExternalReference string
	// Reference is the merchant order reference.
	Reference string
	// Amount is the price in minor units.
	Amount int64
	// Currency is the ISO 4217 code.
	Currency string
	// Status is the normalized state: StatusPending, StatusPaid or
	// StatusCancelled.
	Status string
	// RawStatus is the provider status text, for example "PAID".
	RawStatus string
	// Test reports a test payment when true.
	Test bool
}

func validTransactionID(value string) bool {
	return len(value) >= transactionIDMin &&
		len(value) <= transactionIDMax &&
		transactionPattern.MatchString(value)
}

func validRefundReference(value string) bool {
	if value == "" || len(value) > referenceLimit || !refundPattern.MatchString(value) {
		return false
	}
	return value != nilReference
}

func validRedirectURL(raw string) bool {
	if len(raw) == 0 ||
		len(raw) > redirectURLLimit ||
		strings.ContainsAny(raw, "\r\n") ||
		strings.Contains(strings.ToLower(raw), "%0d") ||
		strings.Contains(strings.ToLower(raw), "%0a") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme != schemeHTTPS ||
		parsed.User != nil ||
		parsed.Fragment != "" ||
		parsed.Port() != "" ||
		parsed.Opaque != "" {
		return false
	}
	host := parsed.Hostname()
	if host == "payments.comgate.cz" {
		return true
	}
	if len(host) == len("pay1.comgate.cz") &&
		strings.HasPrefix(host, "pay") &&
		host[3] >= '1' &&
		host[3] <= '6' &&
		host[4:] == ".comgate.cz" {
		return true
	}
	return false
}

func normalizeStatus(status string) (string, bool) {
	switch status {
	case rawStatusPending, rawStatusAuthorized:
		return StatusPending, true
	case rawStatusPaid:
		return StatusPaid, true
	case rawStatusCancelled:
		return StatusCancelled, true
	default:
		return "", false
	}
}
