package agent

import (
	"context"
	"errors"
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
	tool := NewWebSearchTool(SearchConfig{Provider: "tavily"})

	out, err := tool.Call(context.Background(), "test")
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "provider=tavily") {
		t.Fatalf("output = %q, want provider=tavily", out)
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
	out, err := NewWebScraperTool().Call(context.Background(), `{"url":"not a url"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("output = %q, want error response", out)
	}
}
