package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// Two records of the same type are the whole point of named records, and they
// expose a seam between the two resolution steps: resolveTTSProvider (at load)
// replaces cfg.TTS.Provider with the resolved TYPE, which loses which record was
// chosen. buildTTSProviderConfig then re-resolves by type at speak time and,
// with two records of that type, can pick the other one -- speaking with the
// wrong account's key while the config page shows the right record selected.
func TestActiveRecordSurvivesSpeakTimeResolution(t *testing.T) {
	// The record names are chosen so the ACTIVE one sorts LAST. The type scan
	// walks names in sorted order, so a fixture whose active record happens to
	// sort first passes even when the seam is broken. "a-minimax" sorting ahead
	// of "z-minimax" is what makes this test able to fail.
	path := filepath.Join(t.TempDir(), "agent.toml")
	body := `
[tts_providers.a-minimax]
provider = "minimax"
api_key = "sk-aaa"
voice_id = "male-qn-qingse"

[tts_providers.z-minimax]
provider = "minimax"
api_key = "sk-bbb"
voice_id = "female-shaonv"

[tts]
provider = "z-minimax"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig: %v", err)
	}

	// Load-time resolution picked the referenced record.
	if cfg.TTS.APIKey != "sk-bbb" {
		t.Fatalf("load resolved api_key = %q, want %q", cfg.TTS.APIKey, "sk-bbb")
	}

	// Speak time must not silently swap to minimax-main just because it sorts
	// first among records of type "minimax".
	got := buildTTSProviderConfig(cfg)
	if got.APIKey != "sk-bbb" {
		t.Errorf("speak-time api_key = %q, want %q (resolution swapped records)", got.APIKey, "sk-bbb")
	}
	if got.Voice != "female-shaonv" {
		t.Errorf("speak-time voice = %q, want %q", got.Voice, "female-shaonv")
	}
}

// Switching to a specific record by name must reach that record even when
// another record shares its type.
func TestSwitchByNameReachesTheNamedRecord(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"minimax-main": {Provider: "minimax", APIKey: "sk-aaa", VoiceID: "male-qn-qingse"},
			"minimax-alt":  {Provider: "minimax", APIKey: "sk-bbb", VoiceID: "female-shaonv"},
		},
		// As if minimax-alt were resolved at load.
		TTS: TTSConfig{Provider: "minimax", APIKey: "sk-bbb", VoiceID: "female-shaonv"},
	}

	got := buildTTSProviderConfigFor(cfg, "minimax-main")
	if got.APIKey != "sk-aaa" {
		t.Errorf("api_key = %q, want %q for an explicit record name", got.APIKey, "sk-aaa")
	}
	if got.Voice != "male-qn-qingse" {
		t.Errorf("voice = %q, want %q", got.Voice, "male-qn-qingse")
	}
}

// Switching by bare type (what the phone sends) to a type that is NOT active
// still resolves through the records.
func TestSwitchByTypeToInactiveProviderUsesItsRecord(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"minimax-alt": {Provider: "minimax", APIKey: "sk-bbb"},
			"fish":        {Provider: "fish-audio", APIKey: "sk-fish", ReferenceID: "ref-abc"},
		},
		TTS: TTSConfig{Provider: "minimax", APIKey: "sk-bbb"},
	}

	got := buildTTSProviderConfigFor(cfg, "fish-audio")
	if got.APIKey != "sk-fish" {
		t.Errorf("api_key = %q, want %q", got.APIKey, "sk-fish")
	}
	if ref, _ := got.Extra["reference_id"].(string); ref != "ref-abc" {
		t.Errorf("reference_id = %q, want %q", ref, "ref-abc")
	}
}
