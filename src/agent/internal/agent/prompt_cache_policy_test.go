package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent/context_manager"

	"github.com/tmc/langchaingo/llms"
)

func TestResolveOpenRouterPromptCachePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		endpoints []openRouterEndpointMetadata
		want      PromptCachePolicy
	}{
		{
			name: "explicit cache_control parameter",
			endpoints: []openRouterEndpointMetadata{{
				SupportedParameters: []string{"temperature", "cache_control"},
			}},
			want: PromptCachePolicyExplicit,
		},
		{
			name: "implicit caching only",
			endpoints: []openRouterEndpointMetadata{{
				SupportedParameters:     []string{"temperature"},
				SupportsImplicitCaching: boolPtr(true),
			}},
			want: PromptCachePolicyImplicit,
		},
		{
			name: "cache read pricing only",
			endpoints: []openRouterEndpointMetadata{{
				Pricing: map[string]any{"input_cache_read": "0.0000003"},
			}},
			want: PromptCachePolicyImplicit,
		},
		{
			name:      "none",
			endpoints: []openRouterEndpointMetadata{{SupportedParameters: []string{"temperature"}}},
			want:      PromptCachePolicyNone,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveOpenRouterPromptCachePolicy(tc.endpoints); got != tc.want {
				t.Fatalf("resolveOpenRouterPromptCachePolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenRouterModelEndpointsURL(t *testing.T) {
	t.Parallel()

	got, ok := openRouterModelEndpointsURL("https://openrouter.ai/api/v1", "anthropic/claude-sonnet-4")
	if !ok {
		t.Fatal("expected valid endpoints URL")
	}
	want := "https://openrouter.ai/api/v1/models/anthropic/claude-sonnet-4/endpoints"
	if got != want {
		t.Fatalf("endpoints URL = %q, want %q", got, want)
	}
}

func TestCachedOpenRouterPromptCachePolicyUsesEndpointsResponse(t *testing.T) {
	var captured struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/vendor/cache-control-model/endpoints":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"endpoints":[{"supported_parameters":["cache_control"]}]}}`))
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/cache-control-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	}, ProxyConfig{})
	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := model.GenerateContent(context_manager.WithPromptCacheHints(context.Background(), context_manager.PromptCacheHints{
		EphemeralParts: []context_manager.PromptCachePartHint{{MessageIndex: 0, PartIndex: 0}},
	}), []llms.MessageContent{
		{Role: llms.ChatMessageTypeSystem, Parts: []llms.ContentPart{llms.TextPart("stable"), llms.TextPart("dynamic")}},
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}},
	}); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	systemContent := decodePromptCacheSystemContent(t, captured.Messages[0].Content)
	if len(systemContent) != 2 {
		t.Fatalf("expected split system content, got %#v", systemContent)
	}
	if got := systemContent[0].CacheControl; got == nil || got.Type != "ephemeral" {
		t.Fatalf("first system block should carry cache_control ephemeral, got %#v", systemContent[0])
	}
}

func TestCachedOpenRouterPromptCachePolicyReadsDiskCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "provider-model-metadata.json")
	manager := NewModelManager(
		ModelConfig{Provider: "openrouter", Model: "vendor/model-x", APIKey: "k", BaseURL: "http://127.0.0.1:1"},
		ProxyConfig{},
		WithProviderModelMetadataCachePath(cachePath),
	)

	key := manager.providerModelSpecCacheKey()
	cache := providerModelMetadataCacheFile{
		Version: providerModelMetadataCacheVersion,
		Entries: map[string]providerModelMetadataCacheEntry{
			key: {
				Provider:          "openrouter",
				Model:             "vendor/model-x",
				Endpoint:          openRouterModelsURL("http://127.0.0.1:1"),
				PromptCachePolicy: string(PromptCachePolicyExplicit),
			},
		},
	}
	encoded, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	if err := writeFileAtomic(cachePath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	if got := manager.cachedOpenRouterPromptCachePolicy(); got != PromptCachePolicyExplicit {
		t.Fatalf("cached policy = %q, want explicit", got)
	}
}

func TestBuildAgentProfileSystemPromptSectionsExposeCacheHints(t *testing.T) {
	originalNow := promptNow
	promptNow = func() time.Time {
		return time.Date(2026, time.June, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	t.Cleanup(func() { promptNow = originalNow })

	profile := buildAgentProfile(AgentConfig{}, ResolvedSkills{}, nil)
	sections := profile.SystemPromptSections()
	if len(sections) != 1 {
		t.Fatalf("sections = %d, want single stable section without active skills", len(sections))
	}
	if !sections[0].CacheEphemeral {
		t.Fatalf("stable section should be cacheable")
	}
	wantDate := "Current date: 2026-06-01"
	if !strings.Contains(sections[0].Text, wantDate) {
		t.Fatalf("stable section missing %q: %s", wantDate, sections[0].Text)
	}

	manager := context_manager.NewContextManager()
	preparePlannerContextManager(manager, sections, nil, "hello", nil)
	messages, hints := manager.ConvertToStandardMessageListWithCacheHints()
	if len(messages[0].Parts) != 1 {
		t.Fatalf("system parts = %d, want single stable part", len(messages[0].Parts))
	}
	if !hints.ShouldCache(0, 0) {
		t.Fatalf("context hints should mark stable system part as cacheable: %#v", hints.EphemeralParts)
	}
}
