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
// fields (instruction, additional_prompt) intentionally carry no default: the
// first-boot seed prompt is product content owned by the provisioning path,
// not UI metadata.
func ConfigMeta() ConfigMetadata {
	return ConfigMetadata{
		Sections: []SectionMeta{
			{
				Name: "model",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("openrouter", "openai", "ollama", "fake"),
						Default: "openrouter"},
					{Key: "token_env", Widget: WidgetText},
					{Key: "model", Widget: WidgetText, Default: "bytedance-seed/seed-2.0-lite"},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(ne("model.provider", "openrouter"))},
					{Key: "temperature", Widget: WidgetNumber, Default: 0.2},
					{Key: "max_response_tokens", Widget: WidgetNumber, Default: 1000},
					{Key: "context_window", Widget: WidgetNumber, Default: 0},
					{Key: "model_max_output_tokens", Widget: WidgetNumber, Default: 0},
				},
			},
			{
				Name: "tts",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("minimax-ws", "fish-audio", "alicloud", "volcengine"),
						Default: "minimax-ws"},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "model", Widget: WidgetText},
					{Key: "voice_id", Widget: WidgetText, Default: "male-qn-qingse"},
					{Key: "emotion", Widget: WidgetText, Default: "happy",
						VisibleWhen: all(in("tts.provider", "minimax-ws", "volcengine"))},
					{Key: "speed", Widget: WidgetSelect, Default: 1.0,
						Range: &Range{Min: 0.5, Max: 2, Step: 0.1, Precision: 1}},
				},
			},
			{
				Name: "stt",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions("openai-whisper", "openrouter", "tencent", "tencent_asr"),
						Default: "openai-whisper"},
					{Key: "api_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "model", Widget: WidgetText, Default: "whisper-1",
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(in("stt.provider", "openai-whisper", "openrouter"))},
					{Key: "secret_id", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", "tencent", "tencent_asr"))},
					{Key: "secret_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt.provider", "tencent", "tencent_asr"))},
					{Key: "region", Widget: WidgetText,
						VisibleWhen: all(in("stt.provider", "tencent", "tencent_asr"))},
					{Key: "engine_model_type", Widget: WidgetText,
						VisibleWhen: all(in("stt.provider", "tencent", "tencent_asr"))},
				},
			},
			{
				Name: "audio",
				Fields: []FieldMeta{
					{Key: "socket", Widget: WidgetText, Default: "/run/audio_service/audio_service.sock"},
					{Key: "sample_rate", Widget: WidgetNumber, Default: 16000},
					{Key: "channels", Widget: WidgetNumber, Default: 1},
					{Key: "bit_width", Widget: WidgetNumber, Default: 16},
				},
			},
			{
				Name: "benchmark",
				Fields: []FieldMeta{
					{Key: "judge_model", Widget: WidgetText,
						Default: "bytedance-seed/seed-2.0-lite"},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "benchmark_dir", Widget: WidgetText},
				},
			},
			{
				Name: "hid",
				Fields: []FieldMeta{
					{Key: "pointer_mode", Widget: WidgetSelect,
						Enum:    enumOptions("absolute", "touchscreen"),
						Default: "absolute"},
					{Key: "keyboard_device", Widget: WidgetText, Default: "/dev/hidg0"},
					{Key: "mouse_device", Widget: WidgetText, Default: "/dev/hidg1"},
					{Key: "frame_socket", Widget: WidgetText, Default: "/run/frame_service/frame_service.sock"},
				},
			},
			{
				Name: "search",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions(searchProviderDuckDuckGo, searchProviderBrave, "brave-free", searchProviderTavily),
						Default: searchProviderDuckDuckGo},
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
						Default: "text"},
					{Key: "trigger_mode", Widget: WidgetSelect,
						Enum:        enumOptions("manual", "wakeup"),
						Default:     "manual",
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_backend", Widget: WidgetSelect,
						Enum:        enumOptions(defaultVADBackend, "cpu"),
						Default:     defaultVADBackend,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_model_path", Widget: WidgetText,
						Default:     defaultVADModelPath,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_helper_path", Widget: WidgetText,
						Default:     defaultVADHelperPath,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "vad_speech_threshold", Widget: WidgetSelect,
						Default:     defaultVADSpeechThreshold,
						Range:       &Range{Min: 0, Max: 1, Step: 0.05, Precision: 2},
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "silence_ms", Widget: WidgetNumber, Default: 650,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "min_speech_ms", Widget: WidgetNumber, Default: 300,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_session_enabled", Widget: WidgetBoolean, Default: true,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"))},
					{Key: "voice_followup_timeout_ms", Widget: WidgetNumber, Default: 6000,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_session_enabled"))},
					{Key: "voice_first_turn_timeout_ms", Widget: WidgetNumber, Default: 10000,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_session_enabled"))},
					{Key: "voice_max_turns", Widget: WidgetNumber, Default: 0,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_session_enabled"))},
					{Key: "voice_interrupt_on_wakeup", Widget: WidgetBoolean, Default: true,
						VisibleWhen: all(eq("agent.input_mode", "stt"), eq("agent.trigger_mode", "wakeup"), truthy("agent.voice_session_enabled"))},
					{Key: "voice_streaming_tts_enabled", Widget: WidgetBoolean, Default: true,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_tool_call_speech", Widget: WidgetBoolean, Default: true,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "voice_max_response_tokens", Widget: WidgetNumber, Default: 400,
						VisibleWhen: all(in("agent.input_mode", "stt", "audio"))},
					{Key: "max_iterations", Widget: WidgetNumber, Default: -1},
					{Key: "screenshot_keep_n", Widget: WidgetNumber, Default: 3},
					{Key: "screenshot_prune_interval", Widget: WidgetNumber, Default: 25},
					{Key: "screen_stable_timeout_ms", Widget: WidgetNumber, Default: 3500},
					{Key: "screen_stable_ms", Widget: WidgetNumber, Default: 500},
					{Key: "screen_stable_diff_threshold", Widget: WidgetNumber, Default: 2.0},
					{Key: "instruction", Widget: WidgetTextarea},
					{Key: "additional_prompt", Widget: WidgetTextarea},
				},
			},
		},
	}
}
