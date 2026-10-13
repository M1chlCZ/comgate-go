// Package comgate is a zero-dependency client for the Comgate payment
// gateway REST API v2.0.
//
// The client creates hosted-gateway payments, reads their status, cancels
// pending payments, requests refunds and lists the enabled payment methods.
// It uses HTTP Basic authentication with Config.MerchantID and Config.Secret.
// Every request carries the caller context, refuses HTTP redirects and reads
// JSON responses with a hard 64 KiB limit.
//
// The package maps every provider answer to a small set of sentinel errors.
// The caller decides how to react to a retryable outage, a permanent
// rejection or an ambiguous outcome. The client performs no logging, caching
// or automatic retries.
package comgate

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the production Comgate API root.
	DefaultBaseURL = "https://payments.comgate.cz"
	// DefaultTimeout is the whole-request timeout used when Config.Timeout
	// is zero.
	DefaultTimeout = 30 * time.Second

	apiVersionPath          = "/v2.0"
	maxResponseBytes        = 64 << 10
	maxNotificationBytes    = 64 << 10
	dialTimeout             = 3 * time.Second
	minAmountMinor          = 100
	maxAmountMinor          = 100_000_000
	recurringMinAmountMinor = 1000
	codeInvalidInput        = 1400
	codeInvalidMethod       = 1401
	codeInvalidPrice        = 1402
	contentTypeJSON         = "application/json"
	schemeHTTP              = "http"
	schemeHTTPS             = "https"
	nullLiteral             = "null"
	booleanTrue             = "true"
	booleanFalse            = "false"
	currencyCZK             = "CZK"
	merchantIDLimit         = 100
	secretLimit             = 4096
	transactionIDMin        = 3
	transactionIDMax        = 100
	referenceLimit          = 100
	redirectURLLimit        = 2048
	labelRuneLimit          = 16
	emailLimit              = 254
	phoneLimit              = 16
	fullNameRuneLimit       = 200
	expirationTime          = "30m"
)

const (
	// MethodAll selects every payment method that the merchant has enabled.
	MethodAll = "ALL"
	// MethodCardAll selects card payments only.
	MethodCardAll = "CARD_ALL"
	// MethodBankOnly selects bank transfers only.
	MethodBankOnly = "BANK_ONLY"
)

const (
	// StatusPending reports a payment that the provider has not settled. The
	// provider raw statuses "PENDING" and "AUTHORIZED" map to this value.
	StatusPending = "pending"
	// StatusPaid reports a settled payment.
	StatusPaid = "paid"
	// StatusCancelled reports a cancelled payment.
	StatusCancelled = "cancelled"
)

const (
	rawStatusPending    = "PENDING"
	rawStatusAuthorized = "AUTHORIZED"
	rawStatusPaid       = "PAID"
	rawStatusCancelled  = "CANCELLED"
)

var (
	// ErrProtocol reports invalid input before any network call or a provider
	// answer that violates the protocol. Correct the input. Do not retry.
	ErrProtocol = errors.New("comgate: protocol is invalid")
	// ErrAmbiguous reports that a mutating call can have reached the
	// provider. Reconcile the payment status before a retry.
	ErrAmbiguous = errors.New("comgate: request outcome is ambiguous")
	// ErrRejected reports a permanent refusal of the request or of the
	// supplied data. A retry of the same request cannot succeed.
	ErrRejected = errors.New("comgate: provider rejected the request")
	// ErrUnavailable reports that a read cannot reach the provider or that
	// the provider is temporarily unavailable. A retry can succeed.
	ErrUnavailable = errors.New("comgate: provider is unavailable")
	// ErrNotFound reports an unknown payment. The test simulator returns it.
	ErrNotFound = errors.New("comgate: payment not found")
)

