package agent

import (
	"aiden-agent/internal/agent/model"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitForModelSpec(t *testing.T, mgr *ModelManager, want model.ModelSpec) model.ModelSpec {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		got := mgr.Spec()
		if got.ContextWindow == want.ContextWindow && got.MaxOutput == want.MaxOutput {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("Spec() = %+v, want %+v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLookupModelSpecKnownModels(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		wantContext   int
		wantMaxOutput int
	}{
		{"openrouter gemini flash", "openrouter", "google/gemini-3.5-flash", 1_048_576, 65_536},
		{"claude sonnet 3.5", "openrouter", "anthropic/claude-3.5-sonnet", 200_000, 8_192},
		{"bytedance seed lite", "openrouter", "bytedance-seed/seed-2.0-lite", 128_000, 8_192},
		{"gpt-4o", "openai", "openai/gpt-4o", 128_000, 16_384},
		{"gpt-5.5 prefixed", "openai", "openai/gpt-5.5", 1_050_000, 128_000},
		{"gpt-5.5 bare", "openai", "gpt-5.5", 1_050_000, 128_000},
		{"gpt-5.5 pro bare", "openai", "gpt-5.5-pro", 1_050_000, 128_000},
		{"gpt-5.4 prefixed", "openai", "openai/gpt-5.4", 1_050_000, 128_000},
		{"gpt-5.4 bare", "openai", "gpt-5.4", 1_050_000, 128_000},
		{"gpt-5.4 mini bare", "openai", "gpt-5.4-mini", 400_000, 128_000},
		{"gpt-5.4 nano bare", "openai", "gpt-5.4-nano", 400_000, 128_000},
		{"claude opus 4.8 bare", "anthropic", "claude-opus-4.8", 1_000_000, 64_000},
		{"claude sonnet 4.6 prefixed", "openrouter", "anthropic/claude-sonnet-4.6", 1_000_000, 64_000},
		{"claude haiku 4.5 bare", "anthropic", "claude-haiku-4.5", 200_000, 64_000},
		{"gemini 3.5 pro bare", "google", "gemini-3.5-pro", 1_048_576, 65_536},
		{"kimi k3 bare", "openai", "kimi-k3", 1_048_576, 131_072},
		{"kimi k3 prefixed", "openrouter", "moonshotai/kimi-k3", 1_048_576, 131_072},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := LookupModelSpec(tt.provider, tt.model)
			if !ok {
				t.Fatalf("LookupModelSpec(%q, %q): expected ok, got !ok", tt.provider, tt.model)
			}
			if spec.ContextWindow != tt.wantContext {
				t.Errorf("ContextWindow = %d, want %d", spec.ContextWindow, tt.wantContext)
			}
			if spec.MaxOutput != tt.wantMaxOutput {
				t.Errorf("MaxOutput = %d, want %d", spec.MaxOutput, tt.wantMaxOutput)
			}
		})
	}
}

func TestLookupModelSpecCaseInsensitive(t *testing.T) {
	spec, ok := LookupModelSpec("OpenRouter", "GOOGLE/Gemini-3.5-Flash")
	if !ok {
		t.Fatalf("expected case-insensitive lookup to succeed")
	}
	if spec.ContextWindow != 1_048_576 {
		t.Errorf("ContextWindow = %d, want 1_048_576", spec.ContextWindow)
	}
}

func TestLookupModelSpecUnknownModelReturnsNotOK(t *testing.T) {
	if spec, ok := LookupModelSpec("openrouter", "vendor/unknown-model-9001"); ok {
		t.Fatalf("expected !ok for unknown model, got spec=%+v", spec)
	}
	if spec, ok := LookupModelSpec("openai", ""); ok {
		t.Fatalf("expected !ok for empty model, got spec=%+v", spec)
	}
}

func TestModelManagerSpecUsesConfig(t *testing.T) {
	mgr := NewModelManager(ModelConfig{Provider: "openrouter", Model: "google/gemini-3.5-flash"}, ProxyConfig{})
	if got := mgr.Spec().ContextWindow; got != 1_048_576 {
		t.Errorf("ModelManager.Spec().ContextWindow = %d, want 1_048_576", got)
	}

	unknown := NewModelManager(ModelConfig{Provider: "fake", Model: "vendor/no-such-model"}, ProxyConfig{})
	if got := unknown.Spec().ContextWindow; got != 0 {
		t.Errorf("unknown model: ContextWindow = %d, want 0 (caller falls back to yaml default)", got)
	}
}

