package comgate

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientUsesInjectedHTTPClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"code":0,
			"transId":"TRANS-123",
			"refId":"order-123",
			"price":"1000",
			"curr":"CZK",
			"status":"PAID",
			"test":"true"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	status, err := client.Status(context.Background(), "TRANS-123")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != StatusPaid || status.RawStatus != "PAID" {
		t.Fatalf("status = %#v", status)
	}
}

func TestCreateUsesHostedWalletPaymentContract(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		if request.URL.Path != "/v2.0/payment.json" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString(
			[]byte("merchant:provider-secret"),
		)
		if request.Header.Get("Authorization") != wantAuth {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["merchant"] != nil ||
			body["price"] != float64(3490) ||
			body["curr"] != "CZK" ||
			body["refId"] != "018f0000-0000-4000-8000-0000000000c1" ||
			body["method"] != "ALL" ||
			body["label"] != "Order 1" ||
			body["email"] != "buyer@example.test" ||
			body["phone"] != "+15550100" ||
			body["fullName"] != "Jane Doe" ||
			body["enableApplePayGooglePay"] != true ||
			body["expirationTime"] != "30m" ||
			body["test"] != true {
			t.Errorf("create request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{
			"code": 0,
			"message": "OK",
			"transId": "ABCD-EFGH-IJKL",
			"redirect": "https://payments.comgate.cz/client/instructions/index?id=opaque",
			"newField": true
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, 2*time.Second)
	session, err := client.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if requests.Load() != 1 ||
		session.ExternalReference != "ABCD-EFGH-IJKL" ||
		session.RedirectURL != "https://payments.comgate.cz/client/instructions/index?id=opaque" {
		t.Fatalf("session = %#v requests=%d", session, requests.Load())
	}
}

func TestCreateClassifiesDefiniteAndAmbiguousFailure(t *testing.T) {
	t.Parallel()

	t.Run("provider rejection is definite", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusBadRequest)
			_, _ = response.Write([]byte(`{"code":1400,"message":"invalid request"}`))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		_, err := client.Create(context.Background(), validCreateInput())
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("Create rejection error = %v", err)
		}
	})

	for _, statusCode := range []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(statusCode)+" is ambiguous", func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(statusCode)
				_, _ = response.Write([]byte(`{"code":1500,"message":"temporary failure"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			_, err := client.Create(context.Background(), validCreateInput())
			if !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Create HTTP %d error = %v", statusCode, err)
			}
		})
	}

	for _, statusCode := range []int{http.StatusAccepted, http.StatusFound} {
		t.Run(http.StatusText(statusCode)+" is ambiguous", func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(statusCode)
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			_, err := client.Create(context.Background(), validCreateInput())
			if !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Create HTTP %d error = %v", statusCode, err)
			}
		})
	}

	t.Run("timeout is ambiguous", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			time.Sleep(150 * time.Millisecond)
			response.WriteHeader(http.StatusCreated)
		}))
		defer server.Close()
		client := newTestClient(t, server, 20*time.Millisecond)
		_, err := client.Create(context.Background(), validCreateInput())
		if !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("Create timeout error = %v", err)
		}
	})

	t.Run("oversized response is ambiguous", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		_, err := client.Create(context.Background(), validCreateInput())
		if !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("Create oversized response error = %v", err)
		}
	})
}

func TestRequiresExplicitResultCode(t *testing.T) {
	t.Parallel()

	for _, codeField := range []string{"", `"code":null,`} {
		name := "missing"
		if codeField != "" {
			name = "null"
		}
		t.Run("create "+name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusCreated)
				_, _ = response.Write([]byte(`{` + codeField + `
					"transId":"ABCD-EFGH-IJKL",
					"redirect":"https://payments.comgate.cz/client/opaque"
				}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if _, err := client.Create(
				context.Background(),
				validCreateInput(),
			); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Create %s code error = %v", name, err)
			}
		})
		t.Run("status "+name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{` + codeField + `
					"transId":"ABCD-EFGH-IJKL",
					"refId":"018f0000-0000-4000-8000-0000000000c1",
					"price":"3490",
					"curr":"CZK",
					"status":"PAID",
					"test":"true"
				}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if _, err := client.Status(
				context.Background(),
				"ABCD-EFGH-IJKL",
			); !errors.Is(err, ErrProtocol) {
				t.Fatalf("Status %s code error = %v", name, err)
			}
		})
		t.Run("cancel "+name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{` + codeField + `"message":"OK"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if err := client.Cancel(
				context.Background(),
				"ABCD-EFGH-IJKL",
			); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Cancel %s code error = %v", name, err)
			}
		})
		t.Run("refund "+name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{` + codeField + `"message":"OK"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if err := client.Refund(
				context.Background(),
				validRefundInput(),
			); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Refund %s code error = %v", name, err)
			}
		})
	}
}

func TestCreateRequiresJSONAndOwnedRedirectHost(t *testing.T) {
	t.Parallel()

	t.Run("missing JSON content type is ambiguous", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{
				"code":0,
				"transId":"ABCD-EFGH-IJKL",
				"redirect":"https://payments.comgate.cz/client/opaque"
			}`))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		if _, err := client.Create(
			context.Background(), validCreateInput(),
		); !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("Create without JSON content type error = %v", err)
		}
	})

	t.Run("foreign redirect host is ambiguous", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{
				"code":0,
				"transId":"ABCD-EFGH-IJKL",
				"redirect":"https://attacker.example/checkout"
			}`))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		if _, err := client.Create(
			context.Background(), validCreateInput(),
		); !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("Create foreign redirect error = %v", err)
		}
	})

	for _, raw := range []string{
		"https://attacker.example/checkout",
		"https://payments.comgate.cz.attacker.example/checkout",
		"https://user@payments.comgate.cz/checkout",
		"https://payments.comgate.cz:444/checkout",
		"https://payments.comgate.cz/checkout#fragment",
		"http://payments.comgate.cz/checkout",
		"https://payments.comgate.cz/%0d%0aLocation:evil",
		"https://pay0.comgate.cz/checkout",
		"https://pay7.comgate.cz/checkout",
	} {
		if validRedirectURL(raw) {
			t.Fatalf("unsafe redirect accepted: %q", raw)
		}
	}
	for _, raw := range []string{
		"https://payments.comgate.cz/client/opaque",
		"https://payments.comgate.cz?id=query-only",
		"https://pay1.comgate.cz/client/opaque",
		"https://pay6.comgate.cz/client/opaque",
	} {
		if !validRedirectURL(raw) {
			t.Fatalf("owned redirect rejected: %q", raw)
		}
	}
}

