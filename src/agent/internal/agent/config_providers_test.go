package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderReferences(t *testing.T) {
	tests := []struct {
		name           string
		config         string
		wantProvider   string // Expected resolved provider type
		wantAPIKey     string
		wantBaseURL    string
		wantModel      string
		wantErr        bool
		errContains    string
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