func TestModelManagerSpecUsesExplicitConfigOverrides(t *testing.T) {
	mgr := NewModelManager(ModelConfig{
		Provider:             "openrouter",
		Model:                "google/gemini-3.5-flash",
		ContextWindow:        64_000,
		ModelMaxOutputTokens: 4_096,
	}, ProxyConfig{})

	spec := mgr.Spec()
	if spec.ContextWindow != 64_000 {
		t.Errorf("ContextWindow = %d, want 64_000", spec.ContextWindow)
	}
	if spec.MaxOutput != 4_096 {
		t.Errorf("MaxOutput = %d, want 4_096", spec.MaxOutput)
	}
}

func TestModelManagerSpecAllowsPartialExplicitConfigOverrides(t *testing.T) {
	mgr := NewModelManager(ModelConfig{
		Provider:      "openrouter",
		Model:         "google/gemini-3.5-flash",
		ContextWindow: 64_000,
	}, ProxyConfig{})

	spec := mgr.Spec()
	if spec.ContextWindow != 64_000 {
		t.Errorf("ContextWindow = %d, want 64_000", spec.ContextWindow)
	}
	if spec.MaxOutput != 65_536 {
		t.Errorf("MaxOutput = %d, want registry value 65_536", spec.MaxOutput)
	}
}

func TestModelManagerSpecUsesRegistryBeforeProviderMetadata(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "provider_model_metadata.json")
	cfg := ModelConfig{
		Provider: "openrouter",
		Model:    "google/gemini-3.5-flash",
		BaseURL:  server.URL + "/api/v1",
		APIKey:   "test-token",
	}
	cacheWriter := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))
	if err := cacheWriter.writeProviderModelSpecCache(model.ModelSpec{ContextWindow: 524_288, MaxOutput: 16_384}); err != nil {
		t.Fatalf("writeProviderModelSpecCache: %v", err)
	}

	mgr := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))

	spec := mgr.Spec()
	if spec.ContextWindow != 1_048_576 {
		t.Fatalf("Spec().ContextWindow = %d, want registry value 1_048_576", spec.ContextWindow)
	}
	if spec.MaxOutput != 65_536 {
		t.Fatalf("Spec().MaxOutput = %d, want registry value 65_536", spec.MaxOutput)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestModelManagerSpecFetchesOpenRouterContextWindowWhenRegistryUnknown(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("path = %s, want /api/v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"data": [
				{"id": "other/model", "context_length": 1024},
				{
					"id": "vendor/new-model",
					"context_length": 1000000,
					"top_provider": {
						"context_length": 524288,
						"max_completion_tokens": 16384
					}
				}
			]
		}`)
	}))
	defer server.Close()

	mgr := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/new-model",
		BaseURL:  server.URL + "/api/v1",
		APIKey:   "test-token",
	}, ProxyConfig{})

	for i := 0; i < 2; i++ {
		waitForModelSpec(t, mgr, model.ModelSpec{ContextWindow: 524_288, MaxOutput: 16_384})
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 cached request", got)
	}
}

func TestModelManagerPrefetchesProviderMetadataWithoutBlockingSpec(t *testing.T) {
	var requests atomic.Int64
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var closeStarted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if closeStarted.CompareAndSwap(false, true) {
			close(requestStarted)
		}
		<-releaseResponse
		http.Error(w, "provider unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	mgr := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/slow-model",
		BaseURL:  server.URL + "/api/v1",
		APIKey:   "test-token",
	}, ProxyConfig{})
	mgr.prefetchProviderModelSpecIfNeeded()

	select {
	case <-requestStarted:
	case <-time.After(500 * time.Millisecond):
		close(releaseResponse)
		t.Fatal("provider metadata request was not prefetched")
	}

	startedAt := time.Now()
	spec := mgr.Spec()
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		close(releaseResponse)
		t.Fatalf("Spec() blocked for %v waiting on provider metadata", elapsed)
	}
	if spec.ContextWindow != 0 || spec.MaxOutput != 0 {
		close(releaseResponse)
		t.Fatalf("Spec() = %+v, want zero while provider metadata is pending", spec)
	}

	close(releaseResponse)
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestNewRuntimePrefetchesProviderMetadataBeforeFirstRun(t *testing.T) {
	var requests atomic.Int64
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var closeStarted atomic.Bool
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(releaseResponse)
		}
	}
	defer release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("path = %s, want /api/v1/models", r.URL.Path)
		}
		if closeStarted.CompareAndSwap(false, true) {
			close(requestStarted)
		}
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	rt, err := NewRuntime(Config{
		Model: ModelConfig{
			Provider: "openrouter",
			Model:    "vendor/runtime-prefetch-model",
			BaseURL:  server.URL + "/api/v1",
			APIKey:   "test-token",
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer rt.Close()

	select {
	case <-requestStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("provider metadata request was not prefetched during runtime initialization")
	}

	release()
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestModelManagerSpecReadsProviderMetadataFromFileCache(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"data": [
				{
					"id": "vendor/cached-model",
					"context_length": 262144,
					"top_provider": {
						"context_length": 131072,
						"max_completion_tokens": 4096
					}
				}
			]
		}`)
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "provider_model_metadata.json")
	cfg := ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/cached-model",
		BaseURL:  server.URL + "/api/v1",
		APIKey:   "test-token",
	}

	first := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))
	waitForModelSpec(t, first, model.ModelSpec{ContextWindow: 131_072, MaxOutput: 4_096})

	second := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))
	if spec := second.Spec(); spec.ContextWindow != 131_072 || spec.MaxOutput != 4_096 {
		t.Fatalf("second Spec() = %+v, want cached provider metadata", spec)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("ReadFile(cache): %v", err)
	}
	if strings.Contains(string(data), "test-token") {
		t.Fatalf("cache file leaked API key: %s", string(data))
	}
}