func TestStatusParsesOfficialQuotedMoneyAndTestMode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write([]byte(`{
			"code":0,
			"transId":"ABCD-EFGH-IJKL",
			"refId":"018f0000-0000-4000-8000-0000000000c1",
			"price":"3490",
			"curr":"CZK",
			"status":"PAID",
			"test":"true"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	status, err := client.Status(context.Background(), "ABCD-EFGH-IJKL")
	if err != nil {
		t.Fatalf("Status official JSON: %v", err)
	}
	if status.Amount != 3490 || !status.Test || status.Status != StatusPaid {
		t.Fatalf("official Status = %#v", status)
	}
}

func TestStatusRejectsNoncanonicalQuotedFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		price string
		mode  string
	}{
		{name: "numeric price", price: `3490`, mode: `"true"`},
		{name: "leading-zero price", price: `"03490"`, mode: `"true"`},
		{name: "below minimum price", price: `"99"`, mode: `"true"`},
		{name: "overflow price", price: `"100000001"`, mode: `"true"`},
		{name: "numeric test", price: `"3490"`, mode: `true`},
		{name: "capitalized test", price: `"3490"`, mode: `"TRUE"`},
		{name: "missing test", price: `"3490"`, mode: ``},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testField := ""
			if test.mode != "" {
				testField = `,"test":` + test.mode
			}
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{
					"code":0,
					"transId":"ABCD-EFGH-IJKL",
					"refId":"018f0000-0000-4000-8000-0000000000c1",
					"price":` + test.price + `,
					"curr":"CZK",
					"status":"PAID"` + testField + `
				}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if _, err := client.Status(
				context.Background(), "ABCD-EFGH-IJKL",
			); !errors.Is(err, ErrProtocol) {
				t.Fatalf("Status error = %v", err)
			}
		})
	}
}

func TestStatusRejectsMismatchedFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "foreign transaction ID",
			body: `{"code":0,"transId":"OTHER-ID-123","refId":"order-1","price":"3490","curr":"CZK","status":"PAID","test":"true"}`,
		},
		{
			name: "missing reference",
			body: `{"code":0,"transId":"ABCD-EFGH-IJKL","price":"3490","curr":"CZK","status":"PAID","test":"true"}`,
		},
		{
			name: "missing price",
			body: `{"code":0,"transId":"ABCD-EFGH-IJKL","refId":"order-1","curr":"CZK","status":"PAID","test":"true"}`,
		},
		{
			name: "missing test mode",
			body: `{"code":0,"transId":"ABCD-EFGH-IJKL","refId":"order-1","price":"3490","curr":"CZK","status":"PAID"}`,
		},
		{
			name: "missing currency",
			body: `{"code":0,"transId":"ABCD-EFGH-IJKL","refId":"order-1","price":"3490","status":"PAID","test":"true"}`,
		},
		{
			name: "nonzero code",
			body: `{"code":1500,"transId":"ABCD-EFGH-IJKL","refId":"order-1","price":"3490","curr":"CZK","status":"PAID","test":"true"}`,
		},
		{
			name: "unknown status",
			body: `{"code":0,"transId":"ABCD-EFGH-IJKL","refId":"order-1","price":"3490","curr":"CZK","status":"PROCESSING","test":"true"}`,
		},
		{name: "invalid JSON", body: `{`},
		{name: "empty body", body: ``},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if _, err := client.Status(
				context.Background(), "ABCD-EFGH-IJKL",
			); !errors.Is(err, ErrProtocol) {
				t.Fatalf("Status error = %v", err)
			}
		})
	}
}

func TestStatusMapsRawStatuses(t *testing.T) {
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
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{
					"code":0,
					"transId":"ABCD-EFGH-IJKL",
					"refId":"018f0000-0000-4000-8000-0000000000c1",
					"price":"3490",
					"curr":"CZK",
					"status":"` + test.raw + `",
					"test":"true"
				}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			status, err := client.Status(context.Background(), "ABCD-EFGH-IJKL")
			if err != nil || status.Status != test.want || status.RawStatus != test.raw {
				t.Fatalf("Status = %#v, %v", status, err)
			}
		})
	}
}

func TestStatusRejectsHTTPAndMediaTypeErrors(t *testing.T) {
	t.Parallel()

	t.Run("non-200 is unavailable", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		if _, err := client.Status(
			context.Background(), "ABCD-EFGH-IJKL",
		); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Status HTTP error = %v", err)
		}
	})

	t.Run("non-JSON is a protocol error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.Header().Set("Content-Type", "text/plain")
			_, _ = response.Write([]byte(`{"code":0}`))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		if _, err := client.Status(
			context.Background(), "ABCD-EFGH-IJKL",
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Status media type error = %v", err)
		}
	})
}

func TestCancelRejectsNonJSONAndEmptySuccessBody(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "non-JSON content type", contentType: "text/plain", body: `{"code":0}`},
		{name: "empty body", contentType: "application/json"},
		{name: "invalid JSON", contentType: "application/json", body: `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", test.contentType)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			if err := client.Cancel(
				context.Background(), "ABCD-EFGH-IJKL",
			); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("Cancel error = %v", err)
			}
		})
	}
}

func TestRefundRejectsNonJSONSuccessBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "text/plain")
		_, _ = response.Write([]byte(`{"code":0}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	if err := client.Refund(
		context.Background(), validRefundInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Refund media type error = %v", err)
	}
}

func TestDoesNotFollowRedirectsOrReplayCookies(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/v2.0/payment.json":
			http.SetCookie(response, &http.Cookie{Name: "provider-session", Value: "secret"})
			response.Header().Set("Location", "/redirect-target")
			response.WriteHeader(http.StatusFound)
		case "/v2.0/payment/transId/ABCD-EFGH-IJKL.json":
			if request.Header.Get("Cookie") != "" {
				t.Errorf("status request replayed cookie %q", request.Header.Get("Cookie"))
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{
				"code":0,
				"transId":"ABCD-EFGH-IJKL",
				"refId":"018f0000-0000-4000-8000-0000000000c1",
				"price":"3490",
				"curr":"CZK",
				"status":"PENDING",
				"test":"true"
			}`))
		case "/redirect-target":
			targetHits.Add(1)
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	_, err := client.Create(context.Background(), validCreateInput())
	if !errors.Is(err, ErrAmbiguous) || targetHits.Load() != 0 {
		t.Fatalf("redirect create = %v, target hits=%d", err, targetHits.Load())
	}
	status, err := client.Status(context.Background(), "ABCD-EFGH-IJKL")
	if err != nil || status.Status != StatusPending {
		t.Fatalf("Status after cookie response = %#v, %v", status, err)
	}
}

func TestStatusRejectsUnsafeTransactionIDBeforeNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	for _, transactionID := range []string{
		"", "../refund", "https://attacker.test", strings.Repeat("A", 101),
	} {
		if _, err := client.Status(
			context.Background(), transactionID,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Status(%q) error = %v", transactionID, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("unsafe transaction IDs reached provider %d times", hits.Load())
	}
}

func TestCancelRejectsUnsafeTransactionIDBeforeNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	for _, transactionID := range []string{
		"", "../refund", "https://attacker.test", strings.Repeat("A", 101),
	} {
		if err := client.Cancel(
			context.Background(), transactionID,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Cancel(%q) error = %v", transactionID, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("unsafe transaction IDs reached provider %d times", hits.Load())
	}
}

func TestCreateRejectsInvalidInputBeforeNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		hits.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["method"] != "ALL" {
			t.Errorf("method = %#v", body["method"])
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{
			"code":0,
			"transId":"ABCD-EFGH-IJKL",
			"redirect":"https://payments.comgate.cz/client/opaque"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	if _, err := client.Create(
		context.Background(), validCreateInput(),
	); err != nil {
		t.Fatalf("Create valid input: %v", err)
	}

	invalid := []CreateInput{
		{},
		func() CreateInput {
			input := validCreateInput()
			input.Method = "CARD"
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Reference = ""
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Reference = " ref"
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Reference = string([]byte{0xff})
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Amount = 0
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Amount = 99
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Amount = maxAmountMinor + 1
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Currency = "EUR"
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Label = strings.Repeat("l", labelRuneLimit+1)
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Email = strings.Repeat("e", emailLimit+1)
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.Phone = strings.Repeat("1", phoneLimit+1)
			return input
		}(),
		func() CreateInput {
			input := validCreateInput()
			input.FullName = strings.Repeat("n", fullNameRuneLimit+1)
			return input
		}(),
	}
	for _, input := range invalid {
		if _, err := client.Create(
			context.Background(), input,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Create invalid input %#v error = %v", input, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("invalid inputs reached provider %d times", hits.Load())
	}
}

func TestTransportErrorsAreClassified(t *testing.T) {
	t.Parallel()

	client := newRoundTripClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	}))
	if _, err := client.Create(
		context.Background(), validCreateInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Create transport error = %v", err)
	}
	if _, err := client.Status(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Status transport error = %v", err)
	}
	if err := client.Cancel(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Cancel transport error = %v", err)
	}
	if err := client.Refund(
		context.Background(), validRefundInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Refund transport error = %v", err)
	}
}

func TestReadErrorsAreClassified(t *testing.T) {
	t.Parallel()

	client := newRoundTripClient(t, roundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		statusCode := http.StatusOK
		if request.URL.Path == "/v2.0/payment.json" {
			statusCode = http.StatusCreated
		}
		return &http.Response{
			StatusCode: statusCode,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       errorReader{},
			Request:    request,
		}, nil
	}))
	if _, err := client.Create(
		context.Background(), validCreateInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Create read error = %v", err)
	}
	if _, err := client.Status(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Status read error = %v", err)
	}
	if err := client.Cancel(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Cancel read error = %v", err)
	}
	if err := client.Refund(
		context.Background(), validRefundInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Refund read error = %v", err)
	}
}

func TestRefundAcceptedReturnsNil(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v2.0/refund.json" {
			t.Errorf("refund path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode refund request: %v", err)
		}
		if body["transId"] != "ABCD-EFGH-IJKL" ||
			body["amount"] != float64(3490) ||
			body["test"] != true ||
			body["refId"] != "018f0000-0000-4000-8000-0000000000c1" {
			t.Errorf("refund request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"code":0,"message":"accepted"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	if err := client.Refund(
		context.Background(), validRefundInput(),
	); err != nil {
		t.Fatalf("Refund error = %v", err)
	}
}

