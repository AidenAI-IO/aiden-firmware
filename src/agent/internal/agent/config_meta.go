package agent

// This file is the single source of truth for config field metadata consumed
// by the config web UI (via the `agent config-meta` CLI subcommand). It
// describes how each field is rendered and defaulted, and the conditions under
// which it is shown. Validation rules still live in Config.Validate(); the
// enums here are kept consistent with the constants used there.

// Widget identifies how the config web UI should render a field.
type Widget string

const (
	WidgetText     Widget = "text"
	WidgetTextarea Widget = "textarea"
	WidgetNumber   Widget = "number"
	WidgetBoolean  Widget = "boolean"
	WidgetSelect   Widget = "select"
	WidgetList     Widget = "list"
)

// EnumOption is a single choice for a select widget. Label may differ from
// Value (e.g. an empty value shown as "langfuse (default)").
type EnumOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Range describes the bounds for a numeric field. When a number field also
// carries a Range, the UI renders it as a select of discrete steps.
type Range struct {
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	Step      float64 `json:"step"`
	Precision int     `json:"precision,omitempty"`
}

// Condition is a single predicate against another field's current value.
// Field is a dotted path like "model.provider" or "agent.input_mode".
type Condition struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`               // eq | ne | in | notIn | truthy
	Value  string   `json:"value,omitempty"`  // for eq / ne
	Values []string `json:"values,omitempty"` // for in / notIn
}

// VisibleRule is a declarative visibility expression. Exactly one of All/Any
// is populated. All = logical AND, Any = logical OR.
type VisibleRule struct {
	All []Condition `json:"all,omitempty"`
	Any []Condition `json:"any,omitempty"`
}

// FieldMeta describes a single configurable field.
type FieldMeta struct {
	Key         string       `json:"key"`
	Widget      Widget       `json:"widget"`
	Enum        []EnumOption `json:"enum,omitempty"`
	Range       *Range       `json:"range,omitempty"`
	Default     interface{}  `json:"default,omitempty"`
	Secret      bool         `json:"secret,omitempty"`
	VisibleWhen *VisibleRule `json:"visibleWhen,omitempty"`
}

// SectionMeta groups fields under a UI section. Section names match the JSON
// object keys the UI reads/writes (e.g. "model", "agent").
type SectionMeta struct {
	Name   string      `json:"name"`
	Fields []FieldMeta `json:"fields"`
}

// ConfigMetadata is the top-level payload emitted by `agent config-meta`.
type ConfigMetadata struct {
	Sections []SectionMeta `json:"sections"`
}

// enumOptions builds plain value==label options from raw strings.
func enumOptions(values ...string) []EnumOption {
	opts := make([]EnumOption, 0, len(values))
	for _, v := range values {
		opts = append(opts, EnumOption{Value: v})
	}
	return opts
}

// eq/ne/in/notIn/truthy are small helpers for building conditions.
func eq(field, value string) Condition { return Condition{Field: field, Op: "eq", Value: value} }
func ne(field, value string) Condition { return Condition{Field: field, Op: "ne", Value: value} }
func truthy(field string) Condition    { return Condition{Field: field, Op: "truthy"} }
func in(field string, vs ...string) Condition {
	return Condition{Field: field, Op: "in", Values: vs}
}

func all(conds ...Condition) *VisibleRule { return &VisibleRule{All: conds} }

// ConfigMeta returns the full field metadata for the config web UI. Defaults
// here are the canonical defaults for the device's agent.toml. Free-text
// fields (custom_instruction, additional_prompt) intentionally carry no
// metadata default: the built-in prompt is runtime content, while
// custom_instruction is only an override.
func ConfigMeta() ConfigMetadata {
	defaults := DefaultConfig()
	return ConfigMetadata{
		Sections: []SectionMeta{
			{
				Name: "model",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("openrouter", "openai", "ollama", "fake"),
						Default: defaults.Model.Provider},
					{Key: "token_env", Widget: WidgetText},
					{Key: "model", Widget: WidgetText, Default: defaults.Model.Model},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(ne("model.provider", "openrouter"))},
					{Key: "temperature", Widget: WidgetNumber, Default: defaults.Model.Temperature},
					{Key: "max_response_tokens", Widget: WidgetNumber, Default: defaults.Model.MaxResponseTokens},
					{Key: "context_window", Widget: WidgetNumber, Default: defaults.Model.ContextWindow},
					{Key: "model_max_output_tokens", Widget: WidgetNumber, Default: defaults.Model.ModelMaxOutputTokens},
				},
			},
			{
				Name: "tts",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("minimax-ws", "fish-audio", "alicloud", "volcengine"),
						Default: defaults.TTS.Provider},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "model", Widget: WidgetText,
						VisibleWhen: all(ne("tts.provider", "minimax-ws"))},
					{Key: "voice_id", Widget: WidgetText, Default: defaults.TTS.VoiceID},
					{Key: "emotion", Widget: WidgetText, Default: defaults.TTS.Emotion,
						VisibleWhen: all(in("tts.provider", "minimax-ws", "volcengine"))},
					{Key: "speed", Widget: WidgetSelect, Default: defaults.TTS.Speed,
						Range: &Range{Min: 0.5, Max: 2, Step: 0.1, Precision: 1}},
				},
			},
			{
				Name: "stt",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("openai-whisper", "openrouter", tencentASRProvider),
						Default: defaults.STT.Provider},
					{Key: "api_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "model", Widget: WidgetText, Default: defaults.STT.Model,
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "secret_id", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", tencentASRProvider, legacyTencentProvider, legacyTencentASRProvider))},
					{Key: "secret_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", tencentASRProvider, legacyTencentProvider, legacyTencentASRProvider))},
					{Key: "region", Widget: WidgetText, Default: defaultTencentASRRegion,
						VisibleWhen: all(in("stt.provider", tencentASRProvider, legacyTencentProvider, legacyTencentASRProvider))},
					{Key: "engine_model_type", Widget: WidgetText, Default: defaultTencentASREngineModel,
						VisibleWhen: all(in("stt.provider", tencentASRProvider, legacyTencentProvider, legacyTencentASRProvider))},
				},
			},
			{
				Name: "audio",
				Fields: []FieldMeta{
					{Key: "socket", Widget: WidgetText, Default: defaults.Audio.Socket},
					{Key: "sample_rate", Widget: WidgetNumber, Default: defaults.Audio.SampleRate},
					{Key: "channels", Widget: WidgetNumber, Default: defaults.Audio.Channels},
					{Key: "bit_width", Widget: WidgetNumber, Default: defaults.Audio.BitWidth},
				},
			},
			{
				Name: "audio_archive",
				Fields: []FieldMeta{
					{Key: "enabled", Widget: WidgetBoolean, Default: defaults.AudioArchive.Enabled,
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "storage_path", Widget: WidgetText, Default: defaults.AudioArchive.StoragePathOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
					{Key: "max_files", Widget: WidgetNumber, Default: defaults.AudioArchive.MaxFilesOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
					{Key: "max_size_mb", Widget: WidgetNumber, Default: defaults.AudioArchive.MaxSizeMBOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
				},
			},
			{
				Name: "benchmark",
				Fields: []FieldMeta{
					{Key: "judge_model", Widget: WidgetText,
						Default: defaults.Benchmark.JudgeModel},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "benchmark_dir", Widget: WidgetText},
				},
			},
			{
				Name: "hid",
				Fields: []FieldMeta{
					{Key: "pointer_mode", Widget: WidgetSelect,
						Enum:    enumOptions("absolute", "touchscreen"),
						Default: defaults.HID.PointerMode},
					{Key: "keyboard_device", Widget: WidgetText, Default: defaults.HID.KeyboardDevice},
					{Key: "mouse_device", Widget: WidgetText, Default: defaults.HID.MouseDevice},
					{Key: "frame_socket", Widget: WidgetText, Default: defaults.HID.FrameSocket},
				},
			},
			{
				Name: "search",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions(searchProviderDuckDuckGo, searchProviderBrave, searchProviderTavily),
						Default: defaults.Search.ProviderOrDefault()},
					{Key: "api_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(ne("search.provider", searchProviderDuckDuckGo))},
				},
			},
			{
				Name: "telemetry",
				Fields: []FieldMeta{
					{Key: "enabled", Widget: WidgetBoolean, Default: false},
					{Key: "provider", Widget: WidgetSelect,
						Enum:        []EnumOption{{Value: "", Label: "langfuse (default)"}, {Value: "langfuse"}},
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "public_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "secret_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "upload_screenshots", Widget: WidgetBoolean,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "upload_timeout_sec", Widget: WidgetNumber,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "max_retry", Widget: WidgetNumber,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "environment", Widget: WidgetText,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "tags", Widget: WidgetList,
						VisibleWhen: all(truthy("telemetry.enabled"))},
				},
			},
			{
				Name: "live_activity",
				Fields: []FieldMeta{
					{Key: "enabled", Widget: WidgetBoolean, Default: true},
					{Key: "bundle_id", Widget: WidgetText,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "topic", Widget: WidgetText,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "environment", Widget: WidgetSelect,
						Enum:        enumOptions("sandbox", "production"),
						Default:     "sandbox",
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "team_id", Widget: WidgetText, Secret: true,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "key_id", Widget: WidgetText, Secret: true,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "private_key_path", Widget: WidgetText, Secret: true,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "private_key_pem", Widget: WidgetTextarea, Secret: true,
						VisibleWhen: all(truthy("live_activity.enabled"))},
					{Key: "timeout_sec", Widget: WidgetNumber, Default: 10,
						VisibleWhen: all(truthy("live_activity.enabled"))},
				},
			},
			{
				// The "agent" UI section maps to the top-level Config fields.
				Name: "agent",
				Fields: []FieldMeta{
					{Key: "input_mode", Widget: WidgetSelect,
						Enum:    enumOptions("text", "stt", "audio"),
						Default: defaults.InputMode},
					{Key: "trigger_mode", Widget: WidgetSelect,
						Enum:        enumOptions("manual", "wakeup"),
						Default:     defaults.TriggerMode,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_backend", Widget: WidgetSelect,
						Enum:        enumOptions(defaultVADBackend, "cpu"),
						Default:     defaults.VADBackend,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_model_path", Widget: WidgetText,
						Default:     defaults.VADModelPath,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_helper_path", Widget: WidgetText,
						Default:     defaults.VADHelperPath,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_speech_threshold", Widget: WidgetSelect,
						Default:     defaults.VADSpeechThreshold,
						Range:       &Range{Min: 0, Max: 1, Step: 0.05, Precision: 2},
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "silence_ms", Widget: WidgetNumber, Default: defaults.SilenceMs,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "min_speech_ms", Widget: WidgetNumber, Default: defaults.MinSpeechMs,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_followup_enabled", Widget: WidgetBoolean, Default: defaults.VoiceFollowupEnabledOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"))},
					{Key: "voice_followup_timeout_ms", Widget: WidgetNumber, Default: defaults.VoiceFollowupTimeoutMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_first_turn_timeout_ms", Widget: WidgetNumber, Default: defaults.VoiceFirstTurnTimeoutMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_max_turns", Widget: WidgetNumber, Default: defaults.VoiceMaxTurns,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_interrupt_on_wakeup", Widget: WidgetBoolean, Default: defaults.VoiceInterruptOnWakeupOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_streaming_tts_enabled", Widget: WidgetBoolean, Default: defaults.VoiceStreamingTTSEnabledOrDefault(),
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_tool_call_speech", Widget: WidgetBoolean, Default: defaults.VoiceToolCallSpeechOrDefault(),
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_progress_speech_enabled", Widget: WidgetBoolean, Default: defaults.VoiceProgressSpeechEnabledOrDefault(),
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_speech_summary_enabled", Widget: WidgetBoolean, Default: defaults.VoiceSpeechSummaryEnabledOrDefault(),
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_max_response_tokens", Widget: WidgetNumber, Default: defaults.VoiceMaxResponseTokens,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "todo_reminder_tool_calls", Widget: WidgetNumber, Default: defaults.TodoReminderToolCallsOrDefault()},
					{Key: "max_iterations", Widget: WidgetNumber, Default: defaults.MaxIterations},
					{Key: "force_simple_loop", Widget: WidgetBoolean, Default: defaults.ForceSimpleLoop},
					{Key: "screenshot_keep_n", Widget: WidgetNumber, Default: defaults.ScreenshotKeepN},
					{Key: "screenshot_prune_interval", Widget: WidgetNumber, Default: defaults.ScreenshotPruneInterval},
					{Key: "screen_stable_timeout_ms", Widget: WidgetNumber, Default: defaults.ScreenStableTimeoutMs},
					{Key: "screen_stable_ms", Widget: WidgetNumber, Default: defaults.ScreenStableMs},
					{Key: "screen_stable_diff_threshold", Widget: WidgetNumber, Default: defaults.ScreenStableDiffThreshold},
					{Key: "default_platform", Widget: WidgetSelect,
						Enum:    enumOptions("", "ios", "android", "mac"),
						Default: defaults.DefaultPlatform},
					{Key: "custom_instruction", Widget: WidgetTextarea},
					{Key: "additional_prompt", Widget: WidgetTextarea},
				},
			},
		},
	}
}
