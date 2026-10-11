package comgate

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// Notification is a parsed Comgate background payment notification.
//
// The provider sends the notification as a form-encoded HTTP POST body when
// the payment state changes. The parser validates the transaction ID, the
// merchant reference, the price, the currency, the test flag and the status.
//
// The provider does not sign the notification fields. Every field is
// untrusted input, even when the body arrives over HTTPS. When the
// integration configures a background secret, the provider echoes it in the
// secret field. Compare Notification.Secret with the configured secret by
// constant-time comparison. Regardless, read the authoritative state with
// Client.Status and compare it with the notification before you change an
// order.
type Notification struct {
	// ExternalReference is the provider transaction ID from the transId
	// field.
	ExternalReference string
	// Reference is the merchant order reference from the refId field.
	Reference string
	// Amount is the price in minor units from the price field.
	Amount int64
	// Currency is the ISO 4217 code from the curr field.
	Currency string
	// Status is the normalized state: StatusPending, StatusPaid or
	// StatusCancelled.
	Status string
	// RawStatus is the provider status text from the status field, for
	// example "PAID". The value "AUTHORIZED" maps to StatusPending and marks
	// a successful pre-authorization.
	RawStatus string
	// Test reports a test payment when the test field is "true".
	Test bool
	// Merchant is the merchant identifier from the merchant field. It is
	// empty when the field is absent.
	Merchant string
	// Secret is the shared secret echoed in the secret field. It is empty
	// when the field is absent.
	Secret string
}

// ParseNotification parses the form-encoded body of a Comgate background
// payment notification. The parser requires the transId, refId, price, curr,
// test and status fields. Invalid or missing required fields return
// ErrProtocol. The parser reads the optional merchant and secret fields. The
// parser rejects a body larger than 64 KiB.
func ParseNotification(body []byte) (Notification, error) {
	if len(body) > maxNotificationBytes {
		return Notification{}, ErrProtocol
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return Notification{}, ErrProtocol
	}
	transactionID := values.Get("transId")
	reference := values.Get("refId")
	rawStatus := values.Get("status")
	if !validTransactionID(transactionID) ||
		reference == "" ||
		strings.TrimSpace(reference) != reference ||
		len(reference) > referenceLimit ||
		!utf8.ValidString(reference) {
		return Notification{}, ErrProtocol
	}
	status, ok := normalizeStatus(rawStatus)
	if !ok {
		return Notification{}, ErrProtocol
	}
	amount, err := parseDecimalString(values.Get("price"))
	if err != nil {
		return Notification{}, ErrProtocol
	}
	currency := values.Get("curr")
	if currency == "" {
		return Notification{}, ErrProtocol
	}
	test, err := parseBooleanString(values.Get("test"))
	if err != nil {
		return Notification{}, ErrProtocol
	}
	return Notification{
		ExternalReference: transactionID,
		Reference:         reference,
		Amount:            amount,
		Currency:          currency,
		Status:            status,
		RawStatus:         rawStatus,
		Test:              test,
		Merchant:          values.Get("merchant"),
		Secret:            values.Get("secret"),
	}, nil
}
