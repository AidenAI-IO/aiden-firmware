package agent

import (
	"fmt"
	"net/http"
	"testing"
)

func TestProviderHTTPErrorReadsAnthropicType(t *testing.T) {
	err := newProviderHTTPError(529, []byte(
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	))
	if err.ProviderCode != "overloaded_error" || err.Message != "Overloaded" {
		t.Fatalf("ProviderHTTPError = %#v", err)
	}
}

func TestProviderHTTPErrorReadsTopLevelErrorType(t *testing.T) {
	err := newProviderHTTPError(http.StatusInternalServerError, []byte(
		`{"error":{"code":"server_error","message":"Invalid credentials"},"error_type":"authentication"}`,
	))
	if err.ProviderCode != "authentication" || err.Message != "Invalid credentials" {
		t.Fatalf("ProviderHTTPError = %#v", err)
	}
}

func TestIsProviderContextExceededError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "structured provider code",
			err: newProviderHTTPError(http.StatusBadRequest, []byte(
				`{"error":{"code":"context_length_exceeded","message":"request rejected"}}`,
			)),
			want: true,
		},
		{
			name: "openrouter message",
			err: newProviderHTTPError(http.StatusBadRequest, []byte(
				`{"error":{"message":"This request is larger than the model's context length."}}`,
			)),
			want: true,
		},
		{
			name: "anthropic message through wrapper",
			err:  fmt.Errorf("model request failed: %w", fmt.Errorf("prompt is too long: 210001 tokens > 200000 maximum")),
			want: true,
		},
		{
			name: "input token limit message",
			err:  fmt.Errorf("the input token count 12000 exceeds the maximum number of tokens allowed 8192"),
			want: true,
		},
		{
			name: "quota failure",
			err: newProviderHTTPError(http.StatusTooManyRequests, []byte(
				`{"error":{"code":"insufficient_quota","message":"quota exceeded"}}`,
			)),
			want: false,
		},
		{
			name: "unrelated max tokens validation",
			err:  fmt.Errorf("max_tokens must be greater than zero"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProviderContextExceededError(tt.err); got != tt.want {
				t.Fatalf("isProviderContextExceededError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
