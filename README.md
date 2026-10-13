# comgate-go

A zero-dependency Go client for the Czech Comgate payment gateway REST API
v2.0. The module uses only the Go standard library. The client creates
hosted-gateway payments, reads payment status, cancels pending payments,
requests partial or full refunds, confirms or releases pre-authorized card
payments, charges stored recurring payments, lists the enabled payment
methods and parses background status notifications.

## Install

```sh
go get github.com/m1chlcz/comgate-go
```

Go 1.27 or later is required. Only a loopback test server can use the `http`
scheme. Every other base URL must use `https`.

## Quick start

Create a client with your merchant ID and secret. The client sends the
credentials as HTTP Basic authentication. Then create a payment, redirect the
customer, read the status and request a refund.

```go
package main

import (
	"context"
	"log"
	"time"

	comgate "github.com/m1chlcz/comgate-go"
)

func main() {
	client, err := comgate.New(comgate.Config{
		MerchantID: "merchant-id",
		Secret:     "merchant-secret",
		Timeout:    15 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := client.Create(ctx, comgate.CreateInput{
		Reference: "order-2026-000123",
		Amount:    3490,
		Currency:  "CZK",
		Label:     "Order 123",
		Email:     "buyer@example.com",
		Phone:     "+420123456789",
		FullName:  "Jane Doe",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Print(session.RedirectURL)

	status, err := client.Status(ctx, session.ExternalReference)
	if err != nil {
		log.Fatal(err)
	}
	log.Print(status.Status)

	if status.Status == comgate.StatusPaid {
		err = client.Refund(ctx, comgate.RefundInput{
			ExternalReference: session.ExternalReference,
			Reference:         "018f0000-0000-4000-8000-0000000000c1",
			Amount:            status.Amount,
		})
		if err != nil {
			log.Fatal(err)
		}
	}
}
```

The `Create` call returns a transaction ID and a redirect URL. Send the
customer to the redirect URL.

## Methods

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `Create` | `POST /v2.0/payment.json` | Creates a hosted payment session |
| `Status` | `GET /v2.0/payment/transId/{transId}.json` | Reads the payment state |
| `Methods` | `GET /v2.0/method.json` | Lists the enabled payment methods |
| `Cancel` | `DELETE /v2.0/payment/transId/{transId}.json` | Cancels a pending payment |
| `Refund` | `POST /v2.0/refund.json` | Requests a full or partial refund |
| `Capture` | `PUT /v2.0/preauth/transId/{transId}.json` | Confirms a pre-authorized card payment |
| `CancelPreauth` | `DELETE /v2.0/preauth/transId/{transId}.json` | Releases a pre-authorized card payment |
| `Recurring` | `POST /v2.0/recurring.json` | Charges a stored recurring payment |
| `ParseNotification` | none | Parses a background status notification body |
| `Close` | none | Releases idle connections |

Rules for the calls:

- `Create` always sends `enableApplePayGooglePay: true` and
  `expirationTime: "30m"`. Apple Pay and Google Pay are part of every
  hosted-gateway session.
- `CreateInput.Method` accepts `MethodAll`, `MethodCardAll` and
  `MethodBankOnly`. An empty value selects `MethodAll`.
- `CreateInput.Preauth` requests a card pre-authorization. The provider
  reports a successful pre-authorization as `AUTHORIZED`, which `Status` maps
  to `StatusPending` with `RawStatus` set to `"AUTHORIZED"`.
- `CreateInput.InitRecurring` creates the initial payment of a recurring
  payment. Later charges use `Recurring` with the returned transaction ID.
- `Status` maps the provider status `PENDING` or `AUTHORIZED` to
  `StatusPending`, `PAID` to `StatusPaid` and `CANCELLED` to
  `StatusCancelled`. The `RawStatus` field keeps the provider text.
- `RefundInput.Reference` must be a canonical UUID. The client rejects an
  invalid reference before the network call.
- The amount is in minor units. `Create` accepts 100 to 100000000, `Refund`
  accepts 1 to 100000000 and `Recurring` accepts 1000 to 100000000.
- `CaptureInput.Amount` zero captures the full pre-authorized amount; a value
  from 1 to 100000000 captures a partial amount.
