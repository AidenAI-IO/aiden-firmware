package agent

import (
	"reflect"
	"strings"
	"testing"
)

// fieldIndex builds a section.key -> FieldMeta lookup for the metadata.
func fieldIndex(t *testing.T) map[string]FieldMeta {
	t.Helper()
	idx := map[string]FieldMeta{}
	for _, section := range ConfigMeta().Sections {
		for _, f := range section.Fields {
			key := section.Name + "." + f.Key
			if _, dup := idx[key]; dup {
				t.Fatalf("duplicate field in metadata: %s", key)
			}
			idx[key] = f
		}
	}
	return idx
}

// tomlKeys returns the set of toml field names for a struct type, skipping
// fields marked `toml:"-"` and omitting struct/map/slice members the UI does
// not render as flat fields.
func tomlKeys(t reflect.Type) map[string]reflect.StructField {
	keys := map[string]reflect.StructField{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		keys[name] = f
	}
	return keys
}

func TestConfigMeta_Valid(t *testing.T) {
	if got := ConfigMeta().Sections; len(got) == 0 {
		t.Fatal("ConfigMeta returned no sections")
	}

	validWidgets := map[Widget]bool{
		WidgetText: true, WidgetTextarea: true, WidgetNumber: true,
		WidgetBoolean: true, WidgetSelect: true, WidgetList: true,
	}
	validOps := map[string]bool{"eq": true, "ne": true, "in": true, "notIn": true, "truthy": true}

	idx := fieldIndex(t)
	seenSections := map[string]bool{}

	for _, section := range ConfigMeta().Sections {
		if section.Name == "" {
			t.Error("section with empty name")
		}
		seenSections[section.Name] = true
		if len(section.Fields) == 0 {
			t.Errorf("section %q has no fields", section.Name)
		}

		for _, f := range section.Fields {
			path := section.Name + "." + f.Key
			if !validWidgets[f.Widget] {
				t.Errorf("%s: invalid widget %q", path, f.Widget)
			}
			// select widgets must carry either enum or range.
			if f.Widget == WidgetSelect && len(f.Enum) == 0 && f.Range == nil {
				t.Errorf("%s: select widget without enum or range", path)
			}
			// enum only makes sense on select widgets.
			if len(f.Enum) > 0 && f.Widget != WidgetSelect {
				t.Errorf("%s: enum present on non-select widget %q", path, f.Widget)
			}

			rules := []VisibleRule{}
			if f.VisibleWhen != nil {
				rules = append(rules, *f.VisibleWhen)
			}
			for _, rule := range rules {
				conds := append(append([]Condition{}, rule.All...), rule.Any...)
				for _, c := range conds {
					if !validOps[c.Op] {
						t.Errorf("%s: invalid condition op %q", path, c.Op)
					}
					if _, ok := idx[c.Field]; !ok {
						t.Errorf("%s: visibleWhen references unknown field %q", path, c.Field)
					}
				}
			}
		}
	}

	for _, name := range []string{"model", "tts", "stt", "audio", "benchmark", "hid", "search", "telemetry", "agent"} {
		if !seenSections[name] {
			t.Errorf("expected section %q to be present", name)
		}
	}
}

// TestConfigMeta_EnumsMatchValidation guards against drift between the metadata
// enums and the constants the validator accepts.
func TestConfigMeta_EnumsMatchValidation(t *testing.T) {
	idx := fieldIndex(t)

	enumValues := func(path string) []string {
		f, ok := idx[path]
		if !ok {
			t.Fatalf("missing field %q in metadata", path)
		}
		vals := make([]string, 0, len(f.Enum))
		for _, o := range f.Enum {
			vals = append(vals, o.Value)
		}
		return vals
	}
	contains := func(vals []string, want string) bool {
		for _, v := range vals {
			if v == want {
				return true
			}
		}
		return false
	}

	// search.provider must include every provider the validator accepts.
	searchEnum := enumValues("search.provider")
	for _, p := range []string{searchProviderDuckDuckGo, searchProviderBrave, searchProviderTavily} {
		if !contains(searchEnum, p) {
			t.Errorf("search.provider enum missing validated provider %q", p)
		}
	}

	// hid.pointer_mode enum must match Validate()'s accepted set.
	pmEnum := enumValues("hid.pointer_mode")
	for _, m := range []string{"absolute", "touchscreen"} {
		if !contains(pmEnum, m) {
			t.Errorf("hid.pointer_mode enum missing %q", m)
		}
	}
	for _, m := range pmEnum {
		c := Config{HID: HIDConfig{PointerMode: m}, Model: ModelConfig{Provider: "openai", Model: "x"}}
		if err := c.Validate(); err != nil {
			t.Errorf("hid.pointer_mode enum value %q rejected by Validate: %v", m, err)
		}
	}

	// vad_backend enum must match normalizeVADBackend's accepted set.
	for _, b := range enumValues("agent.vad_backend") {
		if _, err := normalizeVADBackend(b); err != nil {
			t.Errorf("vad_backend enum value %q rejected by normalizeVADBackend: %v", b, err)
		}
	}

	// telemetry.provider non-empty enum values must pass telemetry validation.
	for _, p := range enumValues("telemetry.provider") {
		if p == "" {
			continue
		}
		tc := TelemetryConfig{Provider: p}
		if got := tc.ProviderOrDefault(); got != "langfuse" {
			t.Errorf("telemetry.provider enum value %q normalizes to %q, expected langfuse", p, got)
		}
	}
}

