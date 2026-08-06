package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aiden-agent/internal/agent"
)

// The voice provider records must survive config -> DTO -> config. This is the
// same sync point that broke [providers] end to end: a missing DTO field made
// the config page show zero records AND made every save of an unrelated section
// erase them from agent.toml.
func TestConfigWire_VoiceProvidersRoundTrip(t *testing.T) {
	cfg := agent.Config{
		TTSProviders: map[string]agent.TTSProvider{
			"minimax-main": {Provider: "minimax", APIKey: "sk-aaa", VoiceID: "male-qn-qingse", Emotion: "happy"},
			"fish":         {Provider: "fish-audio", APIKey: "sk-ccc", ReferenceID: "ref-abc"},
			"env-based":    {Provider: "minimax", TokenEnv: "MINIMAX_KEY"},
		},
		STTProviders: map[string]agent.STTProvider{
			"tencent": {Provider: "tencent-asr", AppID: "123", SecretID: "AKID", SecretKey: "sec", Region: "ap-shanghai"},
			"whisper": {Provider: "openai-whisper", APIKey: "sk-w", BaseURL: "https://api.openai.com/v1", Model: "whisper-1"},
		},
		TTS:   agent.TTSConfig{Provider: "minimax-main", Speed: 1.2},
		STT:   agent.STTConfig{Provider: "tencent", Language: "zh"},
		Model: agent.ModelConfig{Provider: "openai", Model: "gpt-4o"},
	}

	dto := webConfigDTOFromAgentConfig(cfg)
	if len(dto.TTSProviders) != 3 {
		t.Fatalf("dto.TTSProviders = %#v, want 3 entries", dto.TTSProviders)
	}
	if len(dto.STTProviders) != 2 {
		t.Fatalf("dto.STTProviders = %#v, want 2 entries", dto.STTProviders)
	}
	if got := dto.TTSProviders["fish"]; got.Provider != "fish-audio" || got.ReferenceID != "ref-abc" {
		t.Errorf("dto.TTSProviders[fish] = %#v", got)
	}
	if got := dto.TTSProviders["env-based"].TokenEnv; got != "MINIMAX_KEY" {
		t.Errorf("token_env dropped: %q", got)
	}
	if got := dto.STTProviders["tencent"]; got.AppID != "123" || got.SecretKey != "sec" {
		t.Errorf("dto.STTProviders[tencent] = %#v", got)
	}

	back := dto.toAgentConfig()
	if !reflect.DeepEqual(back.TTSProviders, cfg.TTSProviders) {
		t.Errorf("round-tripped tts_providers = %#v, want %#v", back.TTSProviders, cfg.TTSProviders)
	}
	if !reflect.DeepEqual(back.STTProviders, cfg.STTProviders) {
		t.Errorf("round-tripped stt_providers = %#v, want %#v", back.STTProviders, cfg.STTProviders)
	}
	// The reference itself must not be resolved on the wire: the config page
	// edits the reference, so it has to come back as the name it wrote.
	if back.TTS.Provider != "minimax-main" {
		t.Errorf("tts.provider = %q, want the unresolved ref %q", back.TTS.Provider, "minimax-main")
	}
	if back.STT.Provider != "tencent" {
		t.Errorf("stt.provider = %q, want the unresolved ref %q", back.STT.Provider, "tencent")
	}
}