- `Methods` sends no filters and returns the methods that the merchant
  account has enabled, with the provider ID, group, display names and logo
  URLs.
- Only the currency `CZK` is accepted. This limit matches the source
  application contract.

### Payment methods

`Methods` reads the payment methods that the merchant has enabled. Use it
when the shop renders its own method selection instead of sending the
customer to the hosted gateway.

```go
methods, err := client.Methods(ctx)
if err != nil {
	log.Fatal(err)
}
for _, method := range methods {
	log.Printf("%s (%s): %s", method.ID, method.Group, method.Name)
}
```

The provider applies the account defaults for the language, currency and
country; the call sends no filters. `Method.ID` is the provider identifier,
for example `CARD_ALL` or `CARD_CZ_COMGATE`. `Method.Group` and
`Method.GroupLabel` carry the method group; `Method.Name` and
`Method.ShortName` carry the display names and `Method.Description` the
description. The logo fields `Logo`, `Logo120Centered`, `Logo240`,
`Logo240Centered`, `Logo150Square` and `Logo100Square` are image URLs in the
documented sizes.

`CreateInput.Method` still accepts `MethodAll`, `MethodCardAll` and
`MethodBankOnly`; the listing does not change that contract.

### Pre-authorization

`CreateInput.Preauth` reserves the amount on the payer card instead of
debiting it. Only card payment methods support pre-authorization. Read the
state with `Status`: a successful reservation is `AUTHORIZED`, which maps to
`StatusPending` with `RawStatus` set to `"AUTHORIZED"`. Confirm the payment
with `Capture`, or release the money with `CancelPreauth`.

```go
session, err := client.Create(ctx, comgate.CreateInput{
	Reference: "order-2026-000123",
	Amount:    3490,
	Currency:  "CZK",
	Label:     "Order 123",
	Email:     "buyer@example.com",
	Phone:     "+420123456789",
	FullName:  "Jane Doe",
	Preauth:   true,
})
if err != nil {
	log.Fatal(err)
}

err = client.Capture(ctx, comgate.CaptureInput{
	ExternalReference: session.ExternalReference,
	Amount:            3000,
})
if err != nil {
	log.Fatal(err)
}
```

The capture amount is in minor units. An amount of zero captures the full
pre-authorized amount. A value from 1 to 100000000 captures that partial
amount. `CancelPreauth` releases the whole amount.

### Recurring payments

`CreateInput.InitRecurring` stores the card during the first payment. The
first payment runs through the hosted gateway like any other payment. Later
charges call `Recurring` with the transaction ID of the initial payment.
They run in the background and return the new transaction ID, not a redirect
URL.

```go
initial, err := client.Create(ctx, comgate.CreateInput{
	Reference:     "subscription-42-init",
	Amount:        1200,
	Currency:      "CZK",
	Label:         "Subscription",
	Email:         "buyer@example.com",
	Phone:         "+420123456789",
	FullName:      "Jane Doe",
	InitRecurring: true,
})
if err != nil {
	log.Fatal(err)
}

charge, err := client.Recurring(ctx, comgate.RecurringInput{
	InitRecurringID: initial.ExternalReference,
	Reference:       "subscription-42-2026-11",
	Amount:          1200,
	Currency:        "CZK",
	Label:           "Subscription",
	Test:            true,
})
if err != nil {
	log.Fatal(err)
}
log.Print(charge.ExternalReference)
```

### Background notifications

The provider sends the payment result to the notification URL as a
form-encoded POST body. Parse the body with `ParseNotification` and read the
authoritative state with `Status` before changing an order.

```go
func handleNotification(response http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 64<<10))
	if err != nil {
		http.Error(response, "bad request", http.StatusBadRequest)
		return
	}
	notification, err := comgate.ParseNotification(body)
	if err != nil {
		http.Error(response, "bad request", http.StatusBadRequest)
		return
	}
	status, err := client.Status(context.Background(), notification.ExternalReference)
	if err != nil ||
		status.Reference != notification.Reference ||
		status.Status != notification.Status {
		http.Error(response, "unverified", http.StatusBadGateway)
		return
	}
	response.WriteHeader(http.StatusOK)
}
```

