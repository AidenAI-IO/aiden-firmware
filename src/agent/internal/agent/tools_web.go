package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	langtools "github.com/tmc/langchaingo/tools"
	"github.com/tmc/langchaingo/tools/duckduckgo"
	"github.com/tmc/langchaingo/tools/scraper"
	"github.com/tmc/langchaingo/tools/wikipedia"
)

const defaultWebSearchMaxResults = 5
const defaultUserAgent = "aiden-agent/1.0 (+https://github.com/AidenAI-IO)"
const defaultWebToolTimeout = 15 * time.Second
const maxWebToolOutputBytes = 12_000

// searchBackend abstracts the actual search call.
type searchBackend interface {
	Search(ctx context.Context, query string) (string, error)
}

// --- Web Search Tool ---

type WebSearchTool struct {
	backend  searchBackend
	provider string
}

func NewWebSearchTool(cfg SearchConfig) *WebSearchTool {
	provider := cfg.ProviderOrDefault()
	var backend searchBackend

	switch provider {
	case "duckduckgo":
		inner, err := duckduckgo.New(
			defaultWebSearchMaxResults,
			defaultUserAgent,
			duckduckgo.WithHTTPClient(&http.Client{Timeout: defaultWebToolTimeout}),
		)
		if err == nil {
			backend = &duckduckgoBackend{inner: inner}
		}
	case "tavily":
		apiKey := strings.TrimSpace(cfg.APIKey)
		if apiKey != "" {
			backend = &tavilyBackend{
				apiKey: apiKey,
				client: http.Client{Timeout: defaultWebToolTimeout},
			}
		}
	}

	return &WebSearchTool{backend: backend, provider: provider}
}

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return `Search the public web and return result snippets. ` +
		`Input JSON: {"query": "..."} or a bare query string. ` +
		`Use this when you need information that is not on the device, such as ` +
		`looking up product details, news, current events, or how a UI element should look.`
}

func (t *WebSearchTool) Call(ctx context.Context, input string) (string, error) {
	if t.backend == nil {
		return "error: web_search tool is not configured (provider=" + t.provider + ")", nil
	}

	query := strings.TrimSpace(input)
	if query == "" {
		return "error: query is required", nil
	}

	if strings.HasPrefix(query, "{") {
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(query), &args); err == nil && strings.TrimSpace(args.Query) != "" {
			query = strings.TrimSpace(args.Query)
		}
	}

	callCtx, cancel := contextWithDefaultTimeout(ctx)
	defer cancel()

	result, err := t.backend.Search(callCtx, query)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return truncateToolOutput(result), nil
}

// --- DuckDuckGo backend ---

type duckduckgoBackend struct {
	inner *duckduckgo.Tool
}

func (b *duckduckgoBackend) Search(ctx context.Context, query string) (string, error) {
	return b.inner.Call(ctx, query)
}

// --- Tavily backend ---

type tavilyBackend struct {
	apiKey string
	client http.Client
}

func (b *tavilyBackend) Search(ctx context.Context, query string) (string, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"api_key":             b.apiKey,
		"query":               query,
		"max_results":         defaultWebSearchMaxResults,
		"include_answer":      true,
		"include_raw_content": false,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tavily API returned %d: %s", resp.StatusCode, string(body))
	}

	var result tavilyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse tavily response: %w", err)
	}

	return result.Format(), nil
}

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func (r *tavilyResponse) Format() string {
	var sb strings.Builder
	if r.Answer != "" {
		sb.WriteString("Answer: ")
		sb.WriteString(r.Answer)
		sb.WriteString("\n\n")
	}
	for i, res := range r.Results {
		sb.WriteString(fmt.Sprintf("[%d] %s\n%s\n%s\n\n", i+1, res.Title, res.URL, res.Content))
	}
	return strings.TrimSpace(sb.String())
}

// --- Wikipedia ---

type WikipediaTool struct {
	inner wikipedia.Tool
}

