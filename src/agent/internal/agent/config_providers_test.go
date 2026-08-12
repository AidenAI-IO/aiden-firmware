package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestModelProviderCanonicalTOML(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[model_providers.work]
type = "openai"
api_key = "sk-work"

[model]
provider = "work"
model = "gpt-4o"
`)
	if err != nil {
		t.Fatalf("load canonical model provider config: %v", err)
	}
	if cfg.Model.Provider != "openai" || cfg.Model.APIKey != "sk-work" {
		t.Fatalf("resolved model = provider %q api_key %q, want openai/sk-work", cfg.Model.Provider, cfg.Model.APIKey)
	}

	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(Config{
		ModelProviders: map[string]ModelProvider{
			"work": {Type: "openai", APIKey: "sk-work"},
		},
		Model: ModelConfig{Provider: "work", Model: "gpt-4o"},
	}); err != nil {
		t.Fatalf("encode config: %v", err)
	}
	output := encoded.String()
	if !strings.Contains(output, "[model_providers.work]") || !strings.Contains(output, `type = "openai"`) {
		t.Errorf("canonical model provider missing from TOML:\n%s", output)
	}
	if strings.Contains(output, "[providers.") {
		t.Errorf("legacy model provider shape leaked into TOML:\n%s", output)
	}
}

func TestLegacyModelProviderTOMLIsRejected(t *testing.T) {
	_, err := loadProviderConfig(t, `
[providers.work]
provider = "openai"
api_key = "sk-work"

[model]
provider = "work"
model = "gpt-4o"
`)
	if err == nil {
		t.Fatal("expected the legacy providers namespace to be rejected")
	}
	if !strings.Contains(err.Error(), "providers") || !strings.Contains(err.Error(), "model_providers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProviderRecordCompatibilityRejectsNonStringFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "model provider",
			body: `
[model_providers.work]
type = "openai"
api_key = 123

[model]
provider = "work"
model = "gpt-4o"
`,
		},
		{
			name: "tts provider",
			body: `
[tts_providers.voice]
type = "fish-audio"
api_key = 123

[model]
provider = "openai"
model = "gpt-4o"
`,
		},
		{
			name: "stt provider",
			body: `
[stt_providers.speech]
type = "tencent-asr"
secret_key = 123

[model]
provider = "openai"
model = "gpt-4o"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadProviderConfig(t, tt.body); err == nil {
				t.Fatal("expected a non-string provider field to fail TOML decoding")
			}
		})
	}
}

