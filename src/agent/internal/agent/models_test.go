package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRetryTransportRetriesEOFAndReplaysBody(t *testing.T) {
	attempts := 0
	var bodies []string
	transport := &retryTransport{
		maxRetries:     2,
		retryDelayBase: 0,
		wrapped: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll body: %v", err)
			}
			bodies = append(bodies, string(body))
			if attempts == 1 {
				return nil, io.EOF
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewBufferString(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(bodies) != 2 || bodies[0] != `{"model":"test"}` || bodies[1] != `{"model":"test"}` {
		t.Fatalf("request bodies were not replayed: %#v", bodies)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRetryTransportDoesNotRetryCanceledContext(t *testing.T) {
	attempts := 0
	transport := &retryTransport{
		maxRetries:     2,
		retryDelayBase: 0,
		wrapped: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			return nil, context.Canceled
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewBufferString(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	_, err = transport.RoundTrip(req)
	if err == nil {
		t.Fatal("expected context canceled error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryTransportRetriesTooManyRequests(t *testing.T) {
	attempts := 0
	transport := &retryTransport{
		maxRetries:     2,
		retryDelayBase: 0,
		wrapped: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`rate limited`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewBufferString(`{"model":"test"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
