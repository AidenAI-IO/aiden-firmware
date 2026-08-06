package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadVoiceProviderConfigErr loads a config and returns the error, for the
// validation cases where loading must fail.
func loadVoiceProviderConfigErr(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadRuntimeConfig(path)
	return err
}

// Strict voice-provider checks belong to the config page's save path, NOT to
// boot. A TTS init failure is a logger.Warn today and the agent still starts
// without voice; Config.Validate() never checks the provider type either. So a
// device whose provider name went stale must keep booting -- refusing to start
// would be a regression on the most core scenario. ValidateVoiceProviders is
// the strict pass, called only when saving.
func TestVoiceProviderMisconfigurationDoesNotBlockBoot(t *testing.T) {
	// A record whose type is an LLM type, referenced by [tts].
	cfg := writeVoiceProviderConfig(t, `
[tts_providers.bad]
provider = "openai"

[tts]
provider = "bad"
`)
	// Boot survived. The provider is left unresolved so tts.New() reports it.
	if cfg.TTS.Provider == "" {
		t.Error("tts.provider was blanked, which reads as 'TTS disabled' rather than 'misconfigured'")
	}

	// A reference left behind after its record was deleted.
	cfg = writeVoiceProviderConfig(t, `
[tts_providers.fish]
provider = "fish-audio"

[tts]
provider = "typo-name"
`)
	if cfg.TTS.Provider != "typo-name" {
		t.Errorf("tts.provider = %q, want the raw ref preserved for the warning", cfg.TTS.Provider)
	}
}

// The TTS type whitelist must stay separate from isKnownProviderType. Merging
// them so one list serves all three would let [model] provider = "minimax" pass
// validation and only fail later when the model client is built.
func TestTTSProviderTypeWhitelistIsSeparateFromLLM(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{"bad": {Provider: "openai"}},
		TTS:          TTSConfig{Provider: "bad"},
	}
	err := cfg.ValidateVoiceProviders()
	if err == nil {
		t.Fatal("expected an error: openai is an LLM type, not a TTS type")
	}
	if !strings.Contains(err.Error(), "tts_providers.bad") {
		t.Errorf("error %q should name the offending record", err.Error())
	}

	// And the converse: a TTS type must not satisfy [model]. This one IS fatal
	// at load, because [model] is not optional the way voice is.
	loadErr := loadVoiceProviderConfigErr(t, `
[providers.bad]
provider = "minimax"

[model]
provider = "bad"
model = "gpt-4o"
`)
	if loadErr == nil {
		t.Fatal("expected an error: minimax is a TTS type, not an LLM type")
	}
}

func TestSTTProviderTypeWhitelist(t *testing.T) {
	cfg := Config{
		STTProviders: map[string]STTProvider{"bad": {Provider: "minimax"}},
		STT:          STTConfig{Provider: "bad"},
	}
	err := cfg.ValidateVoiceProviders()
	if err == nil {
		t.Fatal("expected an error: minimax is a TTS type, not an STT type")
	}
	if !strings.Contains(err.Error(), "stt_providers.bad") {
		t.Errorf("error %q should name the offending record", err.Error())
	}
}

func TestVoiceProviderRecordRequiresType(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{"empty": {APIKey: "sk-xxx"}},
		TTS:          TTSConfig{Provider: "empty"},
	}
	err := cfg.ValidateVoiceProviders()
	if err == nil {
		t.Fatal("expected an error: a record with no provider type is unusable")
	}
	if !strings.Contains(err.Error(), "provider type is required") {
		t.Errorf("error %q should explain the missing type", err.Error())
	}
}

// A reference left behind after its record was deleted is rejected on save, so
// the config page cannot persist a config that silently loses voice.
func TestVoiceProviderDanglingRefRejectedOnSave(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{"fish": {Provider: "fish-audio"}},
		TTS:          TTSConfig{Provider: "typo-name"},
	}
	if err := cfg.ValidateVoiceProviders(); err == nil {
		t.Fatal("expected an error: typo-name is neither a record nor a TTS type")
	}
}

// An unreferenced record is user data parked for later. It must not block a
// save, and above all a legacy [tts.credentials.<type>] entry for an
// unregistered provider must survive migration without breaking anything.
func TestUnreferencedVoiceRecordToleratedOnSave(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"fish":     {Provider: "fish-audio"},
			"cartesia": {Provider: "cartesia"},
		},
		TTS: TTSConfig{Provider: "fish"},
	}
	if err := cfg.ValidateVoiceProviders(); err != nil {
		t.Errorf("unreferenced cartesia record should be tolerated, got: %v", err)
	}
}

