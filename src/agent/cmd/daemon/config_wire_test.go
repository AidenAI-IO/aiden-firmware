package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"aiden-agent/internal/agent"
)

// These tests exercise the real config_web <-> agent wire contract: the JSON
// shape produced by config_web.cpp's config_to_json() (snake_case keys, agent
// settings nested under "agent", search reporting only has_api_key). The
// PascalCase fixtures in config_commands_test.go validate Config.Validate()
// directly and never touch this decode path, so a contract drift between the
// C++ serializer and the Go decoder would pass there while silently accepting
// every invalid config in production. checkConfig() is the guard against that.

func checkWire(t *testing.T, payload string) ValidationResult {
	t.Helper()
	result, err := checkConfig(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("checkConfig returned decode error for valid JSON: %v", err)
	}
	return result
}

// TestConfigCheck_WireFormatContract is the regression test for the bug where
// agent.Config (TOML-tagged only) was decoded straight from the snake_case /
// nested wire format, dropping every field and validating an empty config.
// A minimal valid config in real wire format must pass, and invalid values in
// their real wire positions must be rejected.
func TestConfigCheck_WireFormatContract(t *testing.T) {
	t.Run("minimal valid config passes", func(t *testing.T) {
		payload := `{
			"model":{"provider":"openai","model":"gpt-4"},
			"search":{"provider":"duckduckgo"},
			"agent":{},
			"hid":{"pointer_mode":"absolute"}
		}`
		result := checkWire(t, payload)
		if !result.Valid {
			t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
		}
	})

	// The core regression: these invalid values live in their real wire
	// positions (nested under "agent" / "hid", snake_case). Before the DTO fix
	// every one of these was silently dropped and the config validated as valid.
	invalidCases := []struct {
		name        string
		payload     string
		wantInField string // substring expected in the offending field/message
	}{
		{
			name: "invalid pointer_mode nested under hid",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"hid":{"pointer_mode":"joystick"},"agent":{}}`,
			wantInField: "pointer_mode",
		},
		{
			name: "invalid keyboard_layout nested under hid",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"hid":{"keyboard_layout":"dvorak"},"agent":{}}`,
			wantInField: "keyboard_layout",
		},
		{
			name: "vad_speech_threshold out of range nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"vad_speech_threshold":1.5}}`,
			wantInField: "vad_speech_threshold",
		},
		{
			name: "max_iterations below -1 nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"max_iterations":-5}}`,
			wantInField: "max_iterations",
		},
		{
			name: "invalid input_mode nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"input_mode":"bogus"}}`,
			wantInField: "input_mode",
		},
		{
			name: "invalid locale nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"locale":"fr-FR"}}`,
			wantInField: "locale",
		},
		{
			name: "missing model provider",
			payload: `{"model":{"model":"gpt-4"},
				"search":{"provider":"duckduckgo"},"agent":{}}`,
			wantInField: "model.provider",
		},
		{
			name: "invalid search provider",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"google"},"agent":{}}`,
			wantInField: "search.provider",
		},
		{
			name: "invalid audio playback backend",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"audio":{"playback_backend":"speaker"},"agent":{}}`,
			wantInField: "audio.playback_backend",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkWire(t, tc.payload)
			if result.Valid {
				t.Fatalf("expected valid=false for %s, but config was accepted", tc.name)
			}
			joined := result.Errors[0].Field + " " + result.Errors[0].Message
			if !strings.Contains(joined, tc.wantInField) {
				t.Errorf("expected error to reference %q, got field=%q message=%q",
					tc.wantInField, result.Errors[0].Field, result.Errors[0].Message)
			}
		})
	}
}