func TestModelProviderCanonicalTypeTakesPrecedence(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[model_providers.work]
type = "openai"
provider = "kimi"
api_key = "sk-canonical"

[model]
provider = "work"
model = "gpt-4o"
`)
	if err != nil {
		t.Fatalf("load model provider config: %v", err)
	}
	if cfg.Model.Provider != "openai" || cfg.Model.APIKey != "sk-canonical" || cfg.Model.BaseURL != "" {
		t.Fatalf("canonical provider did not win: %#v", cfg.Model)
	}
}

func TestProviderReferences(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		wantProvider string // Expected resolved provider type
		wantAPIKey   string
		wantBaseURL  string
		wantModel    string
		wantErr      bool
		errContains  string
	}{
		{
			name: "provider reference resolves correctly",
			config: `
[model_providers.my-openai]
type = "openai"
api_key = "sk-test-key"

[model]
provider = "my-openai"
model = "gpt-4o"
`,
			wantProvider: "openai",
			wantAPIKey:   "sk-test-key",
			wantModel:    "gpt-4o",
		},
		{
			name: "provider with base_url",
			config: `
[model_providers.my-ollama]
type = "ollama"
base_url = "http://localhost:11434"

[model]
provider = "my-ollama"
model = "qwen2.5:14b"
`,
			wantProvider: "ollama",
			wantBaseURL:  "http://localhost:11434",
			wantModel:    "qwen2.5:14b",
		},
		{
			name: "model config overrides provider config",
			config: `
[model_providers.my-openai]
type = "openai"
api_key = "sk-provider-key"
base_url = "https://api.openai.com/v1"

[model]
provider = "my-openai"
model = "gpt-4o"
api_key = "sk-override-key"
`,
			wantProvider: "openai",
			wantAPIKey:   "sk-override-key",
			wantBaseURL:  "https://api.openai.com/v1",
			wantModel:    "gpt-4o",
		},
		{
			name: "direct provider type (backward compatibility)",
			config: `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-direct-key"
`,
			wantProvider: "openai",
			wantAPIKey:   "sk-direct-key",
			wantModel:    "gpt-4o",
		},
		{
			name: "multiple providers defined, one used",
			config: `
[model_providers.work]
type = "openai"
api_key = "sk-work-key"

[model_providers.personal]
type = "kimi"
api_key = "sk-personal-key"

[model]
provider = "work"
model = "gpt-4o"
`,
			wantProvider: "openai",
			wantAPIKey:   "sk-work-key",
			wantModel:    "gpt-4o",
		},
		{
			name: "provider missing provider type",
			config: `
[model_providers.broken]
api_key = "sk-test-key"

[model]
provider = "broken"
model = "gpt-4o"
`,
			wantErr:     true,
			errContains: "no provider type specified",
		},
		{
			name: "invalid provider type",
			config: `
[model_providers.invalid]
type = "unknown-provider"
api_key = "sk-test-key"

[model]
provider = "invalid"
model = "gpt-4o"
`,
			wantErr:     true,
			errContains: "unsupported provider type",
		},
		{
			name: "provider reference not found falls back to direct",
			config: `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-test-key"
`,
			wantProvider: "openai",
			wantAPIKey:   "sk-test-key",
			wantModel:    "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "agent.toml")
			if err := os.WriteFile(configPath, []byte(tt.config), 0644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			// Load the config
			cfg, err := LoadRuntimeConfig(configPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify the resolved configuration
			if cfg.Model.Provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", cfg.Model.Provider, tt.wantProvider)
			}
			if cfg.Model.APIKey != tt.wantAPIKey {
				t.Errorf("api_key = %q, want %q", cfg.Model.APIKey, tt.wantAPIKey)
			}
			if cfg.Model.BaseURL != tt.wantBaseURL {
				t.Errorf("base_url = %q, want %q", cfg.Model.BaseURL, tt.wantBaseURL)
			}
			if cfg.Model.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", cfg.Model.Model, tt.wantModel)
			}
		})
	}
}

func TestProviderValidation(t *testing.T) {
	tests := []struct {
		name        string
		provider    ModelProvider
		provName    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid openai provider",
			provName: "my-openai",
			provider: ModelProvider{
				Type:   "openai",
				APIKey: "sk-test",
			},
			wantErr: false,
		},
		{
			name:     "valid ollama provider with base_url",
			provName: "my-ollama",
			provider: ModelProvider{
				Type:    "ollama",
				BaseURL: "http://localhost:11434",
			},
			wantErr: false,
		},
		{
			name:     "missing provider type",
			provName: "broken",
			provider: ModelProvider{
				APIKey: "sk-test",
			},
			wantErr:     true,
			errContains: "provider type is required",
		},
		{
			name:     "invalid provider type",
			provName: "invalid",
			provider: ModelProvider{
				Type:   "unknown-provider",
				APIKey: "sk-test",
			},
			wantErr:     true,
			errContains: "unsupported provider type",
		},
		{
			name:     "empty provider name",
			provName: "",
			provider: ModelProvider{
				Type: "openai",
			},
			wantErr:     true,
			errContains: "provider name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModelProvider(tt.provName, tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// loadProviderConfig writes body to a temp agent.toml and loads it as the
// daemon would.
func loadProviderConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadRuntimeConfig(path)
}

// TestProviderReferenceBaseURLWhitelist covers the interaction between
// provider-reference expansion and the base_url whitelist (OpenAI, Anthropic,
// and Ollama only). Regression test: clearNonAllowedModelBaseURL used to run BEFORE the
// reference was expanded, so it compared the whitelist against a [model_providers]
// section NAME instead of a provider type. That broke both directions -- a
// legitimate override was dropped, and a disallowed one survived.
func TestProviderReferenceBaseURLWhitelist(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantBaseURL string
	}{
		{
			// The name "my-openai" is not in the whitelist, so the early
			// clear discarded a base_url that openai actually accepts.
			name: "model base_url survives a named openai provider",
			config: `
[model_providers.my-openai]
type = "openai"
api_key = "sk-x"

[model]
provider = "my-openai"
model = "gpt-4o"
base_url = "https://gateway.example.com/v1"
`,
			wantBaseURL: "https://gateway.example.com/v1",
		},
		{
			name: "provider base_url survives for ollama",
			config: `
[model_providers.local]
type = "ollama"
base_url = "http://127.0.0.1:11434"

[model]
provider = "local"
model = "qwen2.5:7b"
`,
			wantBaseURL: "http://127.0.0.1:11434",
		},
		{
			// The mirror image: the clear ran before expansion, so a
			// base_url inherited from the section was never checked at all.
			name: "provider base_url is dropped for openrouter",
			config: `
[model_providers.my-router]
type = "openrouter"
api_key = "sk-x"
base_url = "https://sneaky.example.com/v1"

[model]
provider = "my-router"
model = "anthropic/claude-opus-4-8"
`,
			wantBaseURL: "",
		},
		{
			name: "model base_url is dropped for a named volcengine provider",
			config: `
[model_providers.ark]
type = "volcengine"
api_key = "sk-x"

[model]
provider = "ark"
model = "doubao-seed-2-1-pro-260628"
base_url = "https://gateway.example.com/v1"
`,
			wantBaseURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadProviderConfig(t, tt.config)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Model.BaseURL != tt.wantBaseURL {
				t.Errorf("base_url = %q, want %q", cfg.Model.BaseURL, tt.wantBaseURL)
			}
		})
	}
}

// TestDanglingProviderReferenceRejected is the regression test for a [model]
// provider that names no [model_providers] section and is not a provider type --
// a typo, or a reference left behind after the section was deleted. It used to
// fall through as a "direct provider type" and pass both LoadRuntimeConfig and
// Validate(), only failing later when the model client was built. The config
// web UI could produce exactly this state, so it must be caught at load.
func TestDanglingProviderReferenceRejected(t *testing.T) {
	t.Run("model provider naming a missing section", func(t *testing.T) {
		_, err := loadProviderConfig(t, `
[model_providers.exists]
type = "openai"
api_key = "sk-x"

[model]
provider = "gone-provider"
model = "gpt-4o"
`)
		if err == nil {
			t.Fatal("expected a dangling provider reference to be rejected")
		}
		// The message should name the offending value and list what is
		// configured, so the user can see the typo.
		for _, want := range []string{"model.provider", "gone-provider", "exists"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("bare provider type with no providers section still loads", func(t *testing.T) {
		// Backward compatibility: configs predating [model_providers] must keep
		// working, so a known type is never treated as a dangling reference.
		cfg, err := loadProviderConfig(t, `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-x"
`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Model.Provider != "openai" {
			t.Errorf("provider = %q, want openai", cfg.Model.Provider)
		}
	})
}

// TestProviderReferencePrecedence pins that an explicit value on [model] wins
// over the referenced provider's for every inherited field.
func TestProviderReferencePrecedence(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[model_providers.p]
type = "openai"
api_key = "sk-from-provider"
base_url = "https://provider.example.com/v1"

[model]
provider = "p"
model = "gpt-4o"
api_key = "sk-explicit"
base_url = "https://explicit.example.com/v1"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.APIKey != "sk-explicit" {
		t.Errorf("api_key = %q, want sk-explicit", cfg.Model.APIKey)
	}
	if cfg.Model.BaseURL != "https://explicit.example.com/v1" {
		t.Errorf("base_url = %q, want the explicit value", cfg.Model.BaseURL)
	}
}

func TestProviderAPIKeyEnvironmentReference(t *testing.T) {
	t.Setenv("AIDEN_TEST_MODEL_KEY", "sk-from-env")
	cfg, err := loadProviderConfig(t, `
[model_providers.p]
type = "openai"
api_key = "$AIDEN_TEST_MODEL_KEY"

[model]
provider = "p"
model = "gpt-4o"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resolveToken(cfg.Model); got != "sk-from-env" {
		t.Errorf("resolved api_key = %q, want sk-from-env", got)
	}
}

func TestProviderTokenEnvIsIgnored(t *testing.T) {
	t.Setenv("STALE_ENV", "sk-stale")
	cfg, err := loadProviderConfig(t, `
[model_providers.p]
type = "openai"
token_env = "STALE_ENV"

[model]
provider = "p"
model = "gpt-4o"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resolveToken(cfg.Model); got != "" {
		t.Errorf("resolved api_key = %q, want token_env to be ignored", got)
	}
}

// TestProviderNameShadowingProviderType documents the resolution order when a
// section is named exactly like a built-in provider type: the named section
// wins. Locked so the precedence cannot change silently.
func TestProviderNameShadowingProviderType(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[model_providers.openai]
type = "ollama"
base_url = "http://127.0.0.1:11434"

[model]
provider = "openai"
model = "qwen2.5:7b"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.Provider != "ollama" {
		t.Errorf("provider = %q, want ollama (the named section must win)", cfg.Model.Provider)
	}
	if cfg.Model.BaseURL != "http://127.0.0.1:11434" {
		t.Errorf("base_url = %q, want the section's value", cfg.Model.BaseURL)
	}
}