// omitempty on both maps, matching providers: a config with no records omits the
// key rather than emitting {}, and the C++ read path treats a missing key as
// "no records" rather than an error.
func TestConfigWire_VoiceProvidersOmittedWhenEmpty(t *testing.T) {
	cfg := agent.Config{Model: agent.ModelConfig{Provider: "openai", Model: "gpt-4o", APIKey: "sk-x"}}
	payload, err := json.Marshal(webConfigDTOFromAgentConfig(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), `"tts_providers"`) {
		t.Errorf("expected tts_providers omitted when empty, got: %s", payload)
	}
	if strings.Contains(string(payload), `"stt_providers"`) {
		t.Errorf("expected stt_providers omitted when empty, got: %s", payload)
	}

	cfg.TTSProviders = map[string]agent.TTSProvider{"fish": {Provider: "fish-audio"}}
	cfg.STTProviders = map[string]agent.STTProvider{"w": {Provider: "openai-whisper"}}
	payload, err = json.Marshal(webConfigDTOFromAgentConfig(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"tts_providers"`) {
		t.Errorf("expected tts_providers in payload, got: %s", payload)
	}
	if !strings.Contains(string(payload), `"stt_providers"`) {
		t.Errorf("expected stt_providers in payload, got: %s", payload)
	}
}

// A save that stores a dangling reference silently loses voice on the next
// restart, so config-check must reject it while the user is still on the form.
// Boot stays lenient on purpose -- see the agent package tests.
func TestConfigWire_DanglingVoiceRefRejectedOnSave(t *testing.T) {
	payload := `{
		"tts_providers":{"fish":{"provider":"fish-audio"}},
		"tts":{"provider":"typo-name"},
		"model":{"provider":"openai","model":"gpt-4o","api_key":"sk-x"},
		"search":{"provider":"duckduckgo"},
		"agent":{},
		"hid":{"pointer_mode":"absolute"}
	}`
	result := checkWire(t, payload)
	if result.Valid {
		t.Fatal("expected a dangling tts.provider reference to be rejected on save")
	}
	if len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Message, "typo-name") {
		t.Errorf("error should name the dangling ref, got: %+v", result.Errors)
	}
}

func TestConfigWire_UnsupportedVoiceProviderTypeRejectedOnSave(t *testing.T) {
	// openai is an LLM type; accepting it for TTS would save a config that can
	// never build a TTS adapter.
	payload := `{
		"tts_providers":{"bad":{"provider":"openai"}},
		"tts":{"provider":"bad"},
		"model":{"provider":"openai","model":"gpt-4o","api_key":"sk-x"},
		"search":{"provider":"duckduckgo"},
		"agent":{},
		"hid":{"pointer_mode":"absolute"}
	}`
	result := checkWire(t, payload)
	if result.Valid {
		t.Fatal("expected an LLM provider type to be rejected for tts_providers")
	}

	payload = `{
		"stt_providers":{"bad":{"provider":"minimax"}},
		"stt":{"provider":"bad"},
		"model":{"provider":"openai","model":"gpt-4o","api_key":"sk-x"},
		"search":{"provider":"duckduckgo"},
		"agent":{},
		"hid":{"pointer_mode":"absolute"}
	}`
	result = checkWire(t, payload)
	if result.Valid {
		t.Fatal("expected a TTS provider type to be rejected for stt_providers")
	}
}

// An unreferenced record is parked user data. It must not block a save of an
// unrelated section even when this build has no adapter for its provider type.
func TestConfigWire_UnreferencedVoiceRecordSaves(t *testing.T) {
	payload := `{
		"tts_providers":{"fish":{"provider":"fish-audio"},"cartesia":{"provider":"cartesia"}},
		"tts":{"provider":"fish"},
		"model":{"provider":"openai","model":"gpt-4o","api_key":"sk-x"},
		"search":{"provider":"duckduckgo"},
		"agent":{},
		"hid":{"pointer_mode":"absolute"}
	}`
	result := checkWire(t, payload)
	if !result.Valid {
		t.Errorf("unreferenced cartesia record should not block a save, got: %+v", result.Errors)
	}
}

// A bare provider type in [tts] keeps saving: devices in the field have flat
// configs and the config page must not reject them.
func TestConfigWire_BareVoiceProviderTypeSaves(t *testing.T) {
	payload := `{
		"tts":{"provider":"minimax-cn"},
		"stt":{"provider":"openai-whisper"},
		"model":{"provider":"openai","model":"gpt-4o","api_key":"sk-x"},
		"search":{"provider":"duckduckgo"},
		"agent":{},
		"hid":{"pointer_mode":"absolute"}
	}`
	result := checkWire(t, payload)
	if !result.Valid {
		t.Errorf("a bare provider type should still save, got: %+v", result.Errors)
	}
}
