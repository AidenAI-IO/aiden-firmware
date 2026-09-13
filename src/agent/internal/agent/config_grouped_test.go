package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigReadsGroupedTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	const source = `
[basic_settings.device]
device_type = "Android"

[basic_settings.device.hid]
keyboard_layout = "azerty"

[conversation_settings.agent]
max_iterations = 12

[model_settings.providers.local]
type = "openai"
api_key = "secret"

[model_settings.model]
provider = "local"
model = "gpt-test"

[voice_settings.mode]
input_mode = "stt"

[voice_settings.classic.stt.providers.local]
type = "openai-whisper"
model = "whisper-1"

[voice_settings.classic.stt]
provider = "local"

[voice_settings.classic.tts.providers.local]
type = "minimax-cn"

[voice_settings.classic.tts]
provider = "local"
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Device.DeviceType != "Android" || cfg.HID.KeyboardLayout != "azerty" {
		t.Fatalf("device mapping = %#v / %#v", cfg.Device, cfg.HID)
	}
	if cfg.MaxIterations != 12 {
		t.Fatalf("max_iterations = %d, want 12", cfg.MaxIterations)
	}
	if cfg.Model.Provider != "local" || cfg.ModelProviders["local"].Type != "openai" {
		t.Fatalf("model mapping = %#v / %#v", cfg.Model, cfg.ModelProviders)
	}
	if cfg.InputMode != "stt" || cfg.STT.Provider != "local" || cfg.STTProviders["local"].Type != "openai-whisper" {
		t.Fatalf("voice mapping = mode %q stt %#v providers %#v", cfg.InputMode, cfg.STT, cfg.STTProviders)
	}
}

func TestLoadConfigReadsGroupedLegacyFieldAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	const source = `
[model_settings.model]
provider = "fake"
max_tokens = 777

[voice_settings.classic.audio]
playback_backend = "local"
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Model.MaxResponseTokens != 777 {
		t.Fatalf("MaxResponseTokens = %d, want 777", cfg.Model.MaxResponseTokens)
	}
	if cfg.Audio.Backend != AudioBackendLocal {
		t.Fatalf("Audio.Backend = %q, want local", cfg.Audio.Backend)
	}
}
