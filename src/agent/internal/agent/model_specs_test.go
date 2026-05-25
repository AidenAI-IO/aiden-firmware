package agent

import "testing"

func TestLookupModelSpecKnownModels(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		wantContext   int
		wantMaxOutput int
	}{
		{"openrouter gemini flash", "openrouter", "google/gemini-3.5-flash", 1_000_000, 8_192},
		{"claude sonnet 3.5", "openrouter", "anthropic/claude-3.5-sonnet", 200_000, 8_192},
		{"bytedance seed lite", "openrouter", "bytedance-seed/seed-2.0-lite", 128_000, 8_192},
		{"gpt-4o", "openai", "openai/gpt-4o", 128_000, 16_384},
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
	if spec.ContextWindow != 1_000_000 {
		t.Errorf("ContextWindow = %d, want 1_000_000", spec.ContextWindow)
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
	if got := mgr.Spec().ContextWindow; got != 1_000_000 {
		t.Errorf("ModelManager.Spec().ContextWindow = %d, want 1_000_000", got)
	}

	unknown := NewModelManager(ModelConfig{Provider: "fake", Model: "vendor/no-such-model"}, ProxyConfig{})
	if got := unknown.Spec().ContextWindow; got != 0 {
		t.Errorf("unknown model: ContextWindow = %d, want 0 (caller falls back to yaml default)", got)
	}
}
