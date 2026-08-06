package agent

import "testing"

// Runtime switching must read the target provider's own credentials. Before
// records existed this came from [tts.credentials.<type>]; migration clears that
// map, so a switch that still consulted it would silently fall back to the
// ACTIVE provider's key and try to authenticate against the wrong service.
func TestBuildTTSProviderConfigForUsesRecords(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"minimax-main": {Provider: "minimax", APIKey: "sk-minimax", VoiceID: "male-qn-qingse"},
			"fish-main":    {Provider: "fish-audio", APIKey: "sk-fish", ReferenceID: "ref-abc"},
		},
		TTS: TTSConfig{Provider: "minimax", APIKey: "sk-minimax", VoiceID: "male-qn-qingse"},
	}

	// Switching to fish-audio must pick up the fish record, not the active
	// minimax key.
	got := buildTTSProviderConfigFor(cfg, "fish-audio")
	if got.Provider != "fish-audio" {
		t.Errorf("provider = %q, want %q", got.Provider, "fish-audio")
	}
	if got.APIKey != "sk-fish" {
		t.Errorf("api_key = %q, want %q (fell back to the active provider's key)", got.APIKey, "sk-fish")
	}
	if ref, _ := got.Extra["reference_id"].(string); ref != "ref-abc" {
		t.Errorf("reference_id = %q, want %q", ref, "ref-abc")
	}
	// Fish Audio does not use voice, and must not inherit minimax's.
	if got.Voice != "" {
		t.Errorf("voice = %q, want empty for fish-audio", got.Voice)
	}
}

// A record can also be addressed by name, which is what the config page passes.
func TestBuildTTSProviderConfigForAcceptsRecordName(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"fish-main": {Provider: "fish-audio", APIKey: "sk-fish", ReferenceID: "ref-abc"},
		},
		TTS: TTSConfig{Provider: "fish-main"},
	}

	got := buildTTSProviderConfigFor(cfg, "fish-main")
	if got.Provider != "fish-audio" {
		t.Errorf("provider = %q, want the record's type %q", got.Provider, "fish-audio")
	}
	if got.APIKey != "sk-fish" {
		t.Errorf("api_key = %q, want %q", got.APIKey, "sk-fish")
	}
}

// token_env on a record resolves for runtime switching too, not just at load.
func TestBuildTTSProviderConfigForResolvesTokenEnv(t *testing.T) {
	t.Setenv("AIDEN_TEST_SWITCH_KEY", "sk-env-switch")
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"fish-main": {Provider: "fish-audio", TokenEnv: "AIDEN_TEST_SWITCH_KEY"},
		},
	}

	got := buildTTSProviderConfigFor(cfg, "fish-audio")
	if got.APIKey != "sk-env-switch" {
		t.Errorf("api_key = %q, want %q from token_env", got.APIKey, "sk-env-switch")
	}
}

// The legacy per-type credentials map keeps working for a config built in
// process (tests, and any caller that never went through LoadRuntimeConfig).
func TestBuildTTSProviderConfigForStillHonorsLegacyCredentials(t *testing.T) {
	cfg := Config{
		TTS: TTSConfig{
			Provider: "minimax",
			APIKey:   "sk-active",
			Credentials: map[string]TTSProviderCredentials{
				"fish-audio": {APIKey: "sk-legacy-fish", ReferenceID: "ref-legacy"},
			},
		},
	}

	got := buildTTSProviderConfigFor(cfg, "fish-audio")
	if got.APIKey != "sk-legacy-fish" {
		t.Errorf("api_key = %q, want %q", got.APIKey, "sk-legacy-fish")
	}
}

// A record beats the legacy map when both describe the same provider, so a
// half-migrated config converges on the record.
func TestBuildTTSProviderConfigForPrefersRecordOverLegacy(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"fish-main": {Provider: "fish-audio", APIKey: "sk-record-fish"},
		},
		TTS: TTSConfig{
			Provider: "minimax",
			Credentials: map[string]TTSProviderCredentials{
				"fish-audio": {APIKey: "sk-legacy-fish"},
			},
		},
	}

	got := buildTTSProviderConfigFor(cfg, "fish-audio")
	if got.APIKey != "sk-record-fish" {
		t.Errorf("api_key = %q, want the record's %q", got.APIKey, "sk-record-fish")
	}
}
