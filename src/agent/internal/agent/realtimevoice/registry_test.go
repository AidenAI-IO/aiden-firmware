package realtimevoice

import (
	"context"
	"testing"
)

func TestProviderRegistrySupportsNamedFactories(t *testing.T) {
	registry := NewProviderRegistry()
	registry.Register("test", func(ProviderConfig) Provider { return testProvider{} })
	registry.Register("nil", func(ProviderConfig) Provider { return nil })

	provider, err := registry.New(" TEST ", ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(testProvider); !ok {
		t.Fatalf("provider = %T, want testProvider", provider)
	}
	if _, err := registry.New("missing", ProviderConfig{}); err == nil {
		t.Fatal("missing provider unexpectedly resolved")
	}
	if _, err := registry.New("nil", ProviderConfig{}); err == nil {
		t.Fatal("nil provider unexpectedly resolved")
	}
}

func TestDefaultProviderRegistryContainsRealtimeProviders(t *testing.T) {
	registry := DefaultProviderRegistry()
	for _, name := range []string{"qwen", "speko", "openai", "gemini", "xai"} {
		if _, err := registry.New(name, ProviderConfig{}); err != nil {
			t.Fatalf("default registry missing %q: %v", name, err)
		}
	}
}

func TestDefaultProviderRegistryPropagatesOpenAIRealtimeProtocol(t *testing.T) {
	provider, err := DefaultProviderRegistry().New("openai", ProviderConfig{RealtimeProtocol: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	openAI, ok := provider.(OpenAIProvider)
	if !ok {
		t.Fatalf("provider = %T, want OpenAIProvider", provider)
	}
	if openAI.RealtimeProtocol != "legacy" {
		t.Fatalf("RealtimeProtocol = %q, want legacy", openAI.RealtimeProtocol)
	}
}

type testProvider struct{}

func (testProvider) Open(context.Context, SessionConfig) (Session, error) { return nil, nil }

type nativeTestProvider struct{}

func (nativeTestProvider) Open(context.Context, SessionConfig) (Session, error) { return nil, nil }
func (nativeTestProvider) NativeRealtimeReasoning(model string) bool            { return model == "thinking-1" }

func TestProviderRegistryDispatchesNativeRealtimeReasoning(t *testing.T) {
	registry := NewProviderRegistry()
	registry.Register("native", func(ProviderConfig) Provider { return nativeTestProvider{} })
	registry.Register("plain", func(ProviderConfig) Provider { return testProvider{} })

	if !registry.NativeRealtimeReasoning("native", "thinking-1") {
		t.Fatal("native provider did not report native reasoning")
	}
	if registry.NativeRealtimeReasoning("native", "plain-model") {
		t.Fatal("native provider reported native reasoning for a plain model")
	}
	if registry.NativeRealtimeReasoning("plain", "thinking-1") {
		t.Fatal("provider without the capability reported native reasoning")
	}
	if registry.NativeRealtimeReasoning("missing", "thinking-1") {
		t.Fatal("unknown provider reported native reasoning")
	}
	var nilRegistry *ProviderRegistry
	if nilRegistry.NativeRealtimeReasoning("gemini", "thinking-1") {
		t.Fatal("nil registry reported native reasoning")
	}

	gemini := DefaultProviderRegistry()
	if !gemini.NativeRealtimeReasoning("gemini", "gemini-3.8-live-extended-thinking") {
		t.Fatal("default gemini provider did not report native reasoning for extended thinking")
	}
	if gemini.NativeRealtimeReasoning("gemini", "gemini-3.1-flash-live-preview") {
		t.Fatal("default gemini provider reported native reasoning for a plain model")
	}
	if gemini.NativeRealtimeReasoning("openai", "gpt-realtime") {
		t.Fatal("default openai provider reported native reasoning")
	}
}