func TestConfigMeta_RuntimeDefaultsMatch(t *testing.T) {
	idx := fieldIndex(t)
	defaults := DefaultConfig()

	tests := []struct {
		path string
		want any
	}{
		{"model.provider", defaults.Model.Provider},
		{"model.model", defaults.Model.Model},
		{"model.temperature", defaults.Model.Temperature},
		{"model.max_response_tokens", defaults.Model.MaxResponseTokens},
		{"tts.provider", defaults.TTS.Provider},
		{"tts.voice_id", defaults.TTS.VoiceID},
		{"tts.emotion", defaults.TTS.Emotion},
		{"tts.speed", defaults.TTS.Speed},
		{"stt.provider", defaults.STT.Provider},
		{"stt.model", defaults.STT.Model},
		{"audio.socket", defaults.Audio.Socket},
		{"audio.sample_rate", defaults.Audio.SampleRate},
		{"audio.channels", defaults.Audio.Channels},
		{"audio.bit_width", defaults.Audio.BitWidth},
		{"benchmark.judge_model", defaults.Benchmark.JudgeModel},
		{"hid.keyboard_device", defaults.HID.KeyboardDevice},
		{"hid.mouse_device", defaults.HID.MouseDevice},
		{"hid.frame_socket", defaults.HID.FrameSocket},
		{"hid.pointer_mode", defaults.HID.PointerMode},
		{"search.provider", defaults.Search.ProviderOrDefault()},
		{"agent.input_mode", defaults.InputMode},
		{"agent.trigger_mode", defaults.TriggerMode},
		{"agent.vad_backend", defaults.VADBackend},
		{"agent.vad_model_path", defaults.VADModelPath},
		{"agent.vad_helper_path", defaults.VADHelperPath},
		{"agent.vad_speech_threshold", defaults.VADSpeechThreshold},
		{"agent.silence_ms", defaults.SilenceMs},
		{"agent.min_speech_ms", defaults.MinSpeechMs},
		{"agent.voice_session_enabled", defaults.VoiceSessionEnabledOrDefault()},
		{"agent.voice_followup_timeout_ms", defaults.VoiceFollowupTimeoutMs},
		{"agent.voice_first_turn_timeout_ms", defaults.VoiceFirstTurnTimeoutMs},
		{"agent.voice_max_turns", defaults.VoiceMaxTurns},
		{"agent.voice_interrupt_on_wakeup", defaults.VoiceInterruptOnWakeupOrDefault()},
		{"agent.voice_streaming_tts_enabled", defaults.VoiceStreamingTTSEnabledOrDefault()},
		{"agent.voice_tool_call_speech", defaults.VoiceToolCallSpeechOrDefault()},
		{"agent.voice_max_response_tokens", defaults.VoiceMaxResponseTokens},
		{"agent.max_iterations", defaults.MaxIterations},
		{"agent.screenshot_keep_n", defaults.ScreenshotKeepN},
		{"agent.screenshot_prune_interval", defaults.ScreenshotPruneInterval},
		{"agent.screen_stable_timeout_ms", defaults.ScreenStableTimeoutMs},
		{"agent.screen_stable_ms", defaults.ScreenStableMs},
		{"agent.screen_stable_diff_threshold", defaults.ScreenStableDiffThreshold},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			field, ok := idx[tt.path]
			if !ok {
				t.Fatalf("missing metadata field %s", tt.path)
			}
			if !reflect.DeepEqual(field.Default, tt.want) {
				t.Fatalf("%s default = %#v, want runtime default %#v", tt.path, field.Default, tt.want)
			}
		})
	}
}

// TestConfigMeta_CoversConfigFields uses reflection to ensure every flat,
// UI-relevant config field has corresponding metadata, preventing silent drift
// when new fields are added to the Config structs.
func TestConfigMeta_CoversConfigFields(t *testing.T) {
	idx := fieldIndex(t)

	// Nested struct sections map directly to their Go types.
	type sectionType struct {
		name string
		typ  reflect.Type
		// skip lists toml keys that are intentionally not exposed in the UI.
		skip map[string]bool
	}
	sections := []sectionType{
		{"model", reflect.TypeOf(ModelConfig{}), map[string]bool{"responses": true}},
		{"tts", reflect.TypeOf(TTSConfig{}), map[string]bool{"reference_id": true, "credentials": true}},
		{"stt", reflect.TypeOf(STTConfig{}), nil},
		{"audio", reflect.TypeOf(AudioConfig{}), nil},
		{"benchmark", reflect.TypeOf(BenchmarkConfig{}), nil},
		{"hid", reflect.TypeOf(HIDConfig{}), nil},
		{"search", reflect.TypeOf(SearchConfig{}), nil},
		{"telemetry", reflect.TypeOf(TelemetryConfig{}), nil},
	}

	for _, s := range sections {
		for name := range tomlKeys(s.typ) {
			if s.skip[name] {
				continue
			}
			path := s.name + "." + name
			if _, ok := idx[path]; !ok {
				t.Errorf("config field %s has no metadata entry", path)
			}
		}
	}

	// The "agent" UI section maps to top-level Config fields. Only flat
	// (scalar/string/bool) fields are rendered; nested structs and infra-only
	// fields are surfaced under their own sections or not at all.
	agentSkip := map[string]bool{
		"model": true, "model_text": true, "tts": true, "stt": true, "hid": true,
		"audio": true, "search": true, "telemetry": true,
		"skills_dirs": true, "bundled_skills_dir": true,
	}
	cfgType := reflect.TypeOf(Config{})
	for name, f := range tomlKeys(cfgType) {
		if agentSkip[name] {
			continue
		}
		// Only require metadata for flat fields the UI renders.
		k := f.Type.Kind()
		if k == reflect.Struct || k == reflect.Slice || k == reflect.Map {
			continue
		}
		if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Bool {
			// *bool booleans are rendered; fall through to the check.
		}
		path := "agent." + name
		if _, ok := idx[path]; !ok {
			t.Errorf("top-level config field %s has no metadata entry under agent section", path)
		}
	}
}