Notification fields are not signed. Treat every field as untrusted input.
When the integration configures a background secret, the provider echoes it
in `Notification.Secret`; compare it with the configured secret by constant
time comparison. The notification is never proof of payment by itself: read
the payment with `Status`, match the transaction ID, the merchant reference
and the status, and only then change the order state.

## Errors

The client returns five sentinel errors. Use `errors.Is` to test them.

| Error | Meaning | Action |
| --- | --- | --- |
| `ErrProtocol` | The input is invalid, or the provider answer violates the protocol. | Correct the input. Do not retry. |
| `ErrRejected` | The provider refused the request permanently. | Correct the data. Do not retry. |
| `ErrAmbiguous` | A mutating call can have reached the provider. | Read the payment status first. Then retry. |
| `ErrUnavailable` | A read cannot reach the provider or the provider is temporarily unavailable. | Retry later. |
| `ErrNotFound` | The test simulator has no such payment. | Use a valid transaction ID. |

The client sends no automatic retry. For `ErrAmbiguous`, read the status with
`Status` and compare the result before a retry. For `ErrUnavailable`, apply
your own backoff. The client classifies answers in this way:

- A create or recurring answer with HTTP 400 to 499 is `ErrRejected`, except 408 and 429.
- A cancel, cancel-preauth, refund or capture answer with HTTP 400 to 499 is `ErrRejected`, except 429.
- An HTTP 408 answer is `ErrAmbiguous` for create and recurring, and `ErrRejected` for the other mutating calls.
- A `Methods` answer with HTTP 400 to 499 is `ErrRejected`, except 408 and 429. HTTP 408, 429 and 500 to 599, a transport error and a timeout are `ErrUnavailable`, like a status read. An `error` envelope in a 200 answer is `ErrRejected` for the codes 1400, 1401 and 1402 and `ErrUnavailable` for another code.
- HTTP 429 and 500 to 599 answers are `ErrAmbiguous` for every mutating method.
- A transport error or a timeout is `ErrAmbiguous` for a mutating call.

## Test simulator

The `comgatest` package provides a deterministic in-memory provider. It
implements `comgate.Provider` and uses no network.

```go
simulator := comgatest.New()

session, err := simulator.Create(ctx, comgate.CreateInput{
	Reference: "order-2026-000123",
	Amount:    3490,
	Currency:  "CZK",
	Label:     "Order 123",
	Email:     "buyer@example.com",
	Phone:     "+420123456789",
	FullName:  "Jane Doe",
	Test:      true,
})
if err != nil {
	log.Fatal(err)
}

status, err := simulator.Status(ctx, session.ExternalReference)
if err != nil {
	log.Fatal(err)
}
log.Print(status.Status)

paid, err := simulator.Complete(session.ExternalReference)
if err != nil {
	log.Fatal(err)
}
log.Print(paid.Status)
```

The simulator derives the external reference from the merchant reference with
SHA-256. The same input always returns the same session. A replay of the same
reference returns the stored payment. `Complete` marks the payment as paid and
`Cancel` marks it as canceled. `Methods` reports a deterministic method list
with card payments and bank transfers and returns a fresh slice on every call.

The simulator accepts only create and refund input with `Test: true`. This
rule prevents accidental use in production.

## Security

- The client refuses HTTP redirects and its own HTTP client does not replay
  cookies. A caller-supplied `Config.HTTPClient` keeps its cookie jar.
- The client accepts redirect URLs only from `payments.comgate.cz` and the
  hosts `pay1.comgate.cz` through `pay6.comgate.cz`.
- The client reads every response body with a hard 64 KiB limit.
- `ParseNotification` accepts at most 64 KiB and validates the transaction
  ID, the merchant reference and the money fields before it returns.
- The default transport requires TLS 1.2 or later.
- The credentials go only to the configured base URL. A non-loopback base URL
  must use `https`.
- The client sends no credentials in the URL. It uses the `Authorization`
  header.
- The client does not log, cache or persist any value.

## Development

Run the checks from the module root:

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

The tests use `httptest` servers. They use no real credentials and no external
network.

## License

MIT. See [LICENSE](LICENSE).
