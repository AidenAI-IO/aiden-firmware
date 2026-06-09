package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSearchBackend struct {
	query string
	out   string
	err   error
}

func (b *fakeSearchBackend) Search(ctx context.Context, query string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("missing deadline")
	}
	b.query = query
	return b.out, b.err
}

func TestWebSearchAcceptsJSONAndTruncatesOutput(t *testing.T) {
	backend := &fakeSearchBackend{out: strings.Repeat("x", maxWebToolOutputBytes+100)}
	tool := &WebSearchTool{backend: backend, provider: "duckduckgo"}

	out, err := tool.Call(context.Background(), `{"query":"golang testing"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if backend.query != "golang testing" {
		t.Fatalf("query = %q, want golang testing", backend.query)
	}
	if !strings.Contains(out, "output truncated") {
		t.Fatalf("expected truncated output marker, got %q", out[len(out)-80:])
	}
}

func TestWebSearchRejectsUnconfiguredProvider(t *testing.T) {
	tool := NewWebSearchTool(SearchConfig{Provider: "tavily"}, ProxyConfig{})

	out, err := tool.Call(context.Background(), "test")
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "provider=tavily") {
		t.Fatalf("output = %q, want provider=tavily", out)
	}
}

func TestWebSearchConfiguresBraveProvider(t *testing.T) {
	t.Setenv(braveSearchAPIKeyEnv, "")

	tool := NewWebSearchTool(SearchConfig{Provider: "brave", APIKey: "BSA-token"}, ProxyConfig{})
	if tool.provider != searchProviderBrave {
		t.Fatalf("provider = %q, want %q", tool.provider, searchProviderBrave)
	}
	if _, ok := tool.backend.(*braveBackend); !ok {
		t.Fatalf("backend = %T, want *braveBackend", tool.backend)
	}
}

func TestWebSearchConfiguresBraveProviderFromEnv(t *testing.T) {
	t.Setenv(braveSearchAPIKeyEnv, "BSA-env-token")

	tool := NewWebSearchTool(SearchConfig{Provider: "brave-search"}, ProxyConfig{})
	if tool.provider != searchProviderBrave {
		t.Fatalf("provider = %q, want %q", tool.provider, searchProviderBrave)
	}
	backend, ok := tool.backend.(*braveBackend)
	if !ok {
		t.Fatalf("backend = %T, want *braveBackend", tool.backend)
	}
	if backend.apiKey != "BSA-env-token" {
		t.Fatalf("apiKey = %q, want env token", backend.apiKey)
	}
}

func TestBraveSearchBackendSendsTokenAndFormatsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "BSA-token" {
			t.Errorf("X-Subscription-Token = %q, want BSA-token", got)
		}
		if got := r.URL.Query().Get("q"); got != "golang testing" {
			t.Errorf("q = %q, want golang testing", got)
		}
		if got := r.URL.Query().Get("count"); got != "5" {
			t.Errorf("count = %q, want 5", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"A","url":"https://a.example.com","description":"desc A"},{"title":"B","url":"https://b.example.com","description":"desc B"}]}}`))
	}))
	defer server.Close()

	backend := &braveBackend{
		apiKey:   "BSA-token",
		client:   *server.Client(),
		endpoint: server.URL,
	}
	out, err := backend.Search(context.Background(), "golang testing")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	for _, want := range []string{"[1] A", "https://a.example.com", "desc A", "[2] B"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want substring %q", out, want)
		}
	}
}

func TestBraveSearchBackendReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	backend := &braveBackend{
		apiKey:   "BSA-token",
		client:   *server.Client(),
		endpoint: server.URL,
	}
	_, err := backend.Search(context.Background(), "golang testing")
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCalculatorAcceptsJSONExpression(t *testing.T) {
	out, err := NewCalculatorTool().Call(context.Background(), `{"expression":"1 + 2 * 3"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "7" {
		t.Fatalf("output = %q, want 7", out)
	}
}

func TestWebScraperRejectsInvalidURL(t *testing.T) {
	out, err := NewWebScraperTool(ProxyConfig{}).Call(context.Background(), `{"url":"not a url"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("output = %q, want error response", out)
	}
}
