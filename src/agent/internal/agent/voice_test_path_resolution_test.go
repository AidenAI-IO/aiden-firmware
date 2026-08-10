package agent

import "testing"

// The config page's Test button posts the form values. Once the credentials live
// on a record, that form carries only the reference plus the global settings --
// so the test path must resolve the reference itself.
//
// Without this, NewSTTClientFromConfig dispatches on the raw value, does not
// match any provider type, and the user sees "unsupported STT provider:
// tencent-main" for a record that is configured correctly.
func TestApplySTTTestRequestResolvesRecordRef(t *testing.T) {
	cfg := Config{
		STTProviders: map[string]STTProvider{
			"tencent-main": {
				Type: "tencent-asr", AppID: "1234", SecretID: "AKID-xxx",
				SecretKey: "secret-yyy", Region: "ap-shanghai",
			},
		},
	}

	// What the slimmed form posts: the reference and language only.
	applySTTTranscriptionTestRequest(&cfg, STTTranscriptionTestRequest{
		Provider: "tencent-main",
		Language: "zh",
	})

	if cfg.STT.Provider != "tencent-asr" {
		t.Errorf("stt.provider = %q, want the resolved type %q", cfg.STT.Provider, "tencent-asr")
	}
	if cfg.STT.AppID != "1234" {
		t.Errorf("stt.app_id = %q, want %q inherited from the record", cfg.STT.AppID, "1234")
	}
	if cfg.STT.SecretID != "AKID-xxx" || cfg.STT.SecretKey != "secret-yyy" {
		t.Errorf("Tencent credentials not inherited: %+v", cfg.STT)
	}
	if cfg.STT.Region != "ap-shanghai" {
		t.Errorf("stt.region = %q, want %q", cfg.STT.Region, "ap-shanghai")
	}
	if cfg.STT.Language != "zh" {
		t.Errorf("stt.language = %q, want the posted %q", cfg.STT.Language, "zh")
	}
}

// A value the user typed into the form still overrides the record, so editing a
// field and hitting Test exercises what was typed.
func TestApplySTTTestRequestFormValueOverridesRecord(t *testing.T) {
	cfg := Config{
		STTProviders: map[string]STTProvider{
			"whisper": {Type: "openai-whisper", APIKey: "sk-record", BaseURL: "https://record.example"},
		},
	}

	applySTTTranscriptionTestRequest(&cfg, STTTranscriptionTestRequest{
		Provider: "whisper",
		APIKey:   "sk-typed",
	})

	if cfg.STT.APIKey != "sk-typed" {
		t.Errorf("stt.api_key = %q, want the typed %q", cfg.STT.APIKey, "sk-typed")
	}
	if cfg.STT.BaseURL != "https://record.example" {
		t.Errorf("stt.base_url = %q, want %q inherited", cfg.STT.BaseURL, "https://record.example")
	}
}

func TestApplySTTTestRequestSwitchesRecordCredential(t *testing.T) {
	cfg := Config{
		STTProviders: map[string]STTProvider{
			"openai-main": {Type: "openai-whisper", APIKey: "sk-openai"},
			"qwen-main":   {Type: "qwen-asr", APIKey: "sk-qwen"},
		},
		// Runtime config has already expanded openai-main into this effective
		// provider configuration before the config-test request arrives.
		STT: STTConfig{Provider: "openai-whisper", APIKey: "sk-openai"},
	}

	applySTTTranscriptionTestRequest(&cfg, STTTranscriptionTestRequest{
		Provider: "qwen-main",
	})

	if cfg.STT.Provider != "qwen-asr" {
		t.Errorf("stt.provider = %q, want %q", cfg.STT.Provider, "qwen-asr")
	}
	if cfg.STT.APIKey != "sk-qwen" {
		t.Errorf("stt.api_key = %q, want the selected record credential %q", cfg.STT.APIKey, "sk-qwen")
	}
}

func TestApplyTTSTestRequestResolvesRecordRef(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"fish-main": {Type: "fish-audio", APIKey: "sk-fish", ReferenceID: "ref-abc"},
		},
	}

	applyTTSPlaybackTestRequest(&cfg, TTSPlaybackTestRequest{
		Provider: "fish-main",
		Speed:    1.2,
	})

	if cfg.TTS.Provider != "fish-audio" {
		t.Errorf("tts.provider = %q, want the resolved type %q", cfg.TTS.Provider, "fish-audio")
	}
	if cfg.TTS.APIKey != "sk-fish" {
		t.Errorf("tts.api_key = %q, want %q inherited from the record", cfg.TTS.APIKey, "sk-fish")
	}
	if cfg.TTS.ReferenceID != "ref-abc" {
		t.Errorf("tts.reference_id = %q, want %q", cfg.TTS.ReferenceID, "ref-abc")
	}
	if cfg.TTS.Speed != 1.2 {
		t.Errorf("tts.speed = %v, want the posted 1.2", cfg.TTS.Speed)
	}
}

// A bare provider type keeps working through the test path, so a device with a
// pre-records config can still use the Test button.
func TestApplyVoiceTestRequestBareTypeStillWorks(t *testing.T) {
	cfg := Config{}
	applyTTSPlaybackTestRequest(&cfg, TTSPlaybackTestRequest{
		Provider: "minimax-cn",
		APIKey:   "sk-flat",
		VoiceID:  "male-qn-qingse",
	})
	if cfg.TTS.Provider != "minimax-cn" {
		t.Errorf("tts.provider = %q, want %q", cfg.TTS.Provider, "minimax-cn")
	}
	if cfg.TTS.APIKey != "sk-flat" {
		t.Errorf("tts.api_key = %q, want %q", cfg.TTS.APIKey, "sk-flat")
	}

	cfg = Config{}
	applySTTTranscriptionTestRequest(&cfg, STTTranscriptionTestRequest{
		Provider: "openai-whisper",
		APIKey:   "sk-whisper",
	})
	if cfg.STT.Provider != "openai-whisper" {
		t.Errorf("stt.provider = %q, want %q", cfg.STT.Provider, "openai-whisper")
	}
}

// The switch API's provider list drives what a client can switch to. Registered
// adapter types alone are not enough once several records of one type exist:
// switching by type could not name which account to use.
func TestAvailableTTSProvidersIncludesRecordNames(t *testing.T) {
	cfg := Config{
		TTSProviders: map[string]TTSProvider{
			"z-minimax": {Type: "minimax"},
			"a-minimax": {Type: "minimax"},
			"fish-main": {Type: "fish-audio"},
		},
	}

	got := availableTTSProviderNames(cfg)

	index := map[string]int{}
	for i, name := range got {
		index[name] = i
	}
	for _, want := range []string{"a-minimax", "fish-main", "z-minimax"} {
		if _, ok := index[want]; !ok {
			t.Errorf("record name %q missing from available providers: %v", want, got)
		}
	}
	// The registered adapter types stay listed, so a client that switches by
	// type keeps working.
	for _, want := range []string{"minimax", "fish-audio"} {
		if _, ok := index[want]; !ok {
			t.Errorf("registered type %q missing from available providers: %v", want, got)
		}
	}
	// No duplicates, and stable order so the UI list does not shuffle.
	seen := map[string]bool{}
	for _, name := range got {
		if seen[name] {
			t.Errorf("duplicate entry %q in %v", name, got)
		}
		seen[name] = true
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("available providers not sorted: %v", got)
			break
		}
	}
}