func TestModelManagerSpecReadsProviderMaxOutputOnlyFromFileCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "provider_model_metadata.json")
	cfg := ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/max-output-only-cached-model",
		BaseURL:  "http://provider.test/api/v1",
		APIKey:   "test-token",
	}

	cacheWriter := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))
	if err := cacheWriter.writeProviderModelSpecCache(model.ModelSpec{MaxOutput: 16_384}); err != nil {
		t.Fatalf("writeProviderModelSpecCache: %v", err)
	}

	mgr := NewModelManager(cfg, ProxyConfig{}, WithProviderModelMetadataCachePath(cachePath))
	if spec := mgr.Spec(); spec.ContextWindow != 0 || spec.MaxOutput != 16_384 {
		t.Fatalf("Spec() = %+v, want cached max-output-only provider metadata", spec)
	}
}

func TestModelManagerSpecFetchesProviderMaxOutputWhenContextWindowOverrideSet(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"data": [
				{
					"id": "vendor/context-override-model",
					"context_length": 262144,
					"top_provider": {
						"context_length": 131072,
						"max_completion_tokens": 16384
					}
				}
			]
		}`)
	}))
	defer server.Close()

	mgr := NewModelManager(ModelConfig{
		Provider:      "openrouter",
		Model:         "vendor/context-override-model",
		BaseURL:       server.URL + "/api/v1",
		APIKey:        "test-token",
		ContextWindow: 64_000,
	}, ProxyConfig{})

	spec := waitForModelSpec(t, mgr, model.ModelSpec{ContextWindow: 64_000, MaxOutput: 16_384})
	if spec.ContextWindow != 64_000 {
		t.Fatalf("Spec().ContextWindow = %d, want explicit override 64_000", spec.ContextWindow)
	}
	if spec.MaxOutput != 16_384 {
		t.Fatalf("Spec().MaxOutput = %d, want provider metadata 16_384", spec.MaxOutput)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestModelManagerNeedsProviderMetadataWhenOnlyMaxOutputMissing(t *testing.T) {
	mgr := NewModelManager(ModelConfig{
		Provider:      "openrouter",
		Model:         "vendor/context-override-model",
		ContextWindow: 64_000,
	}, ProxyConfig{})

	if !mgr.needsProviderModelMetadata() {
		t.Fatal("needsProviderModelMetadata() = false, want true when max output is still auto")
	}
}

func TestModelManagerRetriesProviderMetadataAfterFailure(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary provider failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"data": [
				{
					"id": "vendor/retry-model",
					"context_length": 262144,
					"top_provider": {
						"context_length": 131072,
						"max_completion_tokens": 4096
					}
				}
			]
		}`)
	}))
	defer server.Close()

	mgr := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "vendor/retry-model",
		BaseURL:  server.URL + "/api/v1",
		APIKey:   "test-token",
	}, ProxyConfig{})

	waitForModelSpec(t, mgr, model.ModelSpec{ContextWindow: 131_072, MaxOutput: 4_096})
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 with retry after failure", got)
	}
}

func TestModelManagerSpecFetchesOllamaContextWindowWhenAuto(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/show" {
			t.Errorf("path = %s, want /api/show", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll request body: %v", err)
		}
		if !strings.Contains(string(body), `"model":"gemma4:latest"`) {
			t.Errorf("body = %s, want model name", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model_info": {
				"gemma4.context_length": 131072
			},
			"parameters": "num_ctx 8192\nnum_predict -1"
		}`)
	}))
	defer server.Close()

	mgr := NewModelManager(ModelConfig{
		Provider: "ollama",
		Model:    "gemma4:latest",
		BaseURL:  server.URL,
	}, ProxyConfig{})

	for i := 0; i < 2; i++ {
		waitForModelSpec(t, mgr, model.ModelSpec{ContextWindow: 8_192})
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 cached request", got)
	}
}
