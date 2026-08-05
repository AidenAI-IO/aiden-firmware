package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
[providers.my-openai]
provider = "openai"
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
[providers.my-ollama]
provider = "ollama"
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
[providers.my-openai]
provider = "openai"
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
[providers.work]
provider = "openai"
api_key = "sk-work-key"

[providers.personal]
provider = "kimi"
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
[providers.broken]
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
[providers.invalid]
provider = "unknown-provider"
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

func TestModelTextProviderReferences(t *testing.T) {
	config := `
[providers.openai-main]
provider = "openai"
api_key = "sk-openai-key"

[providers.kimi-main]
provider = "kimi"
api_key = "sk-kimi-key"

[model]
provider = "openai-main"
model = "gpt-4o"

[model_text]
provider = "kimi-main"
model = "moonshot-v1-8k"
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agent.toml")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify model
	if cfg.Model.Provider != "openai" {
		t.Errorf("model.provider = %q, want %q", cfg.Model.Provider, "openai")
	}
	if cfg.Model.APIKey != "sk-openai-key" {
		t.Errorf("model.api_key = %q, want %q", cfg.Model.APIKey, "sk-openai-key")
	}

	// Verify model_text
	if cfg.ModelText.Provider != "kimi" {
		t.Errorf("model_text.provider = %q, want %q", cfg.ModelText.Provider, "kimi")
	}
	if cfg.ModelText.APIKey != "sk-kimi-key" {
		t.Errorf("model_text.api_key = %q, want %q", cfg.ModelText.APIKey, "sk-kimi-key")
	}
}

func TestProviderValidation(t *testing.T) {
	tests := []struct {
		name        string
		provider    Provider
		provName    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid openai provider",
			provName: "my-openai",
			provider: Provider{
				Provider: "openai",
				APIKey:   "sk-test",
			},
			wantErr: false,
		},
		{
			name:     "valid ollama provider with base_url",
			provName: "my-ollama",
			provider: Provider{
				Provider: "ollama",
				BaseURL:  "http://localhost:11434",
			},
			wantErr: false,
		},
		{
			name:     "missing provider type",
			provName: "broken",
			provider: Provider{
				APIKey: "sk-test",
			},
			wantErr:     true,
			errContains: "provider type is required",
		},
		{
			name:     "invalid provider type",
			provName: "invalid",
			provider: Provider{
				Provider: "unknown-provider",
				APIKey:   "sk-test",
			},
			wantErr:     true,
			errContains: "unsupported provider type",
		},
		{
			name:     "empty provider name",
			provName: "",
			provider: Provider{
				Provider: "openai",
			},
			wantErr:     true,
			errContains: "provider name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProvider(tt.provName, tt.provider)
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
// provider-reference expansion and the base_url whitelist (openai and ollama
// only). Regression test: clearNonAllowedModelBaseURL used to run BEFORE the
// reference was expanded, so it compared the whitelist against a [providers]
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
[providers.my-openai]
provider = "openai"
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
[providers.local]
provider = "ollama"
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
[providers.my-router]
provider = "openrouter"
api_key = "sk-x"
base_url = "https://sneaky.example.com/v1"

[model]
provider = "my-router"
model = "anthropic/claude-opus-4.8"
`,
			wantBaseURL: "",
		},
		{
			name: "model base_url is dropped for a named volcengine provider",
			config: `
[providers.ark]
provider = "volcengine"
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
// provider that names no [providers] section and is not a provider type --
// a typo, or a reference left behind after the section was deleted. It used to
// fall through as a "direct provider type" and pass both LoadRuntimeConfig and
// Validate(), only failing later when the model client was built. The config
// web UI could produce exactly this state, so it must be caught at load.
func TestDanglingProviderReferenceRejected(t *testing.T) {
	t.Run("model provider naming a missing section", func(t *testing.T) {
		_, err := loadProviderConfig(t, `
[providers.exists]
provider = "openai"
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

	t.Run("model_text provider naming a missing section", func(t *testing.T) {
		_, err := loadProviderConfig(t, `
[providers.exists]
provider = "openai"
api_key = "sk-x"

[model]
provider = "exists"
model = "gpt-4o"

[model_text]
provider = "typo-here"
model = "gpt-4o"
`)
		if err == nil {
			t.Fatal("expected a dangling model_text provider reference to be rejected")
		}
		if !strings.Contains(err.Error(), "model_text.provider") {
			t.Errorf("error %q does not mention model_text.provider", err.Error())
		}
	})

	t.Run("bare provider type with no providers section still loads", func(t *testing.T) {
		// Backward compatibility: configs predating [providers] must keep
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
// over the referenced provider's for every inherited field. Only api_key was
// covered before; applyProviderToModel has an identical empty-check per field.
func TestProviderReferencePrecedence(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[providers.p]
provider = "openai"
api_key = "sk-from-provider"
token_env = "PROVIDER_ENV"
base_url = "https://provider.example.com/v1"

[model]
provider = "p"
model = "gpt-4o"
api_key = "sk-explicit"
token_env = "EXPLICIT_ENV"
base_url = "https://explicit.example.com/v1"
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Model.APIKey != "sk-explicit" {
		t.Errorf("api_key = %q, want sk-explicit", cfg.Model.APIKey)
	}
	if cfg.Model.TokenEnv != "EXPLICIT_ENV" {
		t.Errorf("token_env = %q, want EXPLICIT_ENV", cfg.Model.TokenEnv)
	}
	if cfg.Model.BaseURL != "https://explicit.example.com/v1" {
		t.Errorf("base_url = %q, want the explicit value", cfg.Model.BaseURL)
	}
}

// TestProviderNameShadowingProviderType documents the resolution order when a
// section is named exactly like a built-in provider type: the named section
// wins. Locked so the precedence cannot change silently.
func TestProviderNameShadowingProviderType(t *testing.T) {
	cfg, err := loadProviderConfig(t, `
[providers.openai]
provider = "ollama"
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