func TestCancelUsesOnlyDocumentedSuccessResponses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		want        error
	}{
		{
			name: "no content", statusCode: http.StatusNoContent,
		},
		{
			name: "JSON code zero", statusCode: http.StatusOK,
			contentType: "application/json; charset=utf-8",
			body:        `{"code":0,"message":"OK"}`,
		},
		{
			name: "accepted is ambiguous", statusCode: http.StatusAccepted,
			want: ErrAmbiguous,
		},
		{
			name: "redirect is ambiguous", statusCode: http.StatusFound,
			want: ErrAmbiguous,
		},
		{
			name: "server error is ambiguous", statusCode: http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"code":1400,"message":"contradictory"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "bad request is definite", statusCode: http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"code":1400,"message":"invalid"}`,
			want:        ErrRejected,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				if test.contentType != "" {
					response.Header().Set("Content-Type", test.contentType)
				}
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.Cancel(context.Background(), "ABCD-EFGH-IJKL")
			if !errors.Is(err, test.want) {
				t.Fatalf("Cancel error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCancelAndRefundDefinitiveCodes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		statusCode int
		code       int
		want       error
	}{
		{name: "invalid input", code: 1400, want: ErrRejected},
		{name: "invalid method", code: 1401, want: ErrRejected},
		{name: "invalid price", code: 1402, want: ErrRejected},
		{name: "unknown code", code: 1500, want: ErrAmbiguous},
	} {
		t.Run("cancel "+test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{"code":` +
					strconv.Itoa(test.code) + `,"message":"result"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.Cancel(context.Background(), "ABCD-EFGH-IJKL")
			if !errors.Is(err, test.want) {
				t.Fatalf("Cancel error = %v, want %v", err, test.want)
			}
		})
		t.Run("refund "+test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{"code":` +
					strconv.Itoa(test.code) + `,"message":"result"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.Refund(context.Background(), validRefundInput())
			if !errors.Is(err, test.want) {
				t.Fatalf("Refund error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRefundAcceptsDocumentedNoContentAndClassifiesFailures(t *testing.T) {
	t.Parallel()

	t.Run("204 is accepted", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		if err := client.Refund(
			context.Background(), validRefundInput(),
		); err != nil {
			t.Fatalf("Refund 204 error = %v", err)
		}
	})

	t.Run("202 is ambiguous", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"code":0,"message":"unexpected"}`))
		}))
		defer server.Close()
		client := newTestClient(t, server, time.Second)
		err := client.Refund(context.Background(), validRefundInput())
		if !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("Refund 202 error = %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		statusCode int
		code       int
		want       error
	}{
		{
			name: "rate limited", statusCode: http.StatusTooManyRequests,
			code: 1500, want: ErrAmbiguous,
		},
		{
			name: "server processing uncertainty", statusCode: http.StatusInternalServerError,
			code: 1100, want: ErrAmbiguous,
		},
		{
			name: "invalid amount", statusCode: http.StatusBadRequest,
			code: 1402, want: ErrRejected,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(`{"code":` +
					strconv.Itoa(test.code) + `,"message":"failure"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.Refund(context.Background(), validRefundInput())
			if !errors.Is(err, test.want) {
				t.Fatalf("Refund error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRefundRejectsNoncanonicalReferenceBeforeNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	for _, reference := range []string{
		"",
		"not-a-uuid",
		"018F0000-0000-4000-8000-0000000000C1",
		"00000000-0000-0000-0000-000000000000",
		strings.Repeat("x", 101),
	} {
		input := validRefundInput()
		input.Reference = reference
		err := client.Refund(context.Background(), input)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("Refund reference %q error = %v", reference, err)
		}
	}
	for _, amount := range []int64{0, -1, maxAmountMinor + 1} {
		input := validRefundInput()
		input.Amount = amount
		err := client.Refund(context.Background(), input)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("Refund amount %d error = %v", amount, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid refund requests reached provider %d times", hits.Load())
	}
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:   "default production URL",
			config: Config{MerchantID: "merchant", Secret: "secret"},
		},
		{
			name:   "trimmed merchant",
			config: Config{MerchantID: " merchant ", Secret: "secret"},
		},
		{
			name:    "missing merchant",
			config:  Config{MerchantID: " ", Secret: "secret"},
			wantErr: true,
		},
		{
			name:    "long merchant",
			config:  Config{MerchantID: strings.Repeat("m", merchantIDLimit+1), Secret: "secret"},
			wantErr: true,
		},
		{
			name:    "missing secret",
			config:  Config{MerchantID: "merchant"},
			wantErr: true,
		},
		{
			name:    "long secret",
			config:  Config{MerchantID: "merchant", Secret: strings.Repeat("s", secretLimit+1)},
			wantErr: true,
		},
		{
			name: "negative timeout",
			config: Config{
				MerchantID: "merchant", Secret: "secret", Timeout: -time.Second,
			},
			wantErr: true,
		},
		{
			name: "custom https host",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://gateway.example.test",
			},
		},
		{
			name: "trailing slash",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://gateway.example.test/",
			},
		},
		{
			name: "loopback http",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "http://127.0.0.1:8080",
			},
		},
		{
			name: "loopback https",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://127.0.0.1:8443",
			},
		},
		{
			name: "loopback without port",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "http://127.0.0.1",
			},
		},
		{
			name: "http non-loopback",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "http://gateway.example.test",
			},
			wantErr: true,
		},
		{
			name: "version path",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://gateway.example.test/v2.0",
			},
			wantErr: true,
		},
		{
			name: "query",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://gateway.example.test?x=1",
			},
			wantErr: true,
		},
		{
			name: "userinfo",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://user@gateway.example.test",
			},
			wantErr: true,
		},
		{
			name: "fragment",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "https://gateway.example.test#fragment",
			},
			wantErr: true,
		},
		{
			name: "unsupported scheme",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "ftp://gateway.example.test",
			},
			wantErr: true,
		},
		{
			name: "relative URL",
			config: Config{
				MerchantID: "merchant", Secret: "secret",
				BaseURL: "gateway.example.test",
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, err := New(test.config)
			if test.wantErr {
				if err == nil {
					t.Fatalf("New accepted invalid config %#v", test.config)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			client.Close()
		})
	}
}

func TestNewDefaultsAndTLS(t *testing.T) {
	t.Parallel()

	client, err := New(Config{MerchantID: " merchant ", Secret: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(client.Close)

	if client.baseURL != DefaultBaseURL {
		t.Fatalf("baseURL = %q", client.baseURL)
	}
	if client.endpoint("/payment.json") != DefaultBaseURL+"/v2.0/payment.json" {
		t.Fatalf("endpoint = %q", client.endpoint("/payment.json"))
	}
	if client.client.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %v", client.client.Timeout)
	}
	if client.client.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil")
	}
	if client.closeIdleConnections == nil {
		t.Fatal("closeIdleConnections is nil")
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("merchant:secret"))
	if client.authorization != wantAuth {
		t.Fatalf("authorization = %q", client.authorization)
	}
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.client.Transport)
	}
	if transport.TLSClientConfig == nil ||
		transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v", transport.TLSClientConfig)
	}
	client.Close()
	client.Close()

	trailing, err := New(Config{
		MerchantID: "merchant", Secret: "secret",
		BaseURL: "https://payments.comgate.cz/",
	})
	if err != nil {
		t.Fatalf("New trailing slash: %v", err)
	}
	t.Cleanup(trailing.Close)
	if trailing.baseURL != DefaultBaseURL {
		t.Fatalf("trailing baseURL = %q", trailing.baseURL)
	}

	injected := &http.Client{}
	owned, err := New(Config{
		MerchantID: "merchant", Secret: "secret",
		HTTPClient: injected, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New injected: %v", err)
	}
	t.Cleanup(owned.Close)
	if owned.client == injected {
		t.Fatal("injected client was not cloned")
	}
	if owned.client.Timeout != 5*time.Second {
		t.Fatalf("injected timeout = %v", owned.client.Timeout)
	}
	if owned.closeIdleConnections != nil {
		t.Fatal("caller-owned client must not close idle connections")
	}
}

func TestClientNilSafety(t *testing.T) {
	t.Parallel()

	var client *Client
	if _, err := client.Create(
		context.Background(), validCreateInput(),
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Create error = %v", err)
	}
	if _, err := client.Status(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Status error = %v", err)
	}
	if err := client.Cancel(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Cancel error = %v", err)
	}
	if err := client.Refund(
		context.Background(), validRefundInput(),
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Refund error = %v", err)
	}
	client.Close()
}

func TestValidTransactionID(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"ABC",
		"ABCD-EFGH-IJKL",
		"TRANS-123",
		strings.Repeat("A", transactionIDMax),
	} {
		if !validTransactionID(value) {
			t.Fatalf("validTransactionID(%q) = false", value)
		}
	}
	for _, value := range []string{
		"",
		"AB",
		strings.Repeat("A", transactionIDMax+1),
		"../refund",
		"A B",
		"-ABC",
		"ABC-",
		"A--B",
		"https://attacker.test",
	} {
		if validTransactionID(value) {
			t.Fatalf("validTransactionID(%q) = true", value)
		}
	}
}

func TestValidRefundReference(t *testing.T) {
	t.Parallel()

	if !validRefundReference("018f0000-0000-4000-8000-0000000000c1") {
		t.Fatal("canonical UUID rejected")
	}
	for _, value := range []string{
		"",
		"not-a-uuid",
		"018F0000-0000-4000-8000-0000000000C1",
		"018f00000000400080000000000000c1",
		"{018f0000-0000-4000-8000-0000000000c1}",
		"00000000-0000-0000-0000-000000000000",
		strings.Repeat("x", referenceLimit+1),
	} {
		if validRefundReference(value) {
			t.Fatalf("validRefundReference(%q) = true", value)
		}
	}
}

func TestNormalizeStatus(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{raw: "PENDING", want: StatusPending, wantOK: true},
		{raw: "AUTHORIZED", want: StatusPending, wantOK: true},
		{raw: "PAID", want: StatusPaid, wantOK: true},
		{raw: "CANCELLED", want: StatusCancelled, wantOK: true},
		{raw: "pending", wantOK: false},
		{raw: "", wantOK: false},
	} {
		status, ok := normalizeStatus(test.raw)
		if status != test.want || ok != test.wantOK {
			t.Fatalf("normalizeStatus(%q) = %q, %v", test.raw, status, ok)
		}
	}
}

func TestCreateInputValidate(t *testing.T) {
	t.Parallel()

	if err := validCreateInput().Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, method := range []string{"", MethodAll, MethodCardAll, MethodBankOnly} {
		input := validCreateInput()
		input.Method = method
		if err := input.Validate(); err != nil {
			t.Fatalf("method %q rejected: %v", method, err)
		}
	}
}

func TestRefundInputValidate(t *testing.T) {
	t.Parallel()

	if err := validRefundInput().Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	input := validRefundInput()
	input.Amount = maxAmountMinor
	if err := input.Validate(); err != nil {
		t.Fatalf("maximum amount rejected: %v", err)
	}
}

func TestDecimalStringUnmarshal(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`"100"`, `"3490"`, `"100000000"`} {
		var value decimalString
		if err := json.Unmarshal([]byte(raw), &value); err != nil || !value.set {
			t.Fatalf("decimalString(%s) = %#v, %v", raw, value, err)
		}
	}
	for _, raw := range []string{
		`3490`, `"03490"`, `"99"`, `"100000001"`, `""`, `null`, `"12a"`, `"1234567890"`,
	} {
		var value decimalString
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("decimalString(%s) accepted as %#v", raw, value)
		}
	}
}

func TestResponseCodeUnmarshal(t *testing.T) {
	t.Parallel()

	var code responseCode
	if err := json.Unmarshal([]byte(`0`), &code); err != nil ||
		!code.set || code.value != 0 {
		t.Fatalf("code 0 = %#v, %v", code, err)
	}
	if err := json.Unmarshal([]byte(`1400`), &code); err != nil ||
		!code.set || code.value != 1400 {
		t.Fatalf("code 1400 = %#v, %v", code, err)
	}
	for _, raw := range []string{`null`, `"0"`, `1.5`, `{}`} {
		var value responseCode
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("responseCode(%s) accepted as %#v", raw, value)
		}
	}
}

func TestBooleanStringUnmarshal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw   string
		value bool
	}{
		{raw: `"true"`, value: true},
		{raw: `"false"`, value: false},
	} {
		var boolean booleanString
		if err := json.Unmarshal([]byte(test.raw), &boolean); err != nil ||
			!boolean.set || boolean.value != test.value {
			t.Fatalf("booleanString(%s) = %#v, %v", test.raw, boolean, err)
		}
	}
	for _, raw := range []string{`true`, `"TRUE"`, `null`, `""`} {
		var value booleanString
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("booleanString(%s) accepted as %#v", raw, value)
		}
	}
}

func TestJSONResponse(t *testing.T) {
	t.Parallel()

	if jsonResponse(nil) {
		t.Fatal("nil response accepted")
	}
	for _, test := range []struct {
		contentType string
		want        bool
	}{
		{contentType: "", want: false},
		{contentType: "text/plain", want: false},
		{contentType: "application/json", want: true},
		{contentType: "application/json; charset=utf-8", want: true},
	} {
		response := &http.Response{Header: http.Header{}}
		if test.contentType != "" {
			response.Header.Set("Content-Type", test.contentType)
		}
		if jsonResponse(response) != test.want {
			t.Fatalf("jsonResponse(%q) = %v", test.contentType, !test.want)
		}
	}
}

func TestReadResponseLimit(t *testing.T) {
	t.Parallel()

	if _, err := readResponse(nil); err == nil {
		t.Fatal("nil response accepted")
	}
	if _, err := readResponse(&http.Response{}); err == nil {
		t.Fatal("nil body accepted")
	}
	exact := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes))),
	}
	body, err := readResponse(exact)
	if err != nil || len(body) != maxResponseBytes {
		t.Fatalf("exact limit = %d bytes, %v", len(body), err)
	}
	oversized := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
	}
	if _, err := readResponse(oversized); err == nil {
		t.Fatal("oversized body accepted")
	}
}

func TestResultCode(t *testing.T) {
	t.Parallel()

	response := &http.Response{Header: http.Header{}}
	response.Header.Set("Content-Type", "application/json")
	code, ok := resultCode(response, []byte(`{"code":0}`))
	if !ok || code != 0 {
		t.Fatalf("resultCode zero = %d, %v", code, ok)
	}
	code, ok = resultCode(response, []byte(`{"code":1400}`))
	if !ok || code != 1400 {
		t.Fatalf("resultCode 1400 = %d, %v", code, ok)
	}
	if _, ok := resultCode(response, []byte(`{"code":null}`)); ok {
		t.Fatal("null code accepted")
	}
	if _, ok := resultCode(response, []byte(`{`)); ok {
		t.Fatal("invalid JSON accepted")
	}
	if _, ok := resultCode(response, nil); ok {
		t.Fatal("empty body accepted")
	}
	if _, ok := resultCode(nil, []byte(`{"code":0}`)); ok {
		t.Fatal("nil response accepted")
	}
	plain := &http.Response{Header: http.Header{}}
	plain.Header.Set("Content-Type", "text/plain")
	if _, ok := resultCode(plain, []byte(`{"code":0}`)); ok {
		t.Fatal("non-JSON content type accepted")
	}
}

func TestClassifyRefundFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		statusCode int
		want       error
	}{
		{statusCode: http.StatusTooManyRequests, want: ErrAmbiguous},
		{statusCode: http.StatusInternalServerError, want: ErrAmbiguous},
		{statusCode: http.StatusBadRequest, want: ErrRejected},
		{statusCode: http.StatusNotFound, want: ErrRejected},
		{statusCode: http.StatusFound, want: ErrAmbiguous},
		{statusCode: http.StatusTeapot, want: ErrRejected},
	} {
		if err := classifyRefundFailure(test.statusCode); !errors.Is(err, test.want) {
			t.Fatalf("classifyRefundFailure(%d) = %v", test.statusCode, err)
		}
	}
}

func TestPartialRefundSendsExactFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v2.0/refund.json" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode refund request: %v", err)
		}
		want := map[string]any{
			"transId": "ABCD-EFGH-IJKL",
			"amount":  float64(1000),
			"test":    true,
			"refId":   "018f0000-0000-4000-8000-0000000000c1",
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("partial refund request = %#v, want %#v", body, want)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"code":0,"message":"accepted"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	input := validRefundInput()
	input.Amount = 1000
	if err := client.Refund(context.Background(), input); err != nil {
		t.Fatalf("partial Refund: %v", err)
	}
}

func TestPartialRefundRejectionIsDefinite(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(
			`{"code":1402,"message":"refund amount higher than allowed"}`,
		))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	input := validRefundInput()
	input.Amount = maxAmountMinor
	if err := client.Refund(context.Background(), input); !errors.Is(err, ErrRejected) {
		t.Fatalf("partial refund rejection error = %v", err)
	}
}

func TestCreateSendsPreauthAndInitRecurringFlags(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		field string
		set   func(*CreateInput)
	}{
		{
			name: "preauth", field: "preauth",
			set: func(input *CreateInput) { input.Preauth = true },
		},
		{
			name: "initRecurring", field: "initRecurring",
			set: func(input *CreateInput) { input.InitRecurring = true },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode create request: %v", err)
				}
				if body[test.field] != true {
					t.Errorf("create %s = %#v", test.field, body[test.field])
				}
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusCreated)
				_, _ = response.Write([]byte(`{
					"code":0,
					"transId":"ABCD-EFGH-IJKL",
					"redirect":"https://payments.comgate.cz/client/opaqueredirect"
				}`))
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server, time.Second)
			input := validCreateInput()
			test.set(&input)
			if _, err := client.Create(context.Background(), input); err != nil {
				t.Fatalf("Create %s: %v", test.name, err)
			}
		})
	}

	t.Run("defaults omit the flags", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if _, ok := body["preauth"]; ok {
				t.Errorf("preauth present in default create: %#v", body["preauth"])
			}
			if _, ok := body["initRecurring"]; ok {
				t.Errorf("initRecurring present in default create: %#v", body["initRecurring"])
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{
				"code":0,
				"transId":"ABCD-EFGH-IJKL",
				"redirect":"https://payments.comgate.cz/client/opaqueredirect"
			}`))
		}))
		t.Cleanup(server.Close)
		client := newTestClient(t, server, time.Second)
		if _, err := client.Create(context.Background(), validCreateInput()); err != nil {
			t.Fatalf("default Create: %v", err)
		}
	})
}