// TestConfigCheck_WireSearchHasAPIKey verifies the has_api_key contract: the
// web UI never echoes the stored secret, so a provider that requires a key
// must pass when has_api_key=true and fail when it is false/absent.
func TestConfigCheck_WireSearchHasAPIKey(t *testing.T) {
	for _, provider := range []string{"brave", "tavily"} {
		t.Run(provider+" with has_api_key=true passes", func(t *testing.T) {
			payload := `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"` + provider + `","has_api_key":true},"agent":{}}`
			result := checkWire(t, payload)
			if !result.Valid {
				t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
			}
		})

		t.Run(provider+" with has_api_key=false fails", func(t *testing.T) {
			payload := `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"` + provider + `","has_api_key":false},"agent":{}}`
			result := checkWire(t, payload)
			if result.Valid {
				t.Fatalf("expected valid=false when %s key absent, but config was accepted", provider)
			}
		})
	}
}

func TestConfigCheck_WireCustomInstructionMapsToAgentConfig(t *testing.T) {
	dto := webConfigDTO{
		Model:  modelDTO{Provider: "openai", Model: "gpt-4"},
		Search: searchDTO{Provider: "duckduckgo"},
		Agent:  agentDTO{CustomInstruction: "Use custom behavior."},
	}
	cfg := dto.toAgentConfig()
	if cfg.Instruction != "Use custom behavior." {
		t.Fatalf("Instruction = %q, want custom instruction", cfg.Instruction)
	}
}

func TestConfigCheck_WireLocaleMapsToAgentConfig(t *testing.T) {
	dto := webConfigDTO{
		Model:  modelDTO{Provider: "openai", Model: "gpt-4"},
		Search: searchDTO{Provider: "duckduckgo"},
		Agent:  agentDTO{Locale: "en-US"},
	}
	cfg := dto.toAgentConfig()
	if cfg.Locale != "en-US" {
		t.Fatalf("Locale = %q, want en-US", cfg.Locale)
	}
	if got := webConfigDTOFromAgentConfig(cfg).Agent.Locale; got != "en-US" {
		t.Fatalf("round-trip locale = %q, want en-US", got)
	}
}

// TestConfigCheck_WireTelemetryNested verifies telemetry validation runs
// against the nested "telemetry" wire object.
func TestConfigCheck_WireTelemetryNested(t *testing.T) {
	payload := `{"model":{"provider":"openai","model":"gpt-4"},
		"search":{"provider":"duckduckgo"},
		"telemetry":{"enabled":true},"agent":{}}`
	result := checkWire(t, payload)
	if result.Valid {
		t.Fatal("expected valid=false when telemetry.enabled=true but base_url missing")
	}
	if !strings.Contains(result.Errors[0].Field, "telemetry") {
		t.Errorf("expected telemetry field, got %q", result.Errors[0].Field)
	}
}

// TestConfigCheck_WireLiveActivityNested verifies live_activity validation runs
// against the nested wire object used by config_web.
func TestConfigCheck_WireLiveActivityNested(t *testing.T) {
	t.Run("valid relay config passes", func(t *testing.T) {
		payload := `{"model":{"provider":"openai","model":"gpt-4"},
				"benchmark":{"api_key":"sk-judge"},
				"search":{"provider":"duckduckgo"},
				"live_activity":{"enabled":true,"relay_url":"https://relay.example.com",
					"has_relay_api_key":true,"board_id":"board-001",
					"environment":"production","timeout_sec":10},"agent":{}}`
		result := checkWire(t, payload)
		if !result.Valid {
			t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
		}
	})

	t.Run("invalid relay_url fails", func(t *testing.T) {
		payload := `{"model":{"provider":"openai","model":"gpt-4"},
			"benchmark":{"api_key":"sk-judge"},
			"search":{"provider":"duckduckgo"},
			"live_activity":{"enabled":true,"relay_url":"://bad"},"agent":{}}`
		result := checkWire(t, payload)
		if result.Valid {
			t.Fatal("expected valid=false for invalid live_activity.relay_url")
		}
		joined := result.Errors[0].Field + " " + result.Errors[0].Message
		if !strings.Contains(joined, "live_activity.relay_url") {
			t.Errorf("expected live_activity.relay_url error, got field=%q message=%q",
				result.Errors[0].Field, result.Errors[0].Message)
		}
	})
}

