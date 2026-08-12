package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

type recordingStorageWriteGate struct {
	allow bool
}

func newTestLLMRawHTTPLogger(logDir, sessionID string) *llmRawHTTPLogger {
	bindings := NewModelRuntimeBindings()
	bindings.SetSessionIDProvider(func() string { return sessionID })
	return newLLMRawHTTPLogger(logDir, bindings)
}

func (g *recordingStorageWriteGate) AllowWrite(StorageCapability) bool {
	return g.allow
}

func (g *recordingStorageWriteGate) HandleWriteError(error) bool {
	return false
}

func TestModelRuntimeBindingsCanReplaceDependencies(t *testing.T) {
	bindings := NewModelRuntimeBindings()
	bindings.SetSessionIDProvider(func() string { return "session-a" })
	firstGate := &recordingStorageWriteGate{allow: false}
	bindings.SetStorageWriteGate(firstGate)

	if got := bindings.CurrentSessionID(); got != "session-a" {
		t.Fatalf("CurrentSessionID() = %q, want session-a", got)
	}
	if got := bindings.StorageWriteGate(); got != firstGate {
		t.Fatalf("StorageWriteGate() = %T %p, want first gate %p", got, got, firstGate)
	}

	bindings.SetSessionIDProvider(func() string { return "session-b" })
	secondGate := &recordingStorageWriteGate{allow: true}
	bindings.SetStorageWriteGate(secondGate)

	if got := bindings.CurrentSessionID(); got != "session-b" {
		t.Fatalf("CurrentSessionID() after replacement = %q, want session-b", got)
	}
	if got := bindings.StorageWriteGate(); got != secondGate {
		t.Fatalf("StorageWriteGate() after replacement = %T %p, want second gate %p", got, got, secondGate)
	}
}

func TestModelRuntimeBindingsNormalizesTypedNilStorageWriteGate(t *testing.T) {
	bindings := NewModelRuntimeBindings()
	var monitor *StorageMonitor
	bindings.SetStorageWriteGate(monitor)
	if gate := bindings.StorageWriteGate(); gate != nil {
		t.Fatalf("StorageWriteGate() = %T, want nil", gate)
	}
}

func TestLLMRawHTTPLoggerUsesStableRuntimeBindings(t *testing.T) {
	dir := t.TempDir()
	bindings := NewModelRuntimeBindings()
	bindings.SetSessionIDProvider(func() string { return "session-a" })
	bindings.SetStorageWriteGate(&recordingStorageWriteGate{allow: false})
	logger := newLLMRawHTTPLogger(dir, bindings)

	ctx := logger.BeginScope(context.Background())
	if err := logger.Log(ctx, RawHTTPLogEntry{Model: "model", Kind: "request", StatusCode: http.StatusOK, Raw: `{}`}); err != nil {
		t.Fatalf("Log() while writes disabled error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "llm-http-*.log")); err != nil || len(matches) != 0 {
		t.Fatalf("logs while writes disabled = %#v, err = %v", matches, err)
	}

	bindings.SetStorageWriteGate(&recordingStorageWriteGate{allow: true})
	bindings.SetSessionIDProvider(func() string { return "session-b" })
	ctx = logger.BeginScope(context.Background())
	if err := logger.Log(ctx, RawHTTPLogEntry{Model: "model", Kind: "request", StatusCode: http.StatusOK, Raw: `{}`}); err != nil {
		t.Fatalf("Log() after bindings replacement error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "llm-http-session-b.log")); err != nil || len(matches) != 1 {
		t.Fatalf("session-b logs = %#v, err = %v", matches, err)
	}
}

func TestProviderModelsShareManagerRuntimeBindingsWithRawLogger(t *testing.T) {
	for _, cfg := range []ModelConfig{
		{Provider: "openai", Model: "test-model", APIKey: "test-key", LogRawHTTP: true},
		{Provider: "anthropic", Model: "test-model", APIKey: "test-key", LogRawHTTP: true},
	} {
		t.Run(cfg.Provider, func(t *testing.T) {
			manager := NewModelManager(cfg, ProxyConfig{}, WithLLMRawHTTPLogDir(t.TempDir()))
			model, err := manager.get()
			if err != nil {
				t.Fatalf("get() error = %v", err)
			}

			var rawLogger RawHTTPLogger
			switch model := model.(type) {
			case *openAICompatibleModel:
				rawLogger = model.rawLogger
			case *anthropicModel:
				rawLogger = model.rawLogger
			default:
				t.Fatalf("model = %T, want provider model", model)
			}
			logger, ok := rawLogger.(*llmRawHTTPLogger)
			if !ok {
				t.Fatalf("raw logger = %T, want *llmRawHTTPLogger", rawLogger)
			}
			if logger.bindings != manager.bindings() {
				t.Fatal("provider logger does not share the manager runtime bindings")
			}
		})
	}
}

func TestModelManagerSetSessionIDProviderAfterBuildUpdatesOpenRouter(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-session-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	manager := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "test-model",
		APIKey:   "test-key",
		BaseURL:  server.URL,
	}, ProxyConfig{})
	model, err := manager.get()
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	manager.SetSessionIDProvider(func() string { return "session-after-build" })

	if _, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}}); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if gotHeader != "session-after-build" {
		t.Fatalf("x-session-id = %q, want session-after-build", gotHeader)
	}
}

func TestModelManagerUpdatesRawLoggerDependenciesAfterBuild(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	logDir := t.TempDir()
	manager := NewModelManager(ModelConfig{
		Provider:   "openai",
		Model:      "test-model",
		BaseURL:    server.URL,
		LogRawHTTP: true,
	}, ProxyConfig{}, WithLLMRawHTTPLogDir(logDir))
	model, err := manager.get()
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}

	manager.SetSessionIDProvider(func() string { return "session-a" })
	storageConfig := DefaultStorageConfig()
	storageConfig.Cleanup.Enabled = false
	monitor := NewStorageMonitor(storageConfig, &sequenceStorageSampler{
		samples: []StorageSample{storageSampleWithAvailableMB(8)},
	}, nil, nil, nil)
	manager.SetStorageMonitor(monitor)
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("CheckAndRemediate() error = %v", err)
	}
	callModel := func() {
		t.Helper()
		if _, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("hello")},
		}}); err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
	}

	callModel()
	if matches, err := filepath.Glob(filepath.Join(logDir, "llm-http-*.log")); err != nil || len(matches) != 0 {
		t.Fatalf("logs while writes disabled = %#v, err = %v", matches, err)
	}

	manager.SetStorageMonitor(nil)
	callModel()
	if matches, err := filepath.Glob(filepath.Join(logDir, "llm-http-session-a.log")); err != nil || len(matches) != 1 {
		t.Fatalf("session-a logs after clearing gate = %#v, err = %v", matches, err)
	}

	manager.SetSessionIDProvider(func() string { return "session-b" })
	callModel()
	if matches, err := filepath.Glob(filepath.Join(logDir, "llm-http-session-b.log")); err != nil || len(matches) != 1 {
		t.Fatalf("session-b logs after provider replacement = %#v, err = %v", matches, err)
	}
}
