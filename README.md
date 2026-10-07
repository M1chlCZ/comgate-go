# comgate-go

A zero-dependency Go client for the Czech Comgate payment gateway REST API
v2.0. The module uses only the Go standard library. The client creates
hosted-gateway payments, reads payment status, cancels pending payments and
requests refunds.

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
| `Cancel` | `DELETE /v2.0/payment/transId/{transId}.json` | Cancels a pending payment |
| `Refund` | `POST /v2.0/refund.json` | Requests a full or partial refund |
| `Close` | none | Releases idle connections |

Rules for the calls:

- `Create` always sends `enableApplePayGooglePay: true` and
  `expirationTime: "30m"`. Apple Pay and Google Pay are part of every
  hosted-gateway session.
- `CreateInput.Method` accepts `MethodAll`, `MethodCardAll` and
  `MethodBankOnly`. An empty value selects `MethodAll`.
- `Status` maps the provider status `PENDING` or `AUTHORIZED` to
  `StatusPending`, `PAID` to `StatusPaid` and `CANCELLED` to
  `StatusCancelled`. The `RawStatus` field keeps the provider text.
- `RefundInput.Reference` must be a canonical UUID. The client rejects an
  invalid reference before the network call.
- The amount is in minor units. `Create` accepts 100 to 100000000. `Refund`
  accepts 1 to 100000000.
- Only the currency `CZK` is accepted. This limit matches the source
  application contract.

## Errors

The client returns five sentinel errors. Use `errors.Is` to test them.

| Error | Meaning | Action |
| --- | --- | --- |
| `ErrProtocol` | The input is invalid, or the provider answer violates the protocol. | Correct the input. Do not retry. |
| `ErrRejected` | The provider refused the request permanently. | Correct the data. Do not retry. |
| `ErrAmbiguous` | A mutating call can have reached the provider. | Read the payment status first. Then retry. |
| `ErrUnavailable` | A status read cannot reach the provider. | Retry later. |
| `ErrNotFound` | The test simulator has no such payment. | Use a valid transaction ID. |

The client sends no automatic retry. For `ErrAmbiguous`, read the status with
`Status` and compare the result before a retry. For `ErrUnavailable`, apply
your own backoff. The client classifies answers in this way:

- A create answer with HTTP 400 to 499 is `ErrRejected`, except 408 and 429.
- A cancel or refund answer with HTTP 400 to 499 is `ErrRejected`, except 429.
- An HTTP 408 answer is `ErrAmbiguous` for create and `ErrRejected` for cancel and refund.
- HTTP 429 and 500 to 599 answers are `ErrAmbiguous` for every method.
- A transport error or a timeout is `ErrAmbiguous`.

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
`Cancel` marks it as canceled.

The simulator accepts only create and refund input with `Test: true`. This
rule prevents accidental use in production.

## Security

- The client refuses HTTP redirects and its own HTTP client does not replay
  cookies. A caller-supplied `Config.HTTPClient` keeps its cookie jar.
- The client accepts redirect URLs only from `payments.comgate.cz` and the
  hosts `pay1.comgate.cz` through `pay6.comgate.cz`.
- The client reads every response body with a hard 64 KiB limit.
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
