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

func TestBuildKimiProvidersResolveBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		baseURL     string
		wantBaseURL string
	}{
		{"kimi default global", "kimi", "", moonshotGlobalBaseURL},
		{"kimi-cn default cn", "kimi-cn", "", moonshotCNBaseURL},
		{"kimi case insensitive", "Kimi", "", moonshotGlobalBaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewModelManager(ModelConfig{
				Provider: tt.provider,
				Model:    "kimi-k3",
				APIKey:   "test-key",
				BaseURL:  tt.baseURL,
			}, ProxyConfig{})

			model, err := mgr.build(mgr.config)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			compatible, ok := model.(*openAICompatibleModel)
			if !ok {
				t.Fatalf("model type = %T, want *openAICompatibleModel", model)
			}
			if compatible.baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", compatible.baseURL, tt.wantBaseURL)
			}
		})
	}
}

func TestBuildVolcengineProviderResolvesBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		baseURL     string
		wantBaseURL string
	}{
		{"volcengine default ark", "volcengine", "", arkBeijingBaseURL},
		{"volcengine case insensitive", "Volcengine", "", arkBeijingBaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewModelManager(ModelConfig{
				Provider: tt.provider,
				Model:    "doubao-seed-2-1-pro-260628",
				APIKey:   "test-key",
				BaseURL:  tt.baseURL,
			}, ProxyConfig{})

			model, err := mgr.build(mgr.config)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			compatible, ok := model.(*openAICompatibleModel)
			if !ok {
				t.Fatalf("model type = %T, want *openAICompatibleModel", model)
			}
			if compatible.baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", compatible.baseURL, tt.wantBaseURL)
			}
			// The OpenRouter-only nested reasoning object must stay off for Ark:
			// the Ark endpoint only accepts the standard reasoning_effort field.
			if compatible.openRouterReasoning {
				t.Error("openRouterReasoning = true, want false for the volcengine provider")
			}
		})
	}
}

func TestBuildOpenRouterEnablesNestedReasoning(t *testing.T) {
	mgr := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "google/gemini-3.5-flash",
		APIKey:   "test-key",
	}, ProxyConfig{})

	model, err := mgr.build(mgr.config)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	compatible, ok := model.(*openAICompatibleModel)
	if !ok {
		t.Fatalf("model type = %T, want *openAICompatibleModel", model)
	}
	if !compatible.openRouterReasoning {
		t.Error("openRouterReasoning = false, want true for the openrouter provider")
	}
}

func TestBuildOpenRouterMissingAPIKeyError(t *testing.T) {
	t.Run("named environment reference", func(t *testing.T) {
		const tokenEnv = "AIDEN_TEST_MISSING_OPENROUTER_KEY"
		t.Setenv(tokenEnv, "")
		mgr := NewModelManager(ModelConfig{
			Provider: "openrouter",
			Model:    "google/gemini-3.5-flash",
			APIKey:   "$" + tokenEnv,
		}, ProxyConfig{})

		_, err := mgr.build(mgr.config)
		if err == nil {
			t.Fatal("build succeeded without an API key")
		}
		want := "missing the OpenRouter API key, set it in the " + tokenEnv + " environment variable"
		if err.Error() != want {
			t.Fatalf("build error = %q, want %q", err, want)
		}
	})

	t.Run("provider record without api key", func(t *testing.T) {
		mgr := NewModelManager(ModelConfig{
			Provider: "openrouter",
			Model:    "google/gemini-3.5-flash",
		}, ProxyConfig{})

		_, err := mgr.build(mgr.config)
		if err == nil {
			t.Fatal("build succeeded without an API key")
		}
		const want = "missing the OpenRouter API key, set api_key on the provider record"
		if err.Error() != want {
			t.Fatalf("build error = %q, want %q", err, want)
		}
	})
}