// Config configures a Client.
type Config struct {
	// MerchantID is the Comgate merchant identifier. It must not be empty and
	// is limited to 100 bytes.
	MerchantID string
	// Secret is the Comgate merchant secret used as the HTTP Basic password.
	// It must not be empty and is limited to 4096 bytes.
	Secret string
	// BaseURL is the API root, for example "https://payments.comgate.cz".
	// The default is DefaultBaseURL. Only a loopback test server can use the
	// http scheme.
	BaseURL string
	// HTTPClient is an optional caller-owned HTTP client. The client clones
	// it, applies Timeout and refuses redirects. Close does not close the
	// idle connections of a caller-owned client.
	HTTPClient *http.Client
	// Timeout is the whole-request timeout. Zero selects DefaultTimeout.
	Timeout time.Duration
}

// Client is a Comgate payment gateway client. Create it with New. Every
// method is safe for concurrent use. Close must not run concurrently with
// other calls.
type Client struct {
	client               *http.Client
	baseURL              string
	authorization        string
	closeIdleConnections func()
	closeOnce            sync.Once
}

// Provider is the payment provider contract that Client implements. The
// comgatest package provides a deterministic in-memory simulator for tests.
type Provider interface {
	Create(context.Context, CreateInput) (Session, error)
	Status(context.Context, string) (Status, error)
	Cancel(context.Context, string) error
	Refund(context.Context, RefundInput) error
	Close()
}

var _ Provider = (*Client)(nil)

// New validates the configuration and returns a client. MerchantID and
// Secret must not be empty, and BaseURL must be an absolute URL. The https
// scheme is mandatory unless the host is a loopback address, which supports
// local test servers.
func New(config Config) (*Client, error) {
	merchant := strings.TrimSpace(config.MerchantID)
	if merchant == "" || len(merchant) > merchantIDLimit {
		return nil, errors.New(
			"comgate: invalid configuration: merchant ID is required and limited to 100 bytes",
		)
	}
	if config.Secret == "" || len(config.Secret) > secretLimit {
		return nil, errors.New(
			"comgate: invalid configuration: secret is required and limited to 4096 bytes",
		)
	}
	if config.Timeout < 0 {
		return nil, errors.New("comgate: invalid configuration: timeout must not be negative")
	}
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	normalized, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	httpClient, closeIdleConnections := newHTTPClient(config.HTTPClient, timeout)
	authorization := "Basic " + base64.StdEncoding.EncodeToString(
		[]byte(merchant+":"+config.Secret),
	)
	return &Client{
		client:               httpClient,
		baseURL:              normalized,
		authorization:        authorization,
		closeIdleConnections: closeIdleConnections,
	}, nil
}

func validateBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New(
			"comgate: invalid configuration: base URL must be absolute without path, query or fragment",
		)
	}
	address := net.ParseIP(parsed.Hostname())
	loopback := address != nil && address.IsLoopback()
	switch parsed.Scheme {
	case schemeHTTPS:
	case schemeHTTP:
		if !loopback {
			return "", errors.New(
				"comgate: invalid configuration: base URL must use https unless the host is loopback",
			)
		}
	default:
		return "", errors.New("comgate: invalid configuration: base URL must use http or https")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func newHTTPClient(injected *http.Client, timeout time.Duration) (*http.Client, func()) {
	if injected != nil {
		clone := *injected
		clone.Timeout = timeout
		clone.CheckRedirect = refuseRedirect
		return &clone, nil
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("comgate: default transport is not an *http.Transport")
	}
	transport := defaultTransport.Clone()
	transport.DialContext = (&net.Dialer{Timeout: min(timeout, dialTimeout)}).DialContext
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: refuseRedirect,
	}
	return client, client.CloseIdleConnections
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func (client *Client) endpoint(path string) string {
	return client.baseURL + apiVersionPath + path
}

func (client *Client) request(
	ctx context.Context,
	method string,
	path string,
	body any,
) (*http.Response, error) {
	request, err := newJSONRequest(
		ctx, method, client.endpoint(path), client.authorization, body,
	)
	if err != nil {
		return nil, err
	}
	return client.client.Do(request)
}

func newJSONRequest(
	ctx context.Context,
	method string,
	rawURL string,
	authorization string,
	body any,
) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", contentTypeJSON)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if body != nil {
		request.Header.Set("Content-Type", contentTypeJSON)
	}
	return request, nil
}