// TestConfigCheck_InvalidJSON ensures malformed input is reported as a decode
// error rather than silently validating a zero-value config.
func TestConfigCheck_InvalidJSON(t *testing.T) {
	_, err := checkConfig(strings.NewReader("not json"))
	if err == nil {
		t.Fatal("expected decode error for malformed JSON, got nil")
	}
}

func TestConfigCheckPath_ValidatesFullTOMLWithoutRejectingUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	content := `locale = "en-US"
todo_reminder_tool_calls = 7
skills_dirs = ["/userdata/skills"]
future_plugin_flag = true

[device]
backend = "hdmi"
future_device_option = "keep me"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := checkConfigPath(path)
	if !result.Valid {
		t.Fatalf("expected valid config, got errors: %+v", result.Errors)
	}
}

func TestConfigCheckPath_RejectsInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("locale = [\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := checkConfigPath(path)
	if result.Valid {
		t.Fatal("expected malformed TOML to be rejected")
	}
	if len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Message, "TOML") {
		t.Fatalf("expected TOML decode error, got: %+v", result.Errors)
	}
}

// TestConfigWire_ProvidersRoundTrip guards the sync point that broke the
// provider feature end to end: webConfigDTO had no top-level "providers" field,
// so `agent config --format=json` never emitted the section. config_web.cpp
// builds its AgentToml from that output, which made GET /api/config report zero
// providers and made every save of an unrelated section erase them from
// agent.toml. Validation was skipped for the same reason.
func TestConfigWire_ProvidersRoundTrip(t *testing.T) {
	t.Run("providers survive config -> DTO -> config", func(t *testing.T) {
		cfg := agent.Config{
			Providers: map[string]agent.Provider{
				"my-openai": {Provider: "openai", APIKey: "sk-secret", TokenEnv: "OPENAI_KEY"},
				"my-ollama": {Provider: "ollama", BaseURL: "http://127.0.0.1:11434"},
			},
			Model: agent.ModelConfig{Provider: "my-openai", Model: "gpt-4"},
		}

		dto := webConfigDTOFromAgentConfig(cfg)
		if len(dto.Providers) != 2 {
			t.Fatalf("dto.Providers = %#v, want 2 entries", dto.Providers)
		}
		if got := dto.Providers["my-openai"]; got.Provider != "openai" ||
			got.APIKey != "sk-secret" || got.TokenEnv != "OPENAI_KEY" {
			t.Errorf("dto.Providers[my-openai] = %#v", got)
		}
		if got := dto.Providers["my-ollama"].BaseURL; got != "http://127.0.0.1:11434" {
			t.Errorf("dto.Providers[my-ollama].BaseURL = %q", got)
		}

		back := dto.toAgentConfig()
		if !reflect.DeepEqual(back.Providers, cfg.Providers) {
			t.Errorf("round-tripped providers = %#v, want %#v", back.Providers, cfg.Providers)
		}
	})

	t.Run("wire payload marshals a providers key", func(t *testing.T) {
		dto := webConfigDTOFromAgentConfig(agent.Config{
			Providers: map[string]agent.Provider{"my-openai": {Provider: "openai"}},
			Model:     agent.ModelConfig{Provider: "my-openai", Model: "gpt-4"},
		})
		encoded, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal dto: %v", err)
		}
		if !strings.Contains(string(encoded), `"providers":{"my-openai":{"provider":"openai"}}`) {
			t.Errorf("encoded payload missing providers section: %s", encoded)
		}
	})

	t.Run("unsupported provider type is rejected through the wire decoder", func(t *testing.T) {
		payload := `{
			"providers":{"zz":{"provider":"nonsense"}},
			"model":{"provider":"zz","model":"gpt-4"},
			"search":{"provider":"duckduckgo"},
			"agent":{},
			"hid":{"pointer_mode":"absolute"}
		}`
		result := checkWire(t, payload)
		if result.Valid {
			t.Fatal("expected an unsupported provider type to be rejected")
		}
		if len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Message, "providers.zz") {
			t.Fatalf("expected providers.zz error, got: %+v", result.Errors)
		}
	})

	t.Run("provider entry without a type is rejected", func(t *testing.T) {
		payload := `{
			"providers":{"empty-one":{"api_key":"k"}},
			"model":{"provider":"empty-one","model":"gpt-4"},
			"search":{"provider":"duckduckgo"},
			"agent":{},
			"hid":{"pointer_mode":"absolute"}
		}`
		result := checkWire(t, payload)
		if result.Valid {
			t.Fatal("expected a provider without a type to be rejected")
		}
	})

	t.Run("valid named providers pass", func(t *testing.T) {
		payload := `{
			"providers":{
				"my-openai":{"provider":"openai","api_key":"sk-x"},
				"my-ollama":{"provider":"ollama","base_url":"http://127.0.0.1:11434"}
			},
			"model":{"provider":"my-openai","model":"gpt-4"},
			"search":{"provider":"duckduckgo"},
			"agent":{},
			"hid":{"pointer_mode":"absolute"}
		}`
		result := checkWire(t, payload)
		if !result.Valid {
			t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
		}
	})
}

// TestWebConfigDTOTopLevelSectionsAreCovered pins the set of top-level sections
// that `agent config --format=json` emits.
//
// config_web.cpp builds its AgentToml from this payload, so a section the Go DTO
// does not emit does not exist as far as the C++ read path is concerned. That is
// exactly how the `providers` bug worked: the field was missing here, so the
// config page always showed zero providers AND every save of an unrelated
// section started from an empty map and erased them from agent.toml.
//
// The C++ fixtures (tests/agent_stub_main.cpp and the resolved_config_json()
// helper in tests/config_web_e2e_test.cpp) are hand-maintained copies of this
// payload, so they cannot catch drift on the Go side. This test can: when a
// section is added to webConfigDTO, this list must be updated, which is the
// prompt to update the C++ fixtures and the AgentToml struct in the same change.
func TestWebConfigDTOTopLevelSectionsAreCovered(t *testing.T) {
	want := []string{
		"agent",
		"audio",
		"audio_archive",
		"hid",
		"live_activity",
		"log",
		"model",
		"model_text",
		"ota",
		"providers",
		"search",
		"stt",
		"stt_providers",
		"telemetry",
		"termination_policy",
		"tts",
		"tts_providers",
		"voice_notifications",
	}

	dtoType := reflect.TypeOf(webConfigDTO{})
	got := make([]string, 0, dtoType.NumField())
	for i := 0; i < dtoType.NumField(); i++ {
		tag := dtoType.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Errorf("field %s has no json tag; the C++ side keys off these names",
				dtoType.Field(i).Name)
			continue
		}
		got = append(got, strings.Split(tag, ",")[0])
	}

	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("webConfigDTO top-level sections drifted.\n got: %v\nwant: %v\n"+
			"If this is intentional, update this list AND the C++ fixtures in "+
			"tests/agent_stub_main.cpp and tests/config_web_e2e_test.cpp, plus "+
			"AgentToml in src/agent_toml.h.", got, want)
	}
}

// TestWebConfigDTOProvidersOmittedWhenEmpty documents that `providers` carries
// omitempty, so a config with no providers omits the key entirely rather than
// emitting {}. The C++ read path must treat a missing key as "no providers"
// rather than as an error.
func TestWebConfigDTOProvidersOmittedWhenEmpty(t *testing.T) {
	cfg := agent.Config{
		Model: agent.ModelConfig{Provider: "openai", Model: "gpt-4o", APIKey: "sk-x"},
	}
	payload, err := json.Marshal(webConfigDTOFromAgentConfig(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), `"providers"`) {
		t.Errorf("expected providers to be omitted when empty, got: %s", payload)
	}

	// And with a provider present the key must appear.
	cfg.Providers = map[string]agent.Provider{
		"work": {Provider: "openai", APIKey: "sk-work"},
	}
	payload, err = json.Marshal(webConfigDTOFromAgentConfig(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"providers"`) {
		t.Errorf("expected providers in the payload, got: %s", payload)
	}
}