// [tts.credentials.<type>] is the old Go-only shape. It must upgrade to a named
// record so the config page can see it -- until now C++ had no handling for the
// key at all, so a single web save erased the whole block.
func TestLegacyTTSCredentialsMigrateToRecords(t *testing.T) {
	cfg := writeVoiceProviderConfig(t, `
[tts]
provider = "minimax-cn"
api_key = "sk-minimax"

[tts.credentials.fish-audio]
api_key = "sk-fish"
reference_id = "ref-abc"

[tts.credentials.cartesia]
api_key = "sk-cartesia"
voice_id = "voice-xyz"
`)

	// The active provider becomes a record too, so the flat section is left
	// holding nothing but the reference.
	rec, ok := cfg.TTSProviders["minimax-cn"]
	if !ok {
		t.Fatalf("expected a minimax-cn record, got %v", recordNames(cfg.TTSProviders))
	}
	if rec.Provider != "minimax-cn" {
		t.Errorf("record type = %q, want %q", rec.Provider, "minimax-cn")
	}
	if rec.APIKey != "sk-minimax" {
		t.Errorf("record api_key = %q, want %q", rec.APIKey, "sk-minimax")
	}

	fish, ok := cfg.TTSProviders["fish-audio"]
	if !ok {
		t.Fatalf("expected a fish-audio record, got %v", recordNames(cfg.TTSProviders))
	}
	if fish.APIKey != "sk-fish" {
		t.Errorf("fish api_key = %q, want %q", fish.APIKey, "sk-fish")
	}
	if fish.ReferenceID != "ref-abc" {
		t.Errorf("fish reference_id = %q, want %q", fish.ReferenceID, "ref-abc")
	}

	// An unregistered type in legacy credentials must not be silently dropped:
	// it is user data, and dropping it is the bug this migration exists to fix.
	if _, ok := cfg.TTSProviders["cartesia"]; !ok {
		t.Errorf("expected the cartesia record to survive, got %v", recordNames(cfg.TTSProviders))
	}

	// The reference still resolves to the active provider's own credentials.
	if cfg.TTS.Provider != "minimax-cn" {
		t.Errorf("tts.provider = %q, want %q", cfg.TTS.Provider, "minimax-cn")
	}
	if cfg.TTS.APIKey != "sk-minimax" {
		t.Errorf("tts.api_key = %q, want %q", cfg.TTS.APIKey, "sk-minimax")
	}
}

// speed is a listening preference, not a credential, so it stays global. A
// legacy per-type speed is only honored when it belongs to the active provider
// and [tts].speed is unset; otherwise keeping it would silently change playback
// speed on switching voices.
func TestLegacyCredentialsSpeedPromotedOnlyForActiveProvider(t *testing.T) {
	cfg := writeVoiceProviderConfig(t, `
[tts]
provider = "fish-audio"

[tts.credentials.fish-audio]
api_key = "sk-fish"
speed = 1.4
`)
	if cfg.TTS.Speed != 1.4 {
		t.Errorf("tts.speed = %v, want 1.4 promoted from the active provider", cfg.TTS.Speed)
	}

	// An explicit [tts].speed wins, and an inactive provider's speed is dropped.
	cfg = writeVoiceProviderConfig(t, `
[tts]
provider = "fish-audio"
speed = 1.0

[tts.credentials.fish-audio]
speed = 1.4

[tts.credentials.minimax]
speed = 0.8
`)
	if cfg.TTS.Speed != 1.0 {
		t.Errorf("tts.speed = %v, want the explicit 1.0 to win", cfg.TTS.Speed)
	}
}

// The legacy flat [stt] credential set upgrades the same way, so both voice
// sections reach the config page in one shape.
func TestLegacySTTFlatFieldsMigrateToRecord(t *testing.T) {
	cfg := writeVoiceProviderConfig(t, `
[stt]
provider = "tencent-asr"
app_id = "1234"
secret_id = "AKID-xxx"
secret_key = "secret-yyy"
region = "ap-shanghai"
language = "zh"
`)

	rec, ok := cfg.STTProviders["tencent-asr"]
	if !ok {
		t.Fatalf("expected a tencent-asr record, got %v", sttRecordNames(cfg.STTProviders))
	}
	if rec.AppID != "1234" || rec.SecretID != "AKID-xxx" || rec.SecretKey != "secret-yyy" {
		t.Errorf("record did not carry the Tencent credentials: %+v", rec)
	}
	if rec.Region != "ap-shanghai" {
		t.Errorf("record region = %q, want %q", rec.Region, "ap-shanghai")
	}
	// language is global and must not migrate into the record.
	if cfg.STT.Language != "zh" {
		t.Errorf("stt.language = %q, want %q to stay on the flat section", cfg.STT.Language, "zh")
	}
}

// Migration must not clobber a record the user already wrote by hand.
func TestMigrationDoesNotOverwriteExistingRecord(t *testing.T) {
	cfg := writeVoiceProviderConfig(t, `
[tts_providers.fish-audio]
provider = "fish-audio"
api_key = "sk-explicit-record"

[tts]
provider = "fish-audio"

[tts.credentials.fish-audio]
api_key = "sk-legacy"
`)

	rec := cfg.TTSProviders["fish-audio"]
	if rec.APIKey != "sk-explicit-record" {
		t.Errorf("record api_key = %q, want the explicit record to win over legacy credentials", rec.APIKey)
	}
}

// With no TTS configured at all, nothing is invented: TTS stays opt-in.
func TestNoVoiceProviderStaysEmpty(t *testing.T) {
	cfg := writeVoiceProviderConfig(t, `
[model]
provider = "openai"
model = "gpt-4o"
api_key = "sk-test"
`)
	if cfg.TTS.Provider != "" {
		t.Errorf("tts.provider = %q, want empty", cfg.TTS.Provider)
	}
	if len(cfg.TTSProviders) != 0 {
		t.Errorf("len(tts_providers) = %d, want 0", len(cfg.TTSProviders))
	}
	if len(cfg.STTProviders) != 0 {
		t.Errorf("len(stt_providers) = %d, want 0", len(cfg.STTProviders))
	}
}

func recordNames(records map[string]TTSProvider) []string {
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	return names
}

func sttRecordNames(records map[string]STTProvider) []string {
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	return names
}
