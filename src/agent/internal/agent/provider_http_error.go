package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderHTTPError preserves structured details from a provider HTTP response
// while keeping the historic error text used by logs and API responses.
type ProviderHTTPError struct {
	StatusCode   int
	ProviderCode string
	Message      string
	Body         string
}

func newProviderHTTPError(statusCode int, body []byte) *ProviderHTTPError {
	err := &ProviderHTTPError{
		StatusCode: statusCode,
		Body:       string(body),
	}

	var payload struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		err.ProviderCode = normalizeProviderErrorCode(payload.Error.Code)
		err.Message = strings.TrimSpace(payload.Error.Message)
	}
	return err
}

func normalizeProviderErrorCode(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	var decoded string
	if json.Unmarshal(raw, &decoded) == nil {
		return strings.TrimSpace(decoded)
	}
	return value
}

func (e *ProviderHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Body)
}

func (e *ProviderHTTPError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

func (e *ProviderHTTPError) ProviderErrorCode() string {
	if e == nil {
		return ""
	}
	return e.ProviderCode
}