func NewWikipediaTool() *WikipediaTool {
	return &WikipediaTool{
		inner: wikipedia.New(
			defaultUserAgent,
			wikipedia.WithHTTPClient(&http.Client{Timeout: defaultWebToolTimeout}),
		),
	}
}

func (t *WikipediaTool) Name() string { return "wikipedia" }

func (t *WikipediaTool) Description() string {
	return `Search Wikipedia for factual information about people, places, companies, ` +
		`historical events, or other subjects. Input JSON: {"query": "..."} or a bare query string.`
}

func (t *WikipediaTool) Call(ctx context.Context, input string) (string, error) {
	query := strings.TrimSpace(input)
	if query == "" {
		return "error: query is required", nil
	}

	if strings.HasPrefix(query, "{") {
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(query), &args); err == nil && strings.TrimSpace(args.Query) != "" {
			query = strings.TrimSpace(args.Query)
		}
	}

	callCtx, cancel := contextWithDefaultTimeout(ctx)
	defer cancel()

	result, err := t.inner.Call(callCtx, query)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return truncateToolOutput(result), nil
}

// --- Calculator ---

type CalculatorTool struct {
	inner langtools.Calculator
}

func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{inner: langtools.Calculator{}}
}

func (t *CalculatorTool) Name() string { return "calculator" }

func (t *CalculatorTool) Description() string {
	return `Evaluate a math expression and return the numeric result. ` +
		`Input JSON: {"expression": "..."} or a bare expression string. ` +
		`Supports arithmetic, comparisons, and standard math functions (sqrt, sin, cos, etc).`
}

func (t *CalculatorTool) Call(ctx context.Context, input string) (string, error) {
	expr := strings.TrimSpace(input)
	if expr == "" {
		return "error: expression is required", nil
	}

	if strings.HasPrefix(expr, "{") {
		var args struct {
			Expression string `json:"expression"`
		}
		if err := json.Unmarshal([]byte(expr), &args); err == nil && strings.TrimSpace(args.Expression) != "" {
			expr = strings.TrimSpace(args.Expression)
		}
	}

	result, err := t.inner.Call(ctx, expr)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return result, nil
}

// --- Web Scraper ---

type WebScraperTool struct {
	inner *scraper.Scraper
}

func NewWebScraperTool() *WebScraperTool {
	s, err := scraper.New(
		scraper.WithMaxDepth(1),
		scraper.WithAsync(false),
		scraper.WithMaxPages(1),
	)
	if err != nil {
		return &WebScraperTool{}
	}
	return &WebScraperTool{inner: s}
}

func (t *WebScraperTool) Name() string { return "web_scraper" }

func (t *WebScraperTool) Description() string {
	return `Fetch and extract the text content of a web page. ` +
		`Input JSON: {"url": "..."} or a bare URL string. ` +
		`Returns page title, headers, paragraphs, and links. ` +
		`Use this when you need the full content of a specific page rather than search snippets.`
}

func (t *WebScraperTool) Call(ctx context.Context, input string) (string, error) {
	if t.inner == nil {
		return "error: web_scraper tool is not configured", nil
	}

	rawURL := strings.TrimSpace(input)
	if rawURL == "" {
		return "error: url is required", nil
	}

	if strings.HasPrefix(rawURL, "{") {
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(rawURL), &args); err == nil && strings.TrimSpace(args.URL) != "" {
			rawURL = strings.TrimSpace(args.URL)
		}
	}

	callCtx, cancel := contextWithDefaultTimeout(ctx)
	defer cancel()

	result, err := t.inner.Call(callCtx, rawURL)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return truncateToolOutput(result), nil
}

func contextWithDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultWebToolTimeout)
}

func truncateToolOutput(output string) string {
	if len(output) <= maxWebToolOutputBytes {
		return output
	}

	cut := maxWebToolOutputBytes
	for cut > 0 && !utf8.RuneStart(output[cut]) {
		cut--
	}
	if cut == 0 {
		cut = maxWebToolOutputBytes
	}
	return output[:cut] + fmt.Sprintf("\n\n[output truncated to %d bytes]", cut)
}