// Create creates a hosted-gateway payment session. The provider returns a
// transaction ID and a redirect URL. Send the customer to the redirect URL.
// An empty input.Method selects MethodAll. Create always enables Apple Pay
// and Google Pay with enableApplePayGooglePay.
func (client *Client) Create(ctx context.Context, input CreateInput) (Session, error) {
	if input.Method == "" {
		input.Method = MethodAll
	}
	if client == nil || client.client == nil || input.Validate() != nil {
		return Session{}, ErrProtocol
	}
	response, err := client.request(ctx, http.MethodPost, "/payment.json", createRequest{
		Price: input.Amount, Currency: input.Currency, Label: input.Label,
		Reference: input.Reference, Method: input.Method, Email: input.Email,
		Phone: input.Phone, FullName: input.FullName, Test: input.Test,
		Preauth: input.Preauth, InitRecurring: input.InitRecurring,
		EnableApplePayGooglePay: true, ExpirationTime: expirationTime,
	})
	if err != nil {
		return Session{}, ErrAmbiguous
	}
	defer response.Body.Close()
	statusCode := response.StatusCode
	if statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError {
		return Session{}, ErrAmbiguous
	}
	if statusCode != http.StatusCreated {
		if statusCode >= http.StatusBadRequest &&
			statusCode < http.StatusInternalServerError {
			return Session{}, ErrRejected
		}
		return Session{}, ErrAmbiguous
	}
	if !jsonResponse(response) {
		return Session{}, ErrAmbiguous
	}
	body, err := readResponse(response)
	if err != nil {
		return Session{}, ErrAmbiguous
	}
	var result createResponse
	if json.Unmarshal(body, &result) != nil ||
		!result.Code.set ||
		result.Code.value != 0 ||
		!validTransactionID(result.TransID) ||
		!validRedirectURL(result.Redirect) {
		return Session{}, ErrAmbiguous
	}
	return Session{
		ExternalReference: result.TransID,
		RedirectURL:       result.Redirect,
	}, nil
}

