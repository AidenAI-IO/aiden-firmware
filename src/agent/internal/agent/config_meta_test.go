package agent

import (
	"encoding/json"
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
			// enum only makes sense on select widgets or text widgets that
			// conditionally become selects.
			if len(f.Enum) > 0 && f.Widget != WidgetSelect && f.SelectWhen == nil {
				t.Errorf("%s: enum present on non-select widget %q", path, f.Widget)
			}
			if f.SelectWhen != nil && (f.Widget != WidgetText || len(f.Enum) == 0) {
				t.Errorf("%s: selectWhen requires a text widget with enum options", path)
			}

			rules := []VisibleRule{}
			if f.VisibleWhen != nil {
				rules = append(rules, *f.VisibleWhen)
			}
			if f.SelectWhen != nil {
				rules = append(rules, *f.SelectWhen)
			}
			for _, conditionalPlaceholder := range f.PlaceholderWhen {
				rules = append(rules, conditionalPlaceholder.When)
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

	if ConfigMeta().Sections[0].Name != "device" {
		t.Errorf("expected device section first, got %q", ConfigMeta().Sections[0].Name)
	}

	for _, name := range []string{"device", "model", "tts", "stt", "audio", "audio_archive", "log", "hid", "search", "telemetry", "live_activity", "agent"} {
		if !seenSections[name] {
			t.Errorf("expected section %q to be present", name)
		}
	}

}

func TestFieldMeta_DisplayFieldsJSONSchema(t *testing.T) {
	payload, err := json.Marshal(FieldMeta{
		Key:         "github_proxy_url",
		Label:       "GitHub Proxy URL",
		Help:        "Optional proxy for GitHub downloads.",
		Placeholder: "Leave empty to disable",
		Layout:      "wide",
		Widget:      WidgetText,
	})
	if err != nil {
		t.Fatalf("marshal FieldMeta: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal FieldMeta JSON: %v", err)
	}
	for key, want := range map[string]string{
		"key":         "github_proxy_url",
		"label":       "GitHub Proxy URL",
		"help":        "Optional proxy for GitHub downloads.",
		"placeholder": "Leave empty to disable",
		"layout":      "wide",
		"widget":      string(WidgetText),
	} {
		if got[key] != want {
			t.Errorf("FieldMeta JSON %s = %#v, want %q", key, got[key], want)
		}
	}

	minimal, err := json.Marshal(FieldMeta{Key: "enabled", Widget: WidgetBoolean})
	if err != nil {
		t.Fatalf("marshal minimal FieldMeta: %v", err)
	}
	for _, omitted := range []string{"label", "help", "placeholder", "layout"} {
		if strings.Contains(string(minimal), `"`+omitted+`"`) {
			t.Errorf("empty %s must be omitted from FieldMeta JSON: %s", omitted, minimal)
		}
	}
}

func TestConfigMeta_AllFieldsHaveStableLabels(t *testing.T) {
	for _, section := range ConfigMeta().Sections {
		for _, field := range section.Fields {
			if strings.TrimSpace(field.Label) == "" {
				t.Errorf("%s.%s has no display label", section.Name, field.Key)
			}
		}
	}
}

func TestConfigMeta_PreservesExistingFormPresentation(t *testing.T) {
	idx := fieldIndex(t)

	type displayMeta struct {
		label       string
		help        string
		placeholder string
		layout      string
	}
	want := map[string]displayMeta{
		"device.device_type": {
			help: "Android exposes both a touchscreen and an absolute mouse with a visible cursor. iOS, macOS, windows, and linux use the absolute mouse.",
		},
		"agent.vad_model_path":          {layout: "wide"},
		"agent.vad_helper_path":         {layout: "wide"},
		"agent.custom_instruction":      {layout: "wide"},
		"agent.additional_prompt":       {layout: "wide"},
		"model.provider":                {layout: "wide"},
		"model.model":                   {layout: "wide"},
		"model.reasoning_effort":        {help: "Empty = auto. For no-tool requests, Anthropic maps low/medium/high to adaptive thinking; tool requests use Claude's default reasoning because thinking signatures are not persisted. Minimal is OpenRouter and Volcengine Ark only; none is not supported by Anthropic or Ark."},
		"model.context_window":          {placeholder: "0 = auto", help: "0 = auto: use provider metadata when available."},
		"model.model_max_output_tokens": {placeholder: "0 = auto", help: "0 = auto: use provider metadata when available."},
		"tts.provider":                  {layout: "wide"},
		"stt.provider":                  {layout: "wide"},
		"audio.socket":                  {layout: "wide"},
		"audio_archive.enabled":         {help: "After enabling, save STT voice recording WAV for Web UI playback; Automatically delete old files when exceeding quantity or capacity limit."},
		"audio_archive.storage_path":    {layout: "wide"},
		"ota.github_proxy_url": {
			label:       "GitHub Proxy URL",
			help:        "Optional proxy to accelerate GitHub downloads (e.g., https://gh-proxy.com/ or https://ghfast.top/)",
			placeholder: "Leave empty to disable",
			layout:      "wide",
		},
		"hid.keyboard_layout": {
			help: "How the phone interprets the USB keyboard. Keep qwerty unless typed text comes out transposed; then switch the phone input language to match, save, and reboot the board.",
		},
		"hid.keyboard_device":         {layout: "wide"},
		"hid.mouse_device":            {layout: "wide"},
		"hid.android_keyboard_device": {layout: "wide"},
		"hid.touchscreen_device":      {layout: "wide"},
		"hid.frame_socket":            {layout: "wide"},
		"search.api_key":              {layout: "wide"},
		"telemetry.base_url": {
			placeholder: "http://langfuse.example.com:3000",
			layout:      "wide",
		},
		"telemetry.public_key": {layout: "wide"},
		"telemetry.secret_key": {layout: "wide"},
		"telemetry.tags":       {layout: "wide"},
	}

	for path, expected := range want {
		field, ok := idx[path]
		if !ok {
			t.Errorf("missing metadata field %s", path)
			continue
		}
		if expected.label != "" && field.Label != expected.label {
			t.Errorf("%s label = %q, want %q", path, field.Label, expected.label)
		}
		if field.Help != expected.help {
			t.Errorf("%s help = %q, want %q", path, field.Help, expected.help)
		}
		if field.Placeholder != expected.placeholder {
			t.Errorf("%s placeholder = %q, want %q", path, field.Placeholder, expected.placeholder)
		}
		if field.Layout != expected.layout {
			t.Errorf("%s layout = %q, want %q", path, field.Layout, expected.layout)
		}
	}
}

func TestConfigMeta_SpecialRendererFieldsRemainAddressable(t *testing.T) {
	idx := fieldIndex(t)
	// These fields remain in metadata for read/write/default/visibility logic,
	// but config-form.js renders them through its section.key renderer registry.
	for _, path := range []string{
		"model.provider",
		"model.model",
		"tts.provider",
		"stt.provider",
	} {
		if _, ok := idx[path]; !ok {
			t.Errorf("special renderer field %s is missing from metadata", path)
		}
	}
}

// TestConfigMeta_NonRegistryEnumsMatchValidation covers enums that do not have
// a runtime provider registry as their canonical source.
func TestConfigMeta_NonRegistryEnumsMatchValidation(t *testing.T) {
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

	// device.device_type enum must match Validate()'s accepted set.
	deviceTypeEnum := enumValues("device.device_type")
	for _, deviceType := range []string{"iOS", "Android", "macOS", "windows", "linux"} {
		if !contains(deviceTypeEnum, deviceType) {
			t.Errorf("device.device_type enum missing %q", deviceType)
		}
	}
	for _, deviceType := range deviceTypeEnum {
		c := Config{Device: DeviceConfig{DeviceType: deviceType}, Model: ModelConfig{Provider: "openai", Model: "x"}}
		if err := c.Validate(); err != nil {
			t.Errorf("device.device_type enum value %q rejected by Validate: %v", deviceType, err)
		}
	}

	// hid.input_backend enum must match Validate()'s accepted set.
	inputBackendEnum := enumValues("hid.input_backend")
	for _, b := range []string{"hid", "adb"} {
		if !contains(inputBackendEnum, b) {
			t.Errorf("hid.input_backend enum missing %q", b)
		}
	}
	for _, b := range inputBackendEnum {
		c := Config{HID: HIDConfig{InputBackend: b}, Model: ModelConfig{Provider: "openai", Model: "x"}}
		if err := c.Validate(); err != nil {
			t.Errorf("hid.input_backend enum value %q rejected by Validate: %v", b, err)
		}
	}

	keyboardLayoutEnum := enumValues("hid.keyboard_layout")
	for _, layout := range []string{keyboardLayoutQWERTY, keyboardLayoutAZERTY, keyboardLayoutQWERTZ} {
		if !contains(keyboardLayoutEnum, layout) {
			t.Errorf("hid.keyboard_layout enum missing %q", layout)
		}
	}
	for _, layout := range keyboardLayoutEnum {
		c := Config{HID: HIDConfig{KeyboardLayout: layout}, Model: ModelConfig{Provider: "openai", Model: "x"}}
		if err := c.Validate(); err != nil {
			t.Errorf("hid.keyboard_layout enum value %q rejected by Validate: %v", layout, err)
		}
	}

	audioPlaybackEnum := enumValues("audio.playback_backend")
	for _, b := range []string{AudioPlaybackBackendAuto, AudioPlaybackBackendAudioService, AudioPlaybackBackendLocal} {
		if !contains(audioPlaybackEnum, b) {
			t.Errorf("audio.playback_backend enum missing %q", b)
		}
	}
	for _, b := range audioPlaybackEnum {
		c := Config{Audio: AudioConfig{PlaybackBackend: b}, Model: ModelConfig{Provider: "openai", Model: "x"}}
		if err := c.Validate(); err != nil {
			t.Errorf("audio.playback_backend enum value %q rejected by Validate: %v", b, err)
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
		// temperature's effective default is model-dependent and resolved at
		// load time, so the metadata placeholder is the global fallback rather
		// than the (now unset) DefaultConfig value.
		{"model.temperature", defaultModelTemperature},
		{"model.max_response_tokens", defaults.Model.MaxResponseTokens},
		{"model.log_raw_http", defaults.Model.LogRawHTTP},
		{"tts.provider", defaults.TTS.Provider},
		{"tts.speed", defaults.TTS.Speed},
		// voice_id and emotion moved onto the record: they stop meaning
		// anything when the provider type changes, so they are edited in the
		// provider dialog rather than on the flat section.
		{"tts_providers.voice_id", defaults.TTS.VoiceID},
		{"tts_providers.emotion", defaults.TTS.Emotion},
		{"stt.provider", defaults.STT.Provider},
		{"stt_providers.model", defaults.STT.Model},
		{"audio.socket", defaults.Audio.Socket},
		{"audio.sample_rate", defaults.Audio.SampleRate},
		{"audio.channels", defaults.Audio.Channels},
		{"audio.bit_width", defaults.Audio.BitWidth},
		{"audio.playback_backend", defaults.Audio.PlaybackBackend},
		{"audio_archive.enabled", defaults.AudioArchive.Enabled},
		{"audio_archive.max_files", defaults.AudioArchive.MaxFilesOrDefault()},
		{"audio_archive.max_size_mb", defaults.AudioArchive.MaxSizeMBOrDefault()},
		{"audio_archive.storage_path", defaults.AudioArchive.StoragePathOrDefault()},
		{"device.device_type", defaults.Device.DeviceTypeOrDefault()},
		{"log.llm_http_retention_days", defaults.Log.LLMHTTPRetentionDaysOrDefault()},
		{"hid.keyboard_device", defaults.HID.KeyboardDevice},
		{"hid.keyboard_layout", defaults.HID.KeyboardLayout},
		{"hid.mouse_device", defaults.HID.MouseDevice},
		{"hid.android_keyboard_device", defaults.HID.AndroidKeyboardDevice},
		{"hid.touchscreen_device", defaults.HID.TouchscreenDevice},
		{"hid.frame_socket", defaults.HID.FrameSocket},
		{"hid.input_backend", defaults.HID.InputBackend},
		{"search.provider", defaults.Search.ProviderOrDefault()},
		{"agent.input_mode", defaults.InputMode},
		{"agent.locale", defaults.LocaleOrDefault()},
		{"agent.trigger_mode", defaults.TriggerMode},
		{"agent.vad_backend", defaults.VADBackend},
		{"agent.vad_model_path", defaults.VADModelPath},
		{"agent.vad_helper_path", defaults.VADHelperPath},
		{"agent.vad_speech_threshold", defaults.VADSpeechThreshold},
		{"agent.silence_ms", defaults.SilenceMs},
		{"agent.min_speech_ms", defaults.MinSpeechMs},
		{"agent.voice_followup_enabled", defaults.VoiceFollowupEnabledOrDefault()},
		{"agent.voice_followup_timeout_ms", defaults.VoiceFollowupTimeoutMs},
		{"agent.voice_first_turn_timeout_ms", defaults.VoiceFirstTurnTimeoutMs},
		{"agent.voice_max_turns", defaults.VoiceMaxTurns},
		{"agent.voice_interrupt_on_wakeup", defaults.VoiceInterruptOnWakeupOrDefault()},
		{"agent.voice_streaming_tts_enabled", defaults.VoiceStreamingTTSEnabledOrDefault()},
		{"agent.voice_tool_call_speech", defaults.VoiceToolCallSpeechOrDefault()},
		{"agent.voice_progress_speech_enabled", defaults.VoiceProgressSpeechEnabledOrDefault()},
		{"agent.voice_max_response_tokens", defaults.VoiceMaxResponseTokens},
		{"agent.load_all_tools", defaults.LoadAllTools},
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

func TestConfigMeta_TTSFieldsFollowProviderCapabilities(t *testing.T) {
	idx := fieldIndex(t)
	hasPlaceholder := func(field FieldMeta, value interface{}, when VisibleRule) bool {
		for _, candidate := range field.PlaceholderWhen {
			if reflect.DeepEqual(candidate.Value, value) && reflect.DeepEqual(candidate.When, when) {
				return true
			}
		}
		return false
	}

	referenceID, ok := idx["tts_providers.reference_id"]
	if !ok {
		t.Fatal("missing tts_providers.reference_id metadata")
	}
	if referenceID.Widget != WidgetText {
		t.Fatalf("tts_providers.reference_id widget = %q, want %q", referenceID.Widget, WidgetText)
	}
	wantReferenceVisibility := VisibleRule{All: []Condition{{Field: "tts_providers.type", Op: "in", Values: []string{"fish-audio"}}}}
	if referenceID.VisibleWhen == nil || !reflect.DeepEqual(*referenceID.VisibleWhen, wantReferenceVisibility) {
		t.Fatalf("tts_providers.reference_id visibleWhen = %#v, want %#v", referenceID.VisibleWhen, wantReferenceVisibility)
	}
	if !hasPlaceholder(referenceID, "98655a12fa944e26b274c535e5e03842", wantReferenceVisibility) {
		t.Fatalf("tts_providers.reference_id missing Fish Audio conditional placeholder: %#v", referenceID.PlaceholderWhen)
	}

	model, ok := idx["tts_providers.model"]
	if !ok {
		t.Fatal("missing tts_providers.model metadata")
	}
	if model.Widget != WidgetText {
		t.Fatalf("tts_providers.model widget = %q, want %q", model.Widget, WidgetText)
	}
	wantModelSelect := VisibleRule{All: []Condition{{Field: "tts_providers.type", Op: "in", Values: []string{"openrouter"}}}}
	if model.SelectWhen == nil || !reflect.DeepEqual(*model.SelectWhen, wantModelSelect) {
		t.Fatalf("tts_providers.model selectWhen = %#v, want %#v", model.SelectWhen, wantModelSelect)
	}
	if model.VisibleWhen == nil {
		t.Fatal("tts_providers.model has no visibleWhen rule")
	}
	wantModelVisibility := VisibleRule{All: []Condition{{Field: "tts_providers.type", Op: "in", Values: []string{
		"minimax", "minimax-cn", "fish-audio", "alicloud", "volcengine", "openrouter",
	}}}}
	if !reflect.DeepEqual(*model.VisibleWhen, wantModelVisibility) {
		t.Fatalf("tts_providers.model visibleWhen = %#v, want %#v", *model.VisibleWhen, wantModelVisibility)
	}

	wantModelProviders := map[string][]string{
		"speech-2.8-hd":                       {"minimax", "minimax-cn"},
		"s2-pro":                              {"fish-audio"},
		"qwen-tts-realtime":                   {"alicloud"},
		"qwen3-tts-flash-realtime":            {"alicloud"},
		"seed-tts-2.0":                        {"volcengine"},
		"google/gemini-3.1-flash-tts-preview": {"openrouter"},
	}
	for value, wantProviders := range wantModelProviders {
		t.Run("model_"+value, func(t *testing.T) {
			for _, option := range model.Enum {
				if option.Value == value {
					if !reflect.DeepEqual(option.Providers, wantProviders) {
						t.Fatalf("model %q providers = %#v, want %#v", value, option.Providers, wantProviders)
					}
					return
				}
			}
			t.Fatalf("missing tts_providers.model option %q", value)
		})
	}

	voice := idx["tts_providers.voice_id"]
	voiceDefaults := []struct {
		value string
		when  VisibleRule
	}{
		{"male-qn-qingse", VisibleRule{All: []Condition{in("tts_providers.type", "minimax", "minimax-cn")}}},
		{"Cherry", VisibleRule{All: []Condition{in("tts_providers.type", "alicloud")}}},
		{"zh_female_vv_uranus_bigtts", VisibleRule{All: []Condition{in("tts_providers.type", "volcengine")}}},
		{"en-US-Neural2-C", VisibleRule{All: []Condition{in("tts_providers.type", "google-cloud")}}},
		{"Kore", VisibleRule{All: []Condition{eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "google/gemini-3.1-flash-tts-preview")}}},
		{"af_heart", VisibleRule{All: []Condition{eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "hexgrad/kokoro-82m")}}},
		{"en-US-AndrewMultilingualNeural", VisibleRule{All: []Condition{eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "microsoft/mai-voice-2")}}},
	}
	for _, want := range voiceDefaults {
		if !hasPlaceholder(voice, want.value, want.when) {
			t.Errorf("tts_providers.voice_id missing conditional placeholder %q for %#v", want.value, want.when)
		}
	}

	emotion := idx["tts_providers.emotion"]
	minimaxEmotion := VisibleRule{All: []Condition{in("tts_providers.type", "minimax", "minimax-cn")}}
	if !hasPlaceholder(emotion, "happy", minimaxEmotion) {
		t.Errorf("tts_providers.emotion missing MiniMax placeholder")
	}
}

func TestConfigMeta_ModelReasoningEffortProviderScoping(t *testing.T) {
	idx := fieldIndex(t)

	field, ok := idx["model.reasoning_effort"]
	if !ok {
		t.Fatal("missing model.reasoning_effort metadata")
	}

	options := map[string]EnumOption{}
	for _, option := range field.Enum {
		options[option.Value] = option
	}

	// auto plus the three levels every reasoning provider understands must stay
	// unscoped so they render for any provider.
	for _, value := range []string{"", "low", "medium", "high"} {
		option, ok := options[value]
		if !ok {
			t.Errorf("model.reasoning_effort enum missing option %q", value)
			continue
		}
		if len(option.Providers) != 0 {
			t.Errorf("option %q providers = %#v, want unscoped (all providers)", value, option.Providers)
		}
	}

	// "minimal" is supported by OpenRouter and Volcengine Ark.
	minimal, ok := options["minimal"]
	if !ok {
		t.Fatal("model.reasoning_effort enum missing option \"minimal\"")
	}
	if !reflect.DeepEqual(minimal.Providers, []string{"openrouter", "volcengine"}) {
		t.Errorf("option \"minimal\" providers = %#v, want [openrouter volcengine]", minimal.Providers)
	}

	// "none" is not supported by native Anthropic or Ark; offering it there would produce a 400.
	none, ok := options["none"]
	if !ok {
		t.Fatal("model.reasoning_effort enum missing option \"none\"")
	}
	if len(none.Providers) == 0 {
		t.Fatal("option \"none\" is unscoped, want it hidden for the volcengine provider")
	}
	for _, provider := range none.Providers {
		if provider == "volcengine" || provider == "anthropic" {
			t.Errorf("option \"none\" is offered for %s, which rejects it", provider)
		}
	}
}

func TestConfigMeta_STTTencentASRProviderMetadata(t *testing.T) {
	idx := fieldIndex(t)

	provider, ok := idx["stt.provider"]
	if !ok {
		t.Fatal("missing stt.provider metadata")
	}
	values := make(map[string]bool, len(provider.Enum))
	for _, option := range provider.Enum {
		values[option.Value] = true
	}
	if !values[tencentASRProvider] {
		t.Fatalf("stt.provider enum missing canonical %s option: %#v", tencentASRProvider, provider.Enum)
	}
	providerNames := sttProviderNamesForCanonical(tencentASRProvider)
	if len(providerNames) == 0 {
		t.Fatalf("Tencent STT provider %q is not registered", tencentASRProvider)
	}
	for _, alias := range providerNames[1:] {
		if values[alias] {
			t.Fatalf("stt.provider enum still includes legacy alias %q: %#v", alias, provider.Enum)
		}
	}

	tests := []struct {
		path string
		want any
	}{
		// The Tencent credential set moved onto the record: it is meaningless
		// for any other provider type, so it is edited in the provider dialog.
		{"stt_providers.app_id", ""},
		{"stt_providers.region", "ap-shanghai"},
		// language stays flat: it holds regardless of which provider transcribes.
		{"stt.language", "zh"},
	}
	for _, tt := range tests {
		field, ok := idx[tt.path]
		if !ok {
			t.Fatalf("missing %s metadata", tt.path)
		}
		if field.Default != tt.want {
			t.Fatalf("%s default = %#v, want %#v", tt.path, field.Default, tt.want)
		}
	}
}

func TestConfigMeta_AudioArchiveRequiresSTTInputMode(t *testing.T) {
	idx := fieldIndex(t)

	tests := []string{
		"audio_archive.enabled",
		"audio_archive.storage_path",
		"audio_archive.max_files",
		"audio_archive.max_size_mb",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			field, ok := idx[path]
			if !ok {
				t.Fatalf("missing metadata field %s", path)
			}
			if field.VisibleWhen == nil {
				t.Fatalf("%s has no visibleWhen rule", path)
			}
			for _, cond := range field.VisibleWhen.All {
				if cond.Field == "agent.input_mode" && cond.Op == "eq" && cond.Value == "stt" {
					return
				}
			}
			t.Fatalf("%s visibleWhen = %#v, want agent.input_mode == stt", path, field.VisibleWhen)
		})
	}
}

