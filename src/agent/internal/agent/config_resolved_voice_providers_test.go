package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadResolvedConfig backs `agent config --format=json`, which is what the config
// page reads through. It ran neither migration nor resolution, so for a flat
// config it emitted fields and no records: the page showed a provider select
// with no card, leaving the key invisible and un-editable.
//
// Migration has to run here, not only in LoadRuntimeConfig, because both GET and
// POST read the stored config through this CLI path.
func TestLoadResolvedConfigMigratesFlatVoiceCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	body := `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-model"

[tts]
provider = "minimax-cn"
api_key = "sk-minimax"
voice_id = "male-qn-qingse"
speed = 1.3

[stt]
provider = "tencent-asr"
app_id = "1234"
secret_key = "secret-yyy"
language = "zh"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadResolvedConfig(path)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}

	// The flat credential set becomes a record too.
	active, ok := cfg.TTSProviders["minimax-cn"]
	if !ok {
		t.Fatalf("expected a minimax-cn record, got %v", recordNames(cfg.TTSProviders))
	}
	if active.APIKey != "sk-minimax" {
		t.Errorf("minimax-cn record api_key = %q, want %q", active.APIKey, "sk-minimax")
	}
	if active.VoiceID != "male-qn-qingse" {
		t.Errorf("minimax-cn record voice_id = %q, want %q", active.VoiceID, "male-qn-qingse")
	}

	stt, ok := cfg.STTProviders["tencent-asr"]
	if !ok {
		t.Fatalf("expected a tencent-asr record, got %v", sttRecordNames(cfg.STTProviders))
	}
	if stt.AppID != "1234" || stt.SecretKey != "secret-yyy" {
		t.Errorf("tencent record = %+v, want the flat credentials", stt)
	}

	// The reference stays unresolved: the config page edits the reference, so it
	// must come back as a name, not as the resolved provider type.
	if cfg.TTS.Provider != "minimax-cn" {
		t.Errorf("tts.provider = %q, want the ref %q", cfg.TTS.Provider, "minimax-cn")
	}

	// Exactly one editor per credential. Leaving the flat copy in place would
	// give the page two fields for one key that disagree the moment either is
	// edited, and would write both shapes back to agent.toml.
	if cfg.TTS.APIKey != "" {
		t.Errorf("tts.api_key = %q, want cleared after migrating onto the record", cfg.TTS.APIKey)
	}
	if cfg.TTS.VoiceID != "" {
		t.Errorf("tts.voice_id = %q, want cleared", cfg.TTS.VoiceID)
	}
	if cfg.STT.AppID != "" || cfg.STT.SecretKey != "" {
		t.Errorf("flat stt credentials not cleared: %+v", cfg.STT)
	}

	// The global settings stay flat.
	if cfg.TTS.Speed != 1.3 {
		t.Errorf("tts.speed = %v, want 1.3 to stay global", cfg.TTS.Speed)
	}
	if cfg.STT.Language != "zh" {
		t.Errorf("stt.language = %q, want %q to stay global", cfg.STT.Language, "zh")
	}
}

// A config that never configured voice must not gain a record. DefaultConfig
// carries a TTS provider, so migrating without a metadata gate would mint a
// minimax-cn card for every device that only ever set up a model.
func TestLoadResolvedConfigInventsNoVoiceRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	body := `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-model"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadResolvedConfig(path)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}

	if len(cfg.TTSProviders) != 0 {
		t.Errorf("len(tts_providers) = %d, want 0: %v", len(cfg.TTSProviders), recordNames(cfg.TTSProviders))
	}
	if len(cfg.STTProviders) != 0 {
		t.Errorf("len(stt_providers) = %d, want 0: %v", len(cfg.STTProviders), sttRecordNames(cfg.STTProviders))
	}
}

// A config already using records passes through untouched.
func TestLoadResolvedConfigLeavesRecordsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	body := `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-model"

[tts_providers.fish-main]
provider = "fish-audio"
api_key = "sk-fish"

[tts]
provider = "fish-main"
speed = 1.0
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadResolvedConfig(path)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}

	if len(cfg.TTSProviders) != 1 {
		t.Errorf("len(tts_providers) = %d, want 1: %v", len(cfg.TTSProviders), recordNames(cfg.TTSProviders))
	}
	if cfg.TTSProviders["fish-main"].APIKey != "sk-fish" {
		t.Errorf("record api_key = %q, want %q", cfg.TTSProviders["fish-main"].APIKey, "sk-fish")
	}
	if cfg.TTS.Provider != "fish-main" {
		t.Errorf("tts.provider = %q, want the ref %q", cfg.TTS.Provider, "fish-main")
	}
}
