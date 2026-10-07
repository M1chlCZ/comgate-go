package comgate

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
)

type createRequest struct {
	Price                   int64  `json:"price"`
	Currency                string `json:"curr"`
	Label                   string `json:"label"`
	Reference               string `json:"refId"`
	Method                  string `json:"method"`
	Email                   string `json:"email"`
	Phone                   string `json:"phone"`
	FullName                string `json:"fullName"`
	Test                    bool   `json:"test"`
	EnableApplePayGooglePay bool   `json:"enableApplePayGooglePay"`
	ExpirationTime          string `json:"expirationTime"`
}

type refundRequest struct {
	TransID   string `json:"transId"`
	Amount    int64  `json:"amount"`
	Test      bool   `json:"test"`
	Reference string `json:"refId"`
}

type createResponse struct {
	Code     responseCode `json:"code"`
	Message  string       `json:"message"`
	TransID  string       `json:"transId"`
	Redirect string       `json:"redirect"`
}

type statusResponse struct {
	Code      responseCode  `json:"code"`
	TransID   string        `json:"transId"`
	Reference string        `json:"refId"`
	Price     decimalString `json:"price"`
	Currency  string        `json:"curr"`
	Status    string        `json:"status"`
	Test      booleanString `json:"test"`
}

type resultResponse struct {
	Code    responseCode `json:"code"`
	Message string       `json:"message"`
}

type responseCode struct {
	value int
	set   bool
}

func (code *responseCode) UnmarshalJSON(raw []byte) error {
	var value int
	if string(raw) == nullLiteral || json.Unmarshal(raw, &value) != nil {
		return errors.New("comgate: invalid response code")
	}
	code.value = value
	code.set = true
	return nil
}

type decimalString struct {
	value int64
	set   bool
}

func (value *decimalString) UnmarshalJSON(raw []byte) error {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil ||
		encoded == "" ||
		len(encoded) > 9 ||
		len(encoded) > 1 && encoded[0] == '0' {
		return errors.New("comgate: invalid decimal string")
	}
	for _, digit := range encoded {
		if digit < '0' || digit > '9' {
			return errors.New("comgate: invalid decimal string")
		}
	}
	parsed, err := strconv.ParseInt(encoded, 10, 64)
	if err != nil || parsed < minAmountMinor || parsed > maxAmountMinor {
		return errors.New("comgate: invalid decimal string")
	}
	value.value = parsed
	value.set = true
	return nil
}

type booleanString struct {
	value bool
	set   bool
}

func (value *booleanString) UnmarshalJSON(raw []byte) error {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return errors.New("comgate: invalid boolean string")
	}
	switch encoded {
	case booleanTrue:
		value.value = true
	case booleanFalse:
		value.value = false
	default:
		return errors.New("comgate: invalid boolean string")
	}
	value.set = true
	return nil
}

func jsonResponse(response *http.Response) bool {
	if response == nil {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return err == nil && mediaType == contentTypeJSON
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("comgate: invalid provider response")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("comgate: provider response body exceeds limit")
	}
	return body, nil
}

func resultCode(response *http.Response, body []byte) (int, bool) {
	if response == nil || len(body) == 0 || !jsonResponse(response) {
		return 0, false
	}
	var result resultResponse
	if json.Unmarshal(body, &result) != nil {
		return 0, false
	}
	return result.Code.value, result.Code.set
}

func definitiveCode(code int) bool {
	switch code {
	case codeInvalidInput, codeInvalidMethod, codeInvalidPrice:
		return true
	default:
		return false
	}
}

func classifyRefundFailure(statusCode int) error {
	if statusCode == http.StatusTooManyRequests {
		return ErrAmbiguous
	}
	if statusCode >= http.StatusInternalServerError {
		return ErrAmbiguous
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return ErrRejected
	}
	return ErrAmbiguous
}