// Status reads the current state of a payment by transaction ID. It maps the
// provider status to StatusPending, StatusPaid or StatusCancelled.
func (client *Client) Status(ctx context.Context, transactionID string) (Status, error) {
	if client == nil || client.client == nil || !validTransactionID(transactionID) {
		return Status{}, ErrProtocol
	}
	response, err := client.request(
		ctx, http.MethodGet, "/payment/transId/"+transactionID+".json", nil,
	)
	if err != nil {
		return Status{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, ErrUnavailable
	}
	if !jsonResponse(response) {
		return Status{}, ErrProtocol
	}
	body, err := readResponse(response)
	if err != nil {
		return Status{}, ErrUnavailable
	}
	var result statusResponse
	if json.Unmarshal(body, &result) != nil ||
		!result.Code.set ||
		result.Code.value != 0 ||
		result.TransID != transactionID ||
		!validTransactionID(result.TransID) ||
		result.Reference == "" ||
		!result.Price.set ||
		!result.Test.set ||
		result.Currency == "" {
		return Status{}, ErrProtocol
	}
	status, ok := normalizeStatus(result.Status)
	if !ok {
		return Status{}, ErrProtocol
	}
	return Status{
		ExternalReference: result.TransID,
		Reference:         result.Reference,
		Amount:            result.Price.value,
		Currency:          result.Currency,
		Status:            status,
		RawStatus:         result.Status,
		Test:              result.Test.value,
	}, nil
}

// Methods lists the payment methods that the merchant has enabled. The call
// sends no filters, so the provider applies the account defaults for
// language, currency and country. Use Method.ID, Method.Group and
// Method.GroupLabel to render a method selection. A definite rejection
// returns ErrRejected, an unreachable provider, a timeout or an unknown
// provider failure returns ErrUnavailable, and a malformed answer returns
// ErrProtocol.
func (client *Client) Methods(ctx context.Context) ([]Method, error) {
	if client == nil || client.client == nil {
		return nil, ErrProtocol
	}
	response, err := client.request(ctx, http.MethodGet, "/method.json", nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, classifyMethodsFailure(response.StatusCode)
	}
	if !jsonResponse(response) {
		return nil, ErrProtocol
	}
	body, err := readResponse(response)
	if err != nil {
		return nil, ErrUnavailable
	}
	var result methodsResponse
	if json.Unmarshal(body, &result) != nil {
		return nil, ErrProtocol
	}
	if result.Error != nil {
		if !result.Error.Code.set {
			return nil, ErrProtocol
		}
		if definitiveCode(result.Error.Code.value) {
			return nil, ErrRejected
		}
		return nil, ErrUnavailable
	}
	if result.Methods == nil {
		return nil, ErrProtocol
	}
	methods := make([]Method, 0, len(result.Methods))
	for _, entry := range result.Methods {
		if entry.ID == "" {
			return nil, ErrProtocol
		}
		methods = append(methods, Method{
			ID:              entry.ID,
			Group:           entry.Group,
			GroupLabel:      entry.GroupLabel,
			Name:            entry.Name,
			ShortName:       entry.ShortName,
			Description:     entry.Description,
			Logo:            entry.Logo,
			Logo120Centered: entry.Logo120Centered,
			Logo240:         entry.Logo240,
			Logo240Centered: entry.Logo240Centered,
			Logo150Square:   entry.Logo150Square,
			Logo100Square:   entry.Logo100Square,
		})
	}
	return methods, nil
}

// Cancel cancels a pending payment by transaction ID. A provider status 204
// or a JSON result code 0 reports success. A definite rejection returns
// ErrRejected. Any other outcome returns ErrAmbiguous.
func (client *Client) Cancel(ctx context.Context, transactionID string) error {
	if client == nil || client.client == nil || !validTransactionID(transactionID) {
		return ErrProtocol
	}
	return client.cancelPending(ctx, "/payment/transId/"+transactionID+".json")
}

// Capture confirms a pre-authorized card payment. The amount is in minor
// units. An amount of zero captures the full pre-authorized amount and omits
// the amount from the request. A value from 1 to 100000000 captures that
// partial amount. A provider status 204 or a JSON result code 0 reports
// success. A definite rejection returns ErrRejected. Any other failure
// returns ErrAmbiguous.
func (client *Client) Capture(ctx context.Context, input CaptureInput) error {
	if client == nil || client.client == nil || input.Validate() != nil {
		return ErrProtocol
	}
	response, err := client.request(
		ctx,
		http.MethodPut,
		"/preauth/transId/"+input.ExternalReference+".json",
		captureRequest{Amount: input.Amount},
	)
	if err != nil {
		return ErrAmbiguous
	}
	defer response.Body.Close()
	return classifyMutation(response)
}

// CancelPreauth releases the money blocked by a pre-authorized card payment.
// Use it when the order cannot be fulfilled and the pre-authorization must
// not be captured. A provider status 204 or a JSON result code 0 reports
// success. A definite rejection returns ErrRejected. Any other outcome
// returns ErrAmbiguous.
func (client *Client) CancelPreauth(ctx context.Context, transactionID string) error {
	if client == nil || client.client == nil || !validTransactionID(transactionID) {
		return ErrProtocol
	}
	return client.cancelPending(ctx, "/preauth/transId/"+transactionID+".json")
}

// Recurring charges a stored payment method. InitRecurringID must be the
// transaction ID of an initial recurring payment created with
// CreateInput.InitRecurring. The charge runs in the background. The returned
// RecurringCharge carries the transaction ID of the new payment. A definite
// rejection returns ErrRejected. Any other failure returns ErrAmbiguous.
func (client *Client) Recurring(
	ctx context.Context,
	input RecurringInput,
) (RecurringCharge, error) {
	if client == nil || client.client == nil || input.Validate() != nil {
		return RecurringCharge{}, ErrProtocol
	}
	response, err := client.request(ctx, http.MethodPost, "/recurring.json", recurringRequest{
		Test: input.Test, Price: input.Amount, Currency: input.Currency,
		Label: input.Label, Reference: input.Reference,
		InitRecurringID: input.InitRecurringID,
	})
	if err != nil {
		return RecurringCharge{}, ErrAmbiguous
	}
	defer response.Body.Close()
	statusCode := response.StatusCode
	if statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError {
		return RecurringCharge{}, ErrAmbiguous
	}
	if statusCode != http.StatusOK {
		if statusCode >= http.StatusBadRequest &&
			statusCode < http.StatusInternalServerError {
			return RecurringCharge{}, ErrRejected
		}
		return RecurringCharge{}, ErrAmbiguous
	}
	if !jsonResponse(response) {
		return RecurringCharge{}, ErrAmbiguous
	}
	body, err := readResponse(response)
	if err != nil {
		return RecurringCharge{}, ErrAmbiguous
	}
	var result createResponse
	if json.Unmarshal(body, &result) != nil || !result.Code.set {
		return RecurringCharge{}, ErrAmbiguous
	}
	if result.Code.value != 0 {
		if definitiveCode(result.Code.value) {
			return RecurringCharge{}, ErrRejected
		}
		return RecurringCharge{}, ErrAmbiguous
	}
	if !validTransactionID(result.TransID) {
		return RecurringCharge{}, ErrAmbiguous
	}
	return RecurringCharge{ExternalReference: result.TransID}, nil
}

func (client *Client) cancelPending(ctx context.Context, path string) error {
	response, err := client.request(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return ErrAmbiguous
	}
	defer response.Body.Close()
	statusCode := response.StatusCode
	if statusCode == http.StatusNoContent {
		return nil
	}
	if statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError {
		return ErrAmbiguous
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return ErrRejected
	}
	if statusCode != http.StatusOK {
		return ErrAmbiguous
	}
	if !jsonResponse(response) {
		return ErrAmbiguous
	}
	body, err := readResponse(response)
	if err != nil || len(body) == 0 {
		return ErrAmbiguous
	}
	var result resultResponse
	if json.Unmarshal(body, &result) != nil {
		return ErrAmbiguous
	}
	if !result.Code.set {
		return ErrAmbiguous
	}
	if result.Code.value != 0 {
		if definitiveCode(result.Code.value) {
			return ErrRejected
		}
		return ErrAmbiguous
	}
	return nil
}

// Refund requests a full or partial refund of a paid payment. The amount is
// in minor units. RefundInput.Reference must be a canonical UUID, for
// example "018f0000-0000-4000-8000-0000000000c1". A definite rejection
// returns ErrRejected. Any other failure returns ErrAmbiguous.
func (client *Client) Refund(ctx context.Context, input RefundInput) error {
	if client == nil || client.client == nil || input.Validate() != nil {
		return ErrProtocol
	}
	response, err := client.request(ctx, http.MethodPost, "/refund.json", refundRequest{
		TransID: input.ExternalReference, Amount: input.Amount,
		Test: input.Test, Reference: input.Reference,
	})
	if err != nil {
		return ErrAmbiguous
	}
	defer response.Body.Close()
	return classifyMutation(response)
}

// Close releases the idle connections created by the client. It does nothing
// for a caller-owned Config.HTTPClient. Repeated calls are safe.
func (client *Client) Close() {
	if client == nil {
		return
	}
	client.closeOnce.Do(func() {
		if client.closeIdleConnections != nil {
			client.closeIdleConnections()
		}
	})
}