func TestCaptureSendsPartialAmountAndFullCaptureOmitsAmount(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		amount int64
		want   map[string]any
	}{
		{
			name: "partial capture", amount: 1000,
			want: map[string]any{"amount": float64(1000)},
		},
		{name: "full capture", amount: 0, want: map[string]any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.URL.Path != "/v2.0/preauth/transId/ABCD-EFGH-IJKL.json" ||
					request.Method != http.MethodPut {
					t.Errorf("request = %s %s", request.Method, request.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode capture request: %v", err)
				}
				if !reflect.DeepEqual(body, test.want) {
					t.Errorf("capture request = %#v, want %#v", body, test.want)
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(`{"code":0,"message":"OK"}`))
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, server, time.Second)
			err := client.Capture(context.Background(), CaptureInput{
				ExternalReference: "ABCD-EFGH-IJKL",
				Amount:            test.amount,
			})
			if err != nil {
				t.Fatalf("Capture: %v", err)
			}
		})
	}
}

func TestCaptureClassifiesFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		want        error
	}{
		{
			name: "no content", statusCode: http.StatusNoContent,
		},
		{
			name: "rejection is definite", statusCode: http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"code":1400,"message":"invalid"}`,
			want:        ErrRejected,
		},
		{
			name: "rate limit is ambiguous", statusCode: http.StatusTooManyRequests,
			contentType: "application/json",
			body:        `{"code":1500,"message":"busy"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "server error is ambiguous", statusCode: http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"code":1400,"message":"contradictory"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "accepted without a result is ambiguous", statusCode: http.StatusAccepted,
			want: ErrAmbiguous,
		},
		{
			name: "non-JSON is ambiguous", statusCode: http.StatusOK,
			contentType: "text/plain",
			body:        `{"code":0}`,
			want:        ErrAmbiguous,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				if test.contentType != "" {
					response.Header().Set("Content-Type", test.contentType)
				}
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.Capture(
				context.Background(),
				CaptureInput{ExternalReference: "ABCD-EFGH-IJKL", Amount: 1000},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Capture error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCancelPreauthUsesPreauthEndpointAndClassifiesFailures(t *testing.T) {
	t.Parallel()

	t.Run("no content", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			if request.URL.Path != "/v2.0/preauth/transId/ABCD-EFGH-IJKL.json" ||
				request.Method != http.MethodDelete {
				t.Errorf("request = %s %s", request.Method, request.URL.Path)
			}
			response.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)
		client := newTestClient(t, server, time.Second)
		if err := client.CancelPreauth(
			context.Background(), "ABCD-EFGH-IJKL",
		); err != nil {
			t.Fatalf("CancelPreauth: %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		statusCode int
		want       error
	}{
		{name: "rejection is definite", statusCode: http.StatusBadRequest, want: ErrRejected},
		{name: "rate limit is ambiguous", statusCode: http.StatusTooManyRequests, want: ErrAmbiguous},
		{name: "server error is ambiguous", statusCode: http.StatusInternalServerError, want: ErrAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(`{"code":1400,"message":"failure"}`))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			err := client.CancelPreauth(context.Background(), "ABCD-EFGH-IJKL")
			if !errors.Is(err, test.want) {
				t.Fatalf("CancelPreauth error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRecurringSendsExactFieldsAndReturnsTransactionID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v2.0/recurring.json" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode recurring request: %v", err)
		}
		want := map[string]any{
			"test":            true,
			"price":           float64(1200),
			"curr":            "CZK",
			"label":           "Subscription 1",
			"refId":           "018f0000-0000-4000-8000-0000000000c1",
			"initRecurringId": "ABCD-EFGH-IJKL",
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("recurring request = %#v, want %#v", body, want)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"code":0,
			"message":"OK",
			"transId":"WXYZ-1234-5678"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	charge, err := client.Recurring(context.Background(), validRecurringInput())
	if err != nil {
		t.Fatalf("Recurring: %v", err)
	}
	if charge.ExternalReference != "WXYZ-1234-5678" {
		t.Fatalf("charge = %#v", charge)
	}
}

func TestRecurringClassifiesFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		want        error
	}{
		{
			name: "rejection is definite", statusCode: http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"code":1400,"message":"invalid"}`,
			want:        ErrRejected,
		},
		{
			name: "rate limit is ambiguous", statusCode: http.StatusTooManyRequests,
			contentType: "application/json",
			body:        `{"code":1500,"message":"busy"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "server error is ambiguous", statusCode: http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"code":1500,"message":"failure"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "accepted without a result is ambiguous", statusCode: http.StatusAccepted,
			want: ErrAmbiguous,
		},
		{
			name: "no content is ambiguous", statusCode: http.StatusNoContent,
			want: ErrAmbiguous,
		},
		{
			name: "missing transaction ID is ambiguous", statusCode: http.StatusOK,
			contentType: "application/json",
			body:        `{"code":0,"message":"OK"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "definitive result code is rejected", statusCode: http.StatusOK,
			contentType: "application/json",
			body:        `{"code":1400,"message":"invalid"}`,
			want:        ErrRejected,
		},
		{
			name: "unknown result code is ambiguous", statusCode: http.StatusOK,
			contentType: "application/json",
			body:        `{"code":1500,"message":"failure"}`,
			want:        ErrAmbiguous,
		},
		{
			name: "non-JSON is ambiguous", statusCode: http.StatusOK,
			contentType: "text/plain",
			body:        `{"code":0,"transId":"WXYZ-1234-5678"}`,
			want:        ErrAmbiguous,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				if test.contentType != "" {
					response.Header().Set("Content-Type", test.contentType)
				}
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server, time.Second)
			_, err := client.Recurring(context.Background(), validRecurringInput())
			if !errors.Is(err, test.want) {
				t.Fatalf("Recurring error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPreauthAndRecurringRejectInvalidInputBeforeNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Second)
	captures := []CaptureInput{
		{},
		{ExternalReference: "AB"},
		{ExternalReference: "../preauth"},
		{ExternalReference: "ABCD-EFGH-IJKL", Amount: -1},
		{ExternalReference: "ABCD-EFGH-IJKL", Amount: maxAmountMinor + 1},
	}
	for _, input := range captures {
		if err := client.Capture(
			context.Background(), input,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Capture invalid input %#v error = %v", input, err)
		}
	}
	for _, transactionID := range []string{
		"", "../preauth", strings.Repeat("A", transactionIDMax+1),
	} {
		if err := client.CancelPreauth(
			context.Background(), transactionID,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("CancelPreauth(%q) error = %v", transactionID, err)
		}
	}
	recurring := []RecurringInput{
		{},
		func() RecurringInput {
			input := validRecurringInput()
			input.InitRecurringID = "AB"
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Amount = recurringMinAmountMinor - 1
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Amount = maxAmountMinor + 1
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Currency = "EUR"
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Reference = " ref"
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Reference = strings.Repeat("r", referenceLimit+1)
			return input
		}(),
		func() RecurringInput {
			input := validRecurringInput()
			input.Label = strings.Repeat("l", labelRuneLimit+1)
			return input
		}(),
	}
	for _, input := range recurring {
		if _, err := client.Recurring(
			context.Background(), input,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Recurring invalid input %#v error = %v", input, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid inputs reached provider %d times", hits.Load())
	}
}

func TestNewMethodsNilSafety(t *testing.T) {
	t.Parallel()

	var client *Client
	if err := client.Capture(
		context.Background(),
		CaptureInput{ExternalReference: "ABCD-EFGH-IJKL", Amount: 1000},
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Capture error = %v", err)
	}
	if err := client.CancelPreauth(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil CancelPreauth error = %v", err)
	}
	if _, err := client.Recurring(
		context.Background(), validRecurringInput(),
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nil Recurring error = %v", err)
	}
}

func TestNewMethodsTransportErrorsAreAmbiguous(t *testing.T) {
	t.Parallel()

	client := newRoundTripClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	}))
	if err := client.Capture(
		context.Background(),
		CaptureInput{ExternalReference: "ABCD-EFGH-IJKL", Amount: 1000},
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Capture transport error = %v", err)
	}
	if err := client.CancelPreauth(
		context.Background(), "ABCD-EFGH-IJKL",
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("CancelPreauth transport error = %v", err)
	}
	if _, err := client.Recurring(
		context.Background(), validRecurringInput(),
	); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Recurring transport error = %v", err)
	}
}

func TestCaptureInputValidate(t *testing.T) {
	t.Parallel()

	input := CaptureInput{ExternalReference: "ABCD-EFGH-IJKL"}
	if err := input.Validate(); err != nil {
		t.Fatalf("full capture rejected: %v", err)
	}
	input.Amount = maxAmountMinor
	if err := input.Validate(); err != nil {
		t.Fatalf("maximum amount rejected: %v", err)
	}
}

func TestRecurringInputValidate(t *testing.T) {
	t.Parallel()

	if err := validRecurringInput().Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	input := validRecurringInput()
	input.Amount = recurringMinAmountMinor
	if err := input.Validate(); err != nil {
		t.Fatalf("minimum amount rejected: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errorReader) Close() error {
	return nil
}

func newTestClient(t *testing.T, server *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	client, err := New(Config{
		MerchantID: "merchant",
		Secret:     "provider-secret",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    timeout,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func newRoundTripClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Config{
		MerchantID: "merchant",
		Secret:     "provider-secret",
		BaseURL:    "https://gateway.example.test",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func validCreateInput() CreateInput {
	return CreateInput{
		Reference: "018f0000-0000-4000-8000-0000000000c1",
		Amount:    3490, Currency: "CZK", Label: "Order 1",
		Email: "buyer@example.test", Phone: "+15550100",
		FullName: "Jane Doe", Test: true,
	}
}

func validRefundInput() RefundInput {
	return RefundInput{
		ExternalReference: "ABCD-EFGH-IJKL",
		Reference:         "018f0000-0000-4000-8000-0000000000c1",
		Amount:            3490,
		Test:              true,
	}
}

func validRecurringInput() RecurringInput {
	return RecurringInput{
		InitRecurringID: "ABCD-EFGH-IJKL",
		Reference:       "018f0000-0000-4000-8000-0000000000c1",
		Amount:          1200,
		Currency:        "CZK",
		Label:           "Subscription 1",
		Test:            true,
	}
}