func TestConfigMeta_ModelAPIKeyOwnedByProvider(t *testing.T) {
	idx := fieldIndex(t)
	if _, ok := idx["model.api_key"]; ok {
		t.Fatal("model.api_key must not be exposed by config web metadata")
	}
	field, ok := idx["model_providers.api_key"]
	if !ok {
		t.Fatal("model_providers.api_key is missing from config web metadata")
	}
	if !field.Secret {
		t.Fatal("model_providers.api_key must remain a secret field")
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
		// The voice credential fields are edited on a [tts_providers]/
		// [stt_providers] record rather than on the flat section, so metadata
		// for them lives in the record section. They still resolve onto these
		// structs at load, so they must be covered in one place or the other --
		// see the alt lookup below.
		{"tts", reflect.TypeOf(TTSConfig{}), map[string]bool{"credentials": true}},
		{"stt", reflect.TypeOf(STTConfig{}), nil},
		{"audio", reflect.TypeOf(AudioConfig{}), nil},
		{"audio_archive", reflect.TypeOf(AudioArchiveConfig{}), nil},
		{"device", reflect.TypeOf(DeviceConfig{}), map[string]bool{"backend": true}},
		{"hid", reflect.TypeOf(HIDConfig{}), map[string]bool{"pointer_mode": true}},
		{"search", reflect.TypeOf(SearchConfig{}), nil},
		{"log", reflect.TypeOf(LogConfig{}), nil},
		{"telemetry", reflect.TypeOf(TelemetryConfig{}), nil},
		{"live_activity", reflect.TypeOf(LiveActivityConfig{}), nil},
	}

	// A voice credential field may be described on the flat section or on its
	// provider record section. Accepting either keeps the real guarantee -- no
	// config field silently loses its UI -- without pinning down which of the
	// two editors owns it.
	altSection := map[string]string{"tts": "tts_providers", "stt": "stt_providers"}
	// model.api_key remains in ModelConfig, but config web edits it through the
	// selected provider record.
	altFieldSection := map[string]map[string]string{
		"model": {"api_key": "model_providers"},
	}

	for _, s := range sections {
		for name := range tomlKeys(s.typ) {
			if s.skip[name] {
				continue
			}
			path := s.name + "." + name
			if _, ok := idx[path]; ok {
				continue
			}
			if alt, hasAlt := altSection[s.name]; hasAlt {
				if _, ok := idx[alt+"."+name]; ok {
					continue
				}
			}
			if fields, hasFields := altFieldSection[s.name]; hasFields {
				if alt, hasAlt := fields[name]; hasAlt {
					if _, ok := idx[alt+"."+name]; ok {
						continue
					}
				}
			}
			t.Errorf("config field %s has no metadata entry", path)
		}
	}

	// The record types themselves must be fully described, or a field the
	// backend reads would have no editor at all.
	recordSections := []sectionType{
		{"model_providers", reflect.TypeOf(ModelProvider{}), nil},
		{"tts_providers", reflect.TypeOf(TTSProvider{}), nil},
		{"stt_providers", reflect.TypeOf(STTProvider{}), nil},
	}
	for _, s := range recordSections {
		for name := range tomlKeys(s.typ) {
			if s.skip[name] {
				continue
			}
			path := s.name + "." + name
			if _, ok := idx[path]; !ok {
				t.Errorf("voice provider record field %s has no metadata entry", path)
			}
		}
	}

	// The "agent" UI section maps to top-level Config fields. Only flat
	// (scalar/string/bool) fields are rendered; nested structs and infra-only
	// fields are surfaced under their own sections or not at all.
	agentSkip := map[string]bool{
		"model": true, "tts": true, "stt": true, "device": true, "hid": true,
		"audio": true, "search": true, "log": true, "telemetry": true, "termination_policy": true, "live_activity": true,
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
