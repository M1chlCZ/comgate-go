package comgate

import (
	"errors"
	"strings"
	"testing"
)

func TestParseNotificationAcceptsRealisticBody(t *testing.T) {
	t.Parallel()

	body := []byte(
		"merchant=123456&test=false&price=10000&curr=CZK" +
			"&label=Beatles%20-%20Help&refId=2010102600&method=CARD" +
			"&email=info%40example.com&phone=%2B420123456789" +
			"&transId=AB12-EF34-IJ56" +
			"&secret=gx4q8OV3TJt6noJnfhjqJKyX3Z6Ych0y&status=PAID",
	)
	notification, err := ParseNotification(body)
	if err != nil {
		t.Fatalf("ParseNotification: %v", err)
	}
	want := Notification{
		ExternalReference: "AB12-EF34-IJ56",
		Reference:         "2010102600",
		Amount:            10000,
		Currency:          "CZK",
		Status:            StatusPaid,
		RawStatus:         "PAID",
		Test:              false,
		Merchant:          "123456",
		Secret:            "gx4q8OV3TJt6noJnfhjqJKyX3Z6Ych0y",
	}
	if notification != want {
		t.Fatalf("notification = %#v, want %#v", notification, want)
	}
}

func TestParseNotificationMapsStatusesAndDecodesFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "PENDING", want: StatusPending},
		{raw: "AUTHORIZED", want: StatusPending},
		{raw: "PAID", want: StatusPaid},
		{raw: "CANCELLED", want: StatusCancelled},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			body := []byte(
				"transId=AB12-EF34-IJ56&refId=order%2F42&price=1000" +
					"&curr=CZK&test=true&merchant=1&status=" + test.raw,
			)
			notification, err := ParseNotification(body)
			if err != nil {
				t.Fatalf("ParseNotification: %v", err)
			}
			if notification.Status != test.want ||
				notification.RawStatus != test.raw ||
				notification.Reference != "order/42" {
				t.Fatalf("notification = %#v", notification)
			}
		})
	}
}

func TestParseNotificationRejectsMalformedAndMissingFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{
			name: "missing transaction ID",
			body: "refId=order-1&price=1000&curr=CZK&test=true&status=PAID",
		},
		{
			name: "short transaction ID",
			body: "transId=AB&refId=order-1&price=1000&curr=CZK&test=true&status=PAID",
		},
		{
			name: "unsafe transaction ID",
			body: "transId=%2E%2E%2Frefund&refId=order-1&price=1000&curr=CZK&test=true&status=PAID",
		},
		{
			name: "missing reference",
			body: "transId=AB12-EF34-IJ56&price=1000&curr=CZK&test=true&status=PAID",
		},
		{
			name: "padded reference",
			body: "transId=AB12-EF34-IJ56&refId=%20order-1&price=1000&curr=CZK&test=true&status=PAID",
		},
		{
			name: "missing status",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=1000&curr=CZK&test=true",
		},
		{
			name: "unknown status",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=1000&curr=CZK&test=true&status=PROCESSING",
		},
		{
			name: "missing price",
			body: "transId=AB12-EF34-IJ56&refId=order-1&curr=CZK&test=true&status=PAID",
		},
		{
			name: "non-numeric price",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=abc&curr=CZK&test=true&status=PAID",
		},
		{
			name: "below minimum price",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=99&curr=CZK&test=true&status=PAID",
		},
		{
			name: "leading-zero price",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=03490&curr=CZK&test=true&status=PAID",
		},
		{
			name: "missing currency",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=1000&test=true&status=PAID",
		},
		{
			name: "missing test flag",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=1000&curr=CZK&status=PAID",
		},
		{
			name: "invalid test flag",
			body: "transId=AB12-EF34-IJ56&refId=order-1&price=1000&curr=CZK&test=TRUE&status=PAID",
		},
		{
			name: "malformed encoding",
			body: "transId=%zz&refId=order-1&price=1000&curr=CZK&test=true&status=PAID",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseNotification([]byte(test.body)); !errors.Is(err, ErrProtocol) {
				t.Fatalf("ParseNotification error = %v", err)
			}
		})
	}
}

func TestParseNotificationLimit(t *testing.T) {
	t.Parallel()

	body := []byte("a=" + strings.Repeat("b", maxNotificationBytes))
	if _, err := ParseNotification(body); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized ParseNotification error = %v", err)
	}
}
