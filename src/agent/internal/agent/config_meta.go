package agent

import (
	"aiden-agent/internal/agent/realtimevoice"
	"aiden-agent/internal/agent/tts"
)

// This file is the single source of truth for config field metadata consumed
// by the config web UI (via the `agent config-meta` CLI subcommand). It
// describes how each field is rendered, typed and defaulted, and the conditions
// under which it is shown. Config web derives JSON field type checks from the
// widget metadata; semantic validation still lives in Config.Validate(). The
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

// EnumOption is a single predefined choice. Label may differ from Value (e.g.
// an empty value shown as "langfuse (default)"). Text widgets may also carry
// choices when SelectWhen conditionally renders them as a select.
type EnumOption struct {
	Value     string   `json:"value"`
	Label     string   `json:"label,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

func realtimeProviderEnumOptions() []EnumOption {
	descriptors := realtimevoice.ProviderDescriptors()
	options := make([]EnumOption, 0, len(descriptors))
	for _, descriptor := range descriptors {
		options = append(options, EnumOption{Value: descriptor.Name, Label: descriptor.Label})
	}
	return options
}

func realtimeProviderPlaceholders(field string) []ConditionalPlaceholder {
	descriptors := realtimevoice.ProviderDescriptors()
	placeholders := make([]ConditionalPlaceholder, 0, len(descriptors))
	for _, descriptor := range descriptors {
		value := descriptor.ModelPlaceholder
		if field == "voice" {
			value = descriptor.VoicePlaceholder
		}
		if value == "" {
			continue
		}
		placeholders = append(placeholders, ConditionalPlaceholder{
			When:  VisibleRule{All: []Condition{eq("voice_model_providers.type", descriptor.Name)}},
			Value: value,
		})
	}
	return placeholders
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

// ConditionalPlaceholder supplies a provider/model-specific placeholder when
// its condition matches. It documents the runtime recommendation without
// writing the example value into the user's configuration.
type ConditionalPlaceholder struct {
	When  VisibleRule `json:"when"`
	Value interface{} `json:"value"`
}

// FieldMeta describes a single configurable field.
type FieldMeta struct {
	Key             string                   `json:"key"`
	Label           string                   `json:"label,omitempty"`
	Help            string                   `json:"help,omitempty"`
	Placeholder     string                   `json:"placeholder,omitempty"`
	Layout          string                   `json:"layout,omitempty"`
	Widget          Widget                   `json:"widget"`
	Enum            []EnumOption             `json:"enum,omitempty"`
	Range           *Range                   `json:"range,omitempty"`
	Default         interface{}              `json:"default,omitempty"`
	PlaceholderWhen []ConditionalPlaceholder `json:"placeholderWhen,omitempty"`
	Secret          bool                     `json:"secret,omitempty"`
	Advanced        bool                     `json:"advanced,omitempty"`
	Nullable        bool                     `json:"nullable,omitempty"` // For number fields: empty input means unset (omit key), not 0
	VisibleWhen     *VisibleRule             `json:"visibleWhen,omitempty"`
	SelectWhen      *VisibleRule             `json:"selectWhen,omitempty"`
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

func keyboardLayoutEnumOptions() []EnumOption {
	options := make([]EnumOption, 0, len(keyboardLayoutDefinitions))
	for _, layout := range keyboardLayoutDefinitions {
		options = append(options, EnumOption{Value: layout.value, Label: layout.label})
	}
	return options
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
func notIn(field string, vs ...string) Condition {
	return Condition{Field: field, Op: "notIn", Values: vs}
}

func all(conds ...Condition) *VisibleRule { return &VisibleRule{All: conds} }

func placeholderWhen(value interface{}, conds ...Condition) ConditionalPlaceholder {
	return ConditionalPlaceholder{When: VisibleRule{All: conds}, Value: value}
}

// withDisplayDefaults ensures every field has a stable label even when the
// form does not need custom presentation metadata. Config keys are already the
// labels used by the existing web UI, so using the key preserves that contract.
func withDisplayDefaults(metadata ConfigMetadata) ConfigMetadata {
	for sectionIndex := range metadata.Sections {
		for fieldIndex := range metadata.Sections[sectionIndex].Fields {
			field := &metadata.Sections[sectionIndex].Fields[fieldIndex]
			if field.Label == "" {
				field.Label = field.Key
			}
		}
	}
	return metadata
}

// ConfigMeta returns the full field metadata for the config web UI. Defaults
// here are the canonical defaults for the device's agent.toml. Free-text
// fields (custom_instruction, additional_prompt) intentionally carry no
// metadata default: the built-in prompt is runtime content, while
// custom_instruction is only an override.
func ConfigMeta() ConfigMetadata {
	defaults := DefaultConfig()
	tencentSTTProviderNames := sttProviderNamesForCanonical(tencentASRProvider)
	metadata := ConfigMetadata{
		Sections: []SectionMeta{
			{
				Name: "device",
				Fields: []FieldMeta{
					{Key: "device_type", Widget: WidgetSelect,
						Help:    "Android uses HID touchscreen mode. iOS, macOS, windows, and linux use absolute pointer mode.",
						Enum:    enumOptions("iOS", "Android", "macOS", "windows", "linux"),
						Default: defaults.Device.DeviceTypeOrDefault()},
				},
			},
			{
				Name: "model",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Layout:  "wide",
						Enum:    enumOptions(modelProviderTypesForConfigUI()...),
						Default: defaults.Model.Provider},
					{Key: "model", Widget: WidgetText, Default: defaults.Model.Model, Layout: "wide"},
					{Key: "api_mode", Label: "Conversation API", Widget: WidgetSelect,
						Help: "Choose who manages conversation context. Local context sends history without provider storage; provider context stores responses and continues from the previous response ID.",
						Enum: []EnumOption{
							{Value: "", Label: "Chat Completions (compatible)"},
							{Value: "responses", Label: "Responses (local context)", Providers: []string{"openai", "openrouter", "volcengine"}},
							{Value: "responses_stateful", Label: "Responses (provider context)", Providers: []string{"openai", "volcengine"}},
						},
						Default: defaults.Model.APIMode, Layout: "wide"},
					{Key: "responses_context_management", Label: "Provider compaction", Widget: WidgetSelect,
						Help: "Choose provider-side context cleanup. OpenAI uses token compaction; Volcengine Ark uses tool-call and thinking edits.",
						Enum: []EnumOption{
							{Value: "", Label: "Off (recommended)"},
							{Value: "compaction", Label: "Compact at threshold", Providers: []string{"openai"}},
							{Value: "ark_context_edit", Label: "Ark context edits", Providers: []string{"volcengine"}},
						},
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful")),
						Default:     defaults.Model.ResponsesContextManagement,
						Layout:      "wide"},
					{Key: "responses_compact_threshold", Label: "Compaction threshold (tokens)", Widget: WidgetNumber,
						Help:        "Token count that triggers provider compaction. 0 lets the provider choose.",
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful"), eq("model.responses_context_management", "compaction")),
						Default:     defaults.Model.ResponsesCompactThreshold, Placeholder: "0 = provider default"},
					{Key: "responses_context_edit_trigger", Label: "Ark tool-call trigger", Widget: WidgetNumber,
						Help:        "After this many tool calls, Ark clears old tool inputs. 0 uses the recommended value 10.",
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful"), eq("model.responses_context_management", "ark_context_edit")),
						Default:     defaults.Model.ResponsesContextEditTrigger, Placeholder: "10 = recommended"},
					{Key: "responses_context_edit_keep", Label: "Ark tool calls to keep", Widget: WidgetNumber,
						Help:        "Number of recent tool calls Ark keeps after cleanup. 0 uses the recommended value 3.",
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful"), eq("model.responses_context_management", "ark_context_edit")),
						Default:     defaults.Model.ResponsesContextEditKeep, Placeholder: "3 = recommended"},
					{Key: "responses_context_edit_clear_thinking", Label: "Clear old thinking", Widget: WidgetBoolean,
						Help:        "Ask Ark to remove previous thinking turns when it applies the context edit.",
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful"), eq("model.responses_context_management", "ark_context_edit")),
						Default:     defaults.Model.ResponsesContextEditClearThinking},
					{Key: "responses_truncation", Label: "Over-limit input", Widget: WidgetSelect,
						Help: "Fail when input is too long, or let a compatible provider discard the oldest input and continue.",
						Enum: []EnumOption{
							{Value: "", Label: "Fail with an error (default)"},
							{Value: "auto", Label: "Discard oldest input automatically", Providers: []string{"openai", "openrouter"}},
						},
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful")),
						Default:     defaults.Model.ResponsesTruncation},
					{Key: "responses_include", Label: "Extra response fields", Widget: WidgetList, Layout: "wide",
						Help:        "Usually leave empty. Add provider-supported include values only when needed, one per line.",
						Placeholder: "reasoning.encrypted_content",
						VisibleWhen: all(in("model.api_mode", "responses", "responses_stateful")),
						Default:     defaults.Model.ResponsesInclude},
					// The effective default is model-dependent (resolved at load
					// time); show the global fallback here as the UI placeholder.
					{Key: "temperature", Widget: WidgetNumber, Default: defaultModelTemperature, Nullable: true},
					{Key: "max_response_tokens", Widget: WidgetNumber, Default: defaults.Model.MaxResponseTokens},
					{Key: "log_raw_http", Widget: WidgetBoolean, Default: defaults.Model.LogRawHTTP},
					// Reasoning levels differ per provider: "minimal" is supported by
					// OpenRouter and Volcengine Ark; "none" is the OpenAI-style off
					// switch that Ark does not recognize. Scope each to the providers
					// that accept it so the UI cannot save a value the endpoint
					// rejects; auto plus low/medium/high stay unscoped.
					{Key: "reasoning_effort", Widget: WidgetSelect,
						Help: "Empty = auto. Options follow the selected model capability; none is shown only when the model supports disabling reasoning.",
						Enum: []EnumOption{
							{Value: "", Label: "auto (default)"},
							{Value: "minimal", Label: "minimal (no reasoning)", Providers: []string{"openrouter", "volcengine"}},
							{Value: "none", Label: "none", Providers: []string{"openrouter", "openai", "kimi", "kimi-cn", "ollama", "fake"}},
							{Value: "low", Label: "low"},
							{Value: "medium", Label: "medium"},
							{Value: "high", Label: "high"},
							{Value: "xhigh", Label: "xhigh", Providers: []string{"anthropic", "openrouter", "openai", "kimi", "kimi-cn", "volcengine"}},
							{Value: "max", Label: "max", Providers: []string{"anthropic", "openrouter", "openai", "kimi", "kimi-cn", "volcengine"}},
						},
						Default: defaults.Model.ReasoningEffort},
					{Key: "reasoning_budget_tokens", Widget: WidgetNumber,
						Label:       "Reasoning budget (tokens)",
						Placeholder: "0 = auto",
						Help:        "Optional exact reasoning budget. Shown for models that expose budget_tokens; 0 uses the model default or effort preset. Reasoning tokens are drawn from max_response_tokens, so this must be at least 1024 and smaller than that limit.",
						Default:     0},
					{Key: "context_window", Widget: WidgetNumber, Default: defaults.Model.ContextWindow,
						Placeholder: "0 = auto", Help: "0 = auto: use provider metadata when available."},
					{Key: "model_max_output_tokens", Widget: WidgetNumber, Default: defaults.Model.ModelMaxOutputTokens,
						Placeholder: "0 = auto", Help: "0 = auto: use provider metadata when available."},
				},
			},
			// model_providers describes one [model_providers.<name>] record, the same shape
			// [tts_providers] and [stt_providers] give voice. Each holds the
			// credentials and settings for one LLM service, and [model] references
			// one by putting the name in its own provider field. Several providers
			// stay configured at once, so switching is a one-line change instead of
			// a re-entry of keys.
			//
			// Every rule keys on model_providers.type — the record's own type select.
			{
				Name: "model_providers",
				Fields: []FieldMeta{
					{Key: "type", Widget: WidgetSelect,
						Enum: enumOptions(modelProviderTypesForConfigUI()...)},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(in("model_providers.type", modelProviderTypesAllowingCustomBaseURLForConfigUI()...))},
				},
			},
			// [tts] keeps only the provider reference and the settings that are
			// genuinely global. Everything that stops meaning anything when the
			// provider changes lives on the record instead -- see tts_providers.
			//
			// The provider enum here is the legacy bare-type list, which is what
			// a pre-records config carries. The UI replaces these options with
			// the configured record names at runtime.
			{
				Name: "tts",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Layout:  "wide",
						Enum:    enumOptions(tts.AvailableProviders()...),
						Default: defaults.TTS.Provider},
					// speed is a listening preference, not a credential: it must
					// not change when the voice changes, so it stays global.
					{Key: "speed", Widget: WidgetSelect, Default: defaults.TTS.Speed,
						Range: &Range{Min: 0.5, Max: 2, Step: 0.1, Precision: 1}},
				},
			},
			// tts_providers describes one [tts_providers.<name>] record. The
			// provider dialog renders straight from this, so provider knowledge
			// stays here rather than being hardcoded per dialog in JS.
			//
			// Every rule keys on tts_providers.type -- the record's own type
			// select. Keying on tts.provider would compare against a record NAME
			// and never match, which would show every field for every type.
			{
				Name: "tts_providers",
				Fields: []FieldMeta{
					{Key: "type", Widget: WidgetSelect,
						Enum: enumOptions(tts.AvailableProviders()...)},
					{Key: "api_key", Widget: WidgetText, Secret: true},
					{Key: "model", Widget: WidgetText,
						Enum: []EnumOption{
							{Value: "speech-2.8-hd", Label: "MiniMax speech-2.8-hd", Providers: []string{"minimax", "minimax-cn"}},
							{Value: "s2-pro", Label: "Fish Audio s2-pro", Providers: []string{"fish-audio"}},
							{Value: "qwen-tts-realtime", Label: "Alibaba Cloud qwen-tts-realtime", Providers: []string{"alicloud"}},
							{Value: "qwen3-tts-flash-realtime", Label: "Alibaba Cloud qwen3-tts-flash-realtime", Providers: []string{"alicloud"}},
							{Value: "seed-tts-2.0", Label: "Volcengine seed-tts-2.0", Providers: []string{"volcengine"}},
							{Value: "google/gemini-3.1-flash-tts-preview", Label: "Google Gemini TTS", Providers: []string{"openrouter"}},
							{Value: "hexgrad/kokoro-82m", Label: "Hexgrad Kokoro", Providers: []string{"openrouter"}},
							{Value: "microsoft/mai-voice-2", Label: "Microsoft MAI Voice 2", Providers: []string{"openrouter"}},
						},
						PlaceholderWhen: []ConditionalPlaceholder{
							placeholderWhen("speech-2.8-hd", in("tts_providers.type", "minimax", "minimax-cn")),
							placeholderWhen("s2-pro", in("tts_providers.type", "fish-audio")),
							placeholderWhen("qwen-tts-realtime", in("tts_providers.type", "alicloud")),
							placeholderWhen("seed-tts-2.0", in("tts_providers.type", "volcengine")),
							placeholderWhen("google/gemini-3.1-flash-tts-preview", in("tts_providers.type", "openrouter")),
						},
						SelectWhen:  all(in("tts_providers.type", "openrouter")),
						VisibleWhen: all(in("tts_providers.type", "minimax", "minimax-cn", "fish-audio", "alicloud", "volcengine", "openrouter"))},
					{Key: "voice_id", Widget: WidgetText, Default: defaults.TTS.VoiceID,
						PlaceholderWhen: []ConditionalPlaceholder{
							placeholderWhen("Kore", eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "google/gemini-3.1-flash-tts-preview")),
							placeholderWhen("af_heart", eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "hexgrad/kokoro-82m")),
							placeholderWhen("en-US-AndrewMultilingualNeural", eq("tts_providers.type", "openrouter"), eq("tts_providers.model", "microsoft/mai-voice-2")),
							placeholderWhen("alloy", in("tts_providers.type", "openrouter")),
							placeholderWhen("male-qn-qingse", in("tts_providers.type", "minimax", "minimax-cn")),
							placeholderWhen("Cherry", in("tts_providers.type", "alicloud")),
							placeholderWhen("zh_female_vv_uranus_bigtts", in("tts_providers.type", "volcengine")),
							placeholderWhen("en-US-Neural2-C", in("tts_providers.type", "google-cloud")),
						},
						VisibleWhen: all(in("tts_providers.type", "minimax", "minimax-cn", "alicloud", "volcengine", "openrouter", "google-cloud"))},
					{Key: "reference_id", Widget: WidgetText,
						PlaceholderWhen: []ConditionalPlaceholder{
							placeholderWhen(tts.DefaultFishAudioReferenceID, in("tts_providers.type", "fish-audio")),
						},
						VisibleWhen: all(in("tts_providers.type", "fish-audio"))},
					{Key: "emotion", Widget: WidgetText, Default: defaults.TTS.Emotion,
						PlaceholderWhen: []ConditionalPlaceholder{
							placeholderWhen("happy", in("tts_providers.type", "minimax", "minimax-cn")),
						},
						VisibleWhen: all(in("tts_providers.type", "minimax", "minimax-cn", "volcengine"))},
				},
			},
			// [stt] keeps the provider reference and language. language is a user
			// preference that holds regardless of which provider transcribes, so
			// unlike the credential set it does not belong on a record.
			{
				Name: "stt",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Layout:  "wide",
						Enum:    enumOptions(sttProviderTypes()...),
						Default: defaults.STT.Provider},
					{Key: "language", Widget: WidgetSelect,
						Enum:    []EnumOption{{Value: "zh", Label: "中文"}, {Value: "en", Label: "English"}},
						Default: defaults.STT.Language},
				},
			},
			// stt_providers describes one [stt_providers.<name>] record. Same
			// contract as tts_providers: every rule keys on the record's own type.
			{
				Name: "stt_providers",
				Fields: []FieldMeta{
					{Key: "type", Widget: WidgetSelect,
						Enum: enumOptions(sttProviderTypes()...)},
					{Key: "api_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt_providers.type", "openai-whisper", "openrouter", "qwen-asr", "google-cloud"))},
					{Key: "model", Widget: WidgetSelect,
						Enum: []EnumOption{
							{Value: "openai/whisper-large-v3-turbo", Label: "OpenAI Whisper v3 Turbo"},
							{Value: "openai/whisper-large-v3", Label: "OpenAI Whisper v3"},
							{Value: "openai/whisper-1", Label: "OpenAI Whisper 1"},
							{Value: "openai/gpt-4o-transcribe", Label: "OpenAI GPT-4o Transcribe"},
							{Value: "openai/gpt-4o-mini-transcribe", Label: "OpenAI GPT-4o Mini Transcribe"},
							{Value: "microsoft/mai-transcribe-1.5", Label: "Microsoft MAI Transcribe"},
							{Value: "nvidia/parakeet-tdt-0.6b-v3", Label: "NVIDIA Parakeet"},
							{Value: "mistralai/voxtral-mini-transcribe", Label: "Mistral Voxtral Mini"},
							{Value: "qwen/qwen3-asr-flash-2026-02-10", Label: "Qwen3 ASR Flash"},
							{Value: "google/chirp-3", Label: "Google Chirp 3"},
						},
						Default:     defaults.STT.Model,
						VisibleWhen: all(in("stt_providers.type", "openrouter"))},
					{Key: "base_url", Widget: WidgetText,
						VisibleWhen: all(in("stt_providers.type", "openai-whisper"))},
					{Key: "app_id", Widget: WidgetText, Default: "",
						VisibleWhen: all(in("stt_providers.type", tencentSTTProviderNames...))},
					{Key: "secret_id", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt_providers.type", tencentSTTProviderNames...))},
					{Key: "secret_key", Widget: WidgetText, Secret: true,
						VisibleWhen: all(in("stt_providers.type", tencentSTTProviderNames...))},
					{Key: "region", Widget: WidgetText, Default: defaultTencentASRRegion,
						VisibleWhen: all(in("stt_providers.type", tencentSTTProviderNames...))},
					{Key: "engine_model_type", Widget: WidgetText,
						VisibleWhen: all(in("stt_providers.type", tencentSTTProviderNames...))},
				},
			},
			{
				Name: "audio",
				Fields: []FieldMeta{
					{Key: "socket", Widget: WidgetText, Default: defaults.Audio.Socket, Layout: "wide"},
					{Key: "sample_rate", Widget: WidgetNumber, Default: defaults.Audio.SampleRate},
					{Key: "channels", Widget: WidgetNumber, Default: defaults.Audio.Channels},
					{Key: "bit_width", Widget: WidgetNumber, Default: defaults.Audio.BitWidth},
					{Key: "backend", Widget: WidgetSelect,
						Enum:    enumOptions(AudioBackendAuto, AudioBackendAudioService, AudioBackendLocal),
						Default: defaults.Audio.Backend},
				},
			},
			{
				Name: "voice_model",
				Fields: []FieldMeta{
					{Key: "provider", Label: "Realtime Provider", Widget: WidgetSelect,
						Enum:        realtimeProviderEnumOptions(),
						Default:     defaults.VoiceModel.Provider,
						VisibleWhen: all(eq("agent.input_mode", "realtime"))},
				},
			},
			{
				Name: "voice_model_providers",
				Fields: []FieldMeta{
					{Key: "type", Label: "Realtime Provider Type", Widget: WidgetSelect,
						Enum:    realtimeProviderEnumOptions(),
						Default: defaults.VoiceModel.Provider},
					// OpenAI is absent on purpose: Speko serves that upstream over WebRTC
					// and every adapter here speaks WebSocket, so the option could only
					// fail after spending a mint request. Select the standalone "OpenAI
					// Realtime" provider type for a direct WebSocket session instead.
					{Key: "upstream_provider", Label: "Realtime Engine", Widget: WidgetSelect,
						Enum: []EnumOption{
							{Value: "google", Label: "Google Gemini Live"},
							{Value: "xai", Label: "xAI Grok Voice"},
						},
						Help:        "Required Speko S2S engine. Select Google or xAI and set its Model. Automatic routing is disabled because it may select an unsupported WebRTC route. For OpenAI Realtime, select it as the provider type instead. No separate engine API key is needed here.",
						VisibleWhen: all(eq("voice_model_providers.type", "speko"))},
					{Key: "agent_id", Label: "Speko Agent ID", Widget: WidgetText,
						Help: "Optional Speko agent ID.", Advanced: true,
						VisibleWhen: all(eq("voice_model_providers.type", "speko"))},
					{Key: "api_key", Label: "Realtime API Key", Widget: WidgetText, Secret: true, Layout: "wide",
						Help:        "Credential for the selected realtime provider. For Gemini Vertex, provide an OAuth access token. Use a literal value or $ENV_VAR.",
						Placeholder: "$REALTIME_API_KEY"},
					{Key: "auth_mode", Label: "Gemini Authentication", Widget: WidgetSelect,
						Enum:        []EnumOption{{Value: "api_key", Label: "Gemini API key"}, {Value: "vertex", Label: "Vertex OAuth"}},
						Default:     "api_key",
						VisibleWhen: all(eq("voice_model_providers.type", "gemini"))},
					{Key: "project_id", Label: "Google Cloud Project ID", Widget: WidgetText, Layout: "wide",
						VisibleWhen: all(eq("voice_model_providers.type", "gemini"), eq("voice_model_providers.auth_mode", "vertex"))},
					{Key: "location", Label: "Vertex Location", Widget: WidgetText,
						Placeholder: "us-central1",
						VisibleWhen: all(eq("voice_model_providers.type", "gemini"), eq("voice_model_providers.auth_mode", "vertex"))},
					{Key: "model", Label: "Realtime Model", Widget: WidgetText, Default: defaults.VoiceModel.Model, Layout: "wide",
						Help:            "Realtime model ID (provider-specific).",
						PlaceholderWhen: realtimeProviderPlaceholders("model")},
					{Key: "workspace_id", Label: "DashScope Workspace ID", Widget: WidgetText,
						Advanced:    true,
						VisibleWhen: all(eq("voice_model_providers.type", "qwen"))},
					{Key: "endpoint", Label: "WebSocket Endpoint", Widget: WidgetText, Layout: "wide",
						Help:        "Optional provider WebSocket endpoint override. Leave empty to use the provider default.",
						Advanced:    true,
						VisibleWhen: all(in("voice_model_providers.type", "qwen", "openai", "gemini", "xai"))},
					{Key: "realtime_protocol", Label: "Realtime Protocol", Widget: WidgetSelect,
						Enum: []EnumOption{
							{Value: "", Label: "OpenAI GA (default)"},
							{Value: "legacy", Label: "Legacy / Beta-compatible"},
						},
						Help:        "Use Legacy / Beta-compatible for OpenAI-compatible gateways that reject session.output_modalities or session.audio, such as MixRoute.",
						Advanced:    true,
						VisibleWhen: all(eq("voice_model_providers.type", "openai"))},
					{Key: "base_url", Label: "Provider Base URL", Widget: WidgetText, Layout: "wide",
						Help: "Optional provider API base URL; useful for Speko-compatible deployments or tests.", Advanced: true,
						VisibleWhen: all(eq("voice_model_providers.type", "speko"))},
					{Key: "region", Label: "Region", Widget: WidgetSelect,
						Enum: []EnumOption{
							{Value: "", Label: "Automatic"},
							{Value: "cn-beijing", Label: "China (Beijing)"},
							{Value: "ap-southeast-1", Label: "Singapore"},
						},
						Default: defaults.VoiceModel.Region, Advanced: true,
						VisibleWhen: all(eq("voice_model_providers.type", "qwen"))},
					{Key: "voice", Label: "Voice", Widget: WidgetText, Default: defaults.VoiceModel.Voice,
						Help:            "Realtime system voice name or voice-clone ID (provider-specific).",
						PlaceholderWhen: realtimeProviderPlaceholders("voice")},
				},
			},
			{
				Name: "audio_archive",
				Fields: []FieldMeta{
					{Key: "enabled", Widget: WidgetBoolean, Default: defaults.AudioArchive.Enabled,
						Help:        "After enabling, save STT voice recording WAV for Web UI playback; Automatically delete old files when exceeding quantity or capacity limit.",
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "storage_path", Widget: WidgetText, Default: defaults.AudioArchive.StoragePathOrDefault(),
						Layout:      "wide",
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
					{Key: "max_files", Widget: WidgetNumber, Default: defaults.AudioArchive.MaxFilesOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
					{Key: "max_size_mb", Widget: WidgetNumber, Default: defaults.AudioArchive.MaxSizeMBOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("audio_archive.enabled"))},
				},
			},
			{
				Name: "frame_service",
				Fields: []FieldMeta{
					{Key: "keep_streamon", Label: "Keep STREAMON", Widget: WidgetBoolean,
						Default: defaults.FrameService.KeepStreamOn,
						Help:    "Keep the RK628 CSI capture stream enabled between screenshots. This reduces screenshot latency but increases idle power consumption."},
				},
			},
			{
				Name: "quick_capture",
				Fields: []FieldMeta{
					{Key: "enabled", Label: "Enabled", Widget: WidgetBoolean,
						Default: defaults.QuickCapture.EnabledOrDefault(),
						Help:    "Enable GPIO-triggered Screen Memory capture. Legacy GPIO32/GPIO33 wakeup remains independent."},
					{Key: "gpio_pin", Label: "GPIO Pin", Widget: WidgetNumber,
						Default: defaults.QuickCapture.GPIOPin,
						Help:    "Falling-edge trigger pin. Supported values are 0 (disabled) and GPIO3 (physical pin 38 on Luckfox Pico Zero). GPIO32 and GPIO33 remain reserved for legacy wakeup."},
					{Key: "screen_memory_ttl", Label: "Screen Memory TTL", Widget: WidgetText,
						Default: defaults.QuickCapture.ScreenMemoryTTLOrDefault(),
						Help:    "Retention period for captured Screen Memory entries, such as 90d, or forever."},
				},
			},
			{
				Name: "storage",
				Fields: []FieldMeta{
					{Key: "monitor_enabled", Widget: WidgetBoolean, Default: defaults.Storage.MonitorEnabled},
					{Key: "mount_point", Widget: WidgetText, Default: defaults.Storage.MountPointOrDefault()},
					{Key: "device", Widget: WidgetText, Default: defaults.Storage.DeviceOrDefault()},
					{Key: "min_card_free_mb", Widget: WidgetNumber, Default: defaults.Storage.MinCardFreeMBOrDefault()},
					{Key: "migrate_start_free_pct", Widget: WidgetNumber, Default: defaults.Storage.MigrateStartFreePct},
					{Key: "migrate_stop_free_pct", Widget: WidgetNumber, Default: defaults.Storage.MigrateStopFreePct},
				},
			},
			{
				Name: "log",
				Fields: []FieldMeta{
					{Key: "llm_http_retention_days", Widget: WidgetNumber,
						Default: defaults.Log.LLMHTTPRetentionDaysOrDefault()},
				},
			},
			{
				Name: "ota",
				Fields: []FieldMeta{
					{Key: "github_proxy_url", Label: "GitHub Proxy URL", Widget: WidgetText, Default: "",
						Help:        "Optional proxy to accelerate GitHub downloads (e.g., https://gh-proxy.com/ or https://ghfast.top/)",
						Placeholder: "Leave empty to disable",
						Layout:      "wide"},
				},
			},
			{
				Name: "hid",
				Fields: []FieldMeta{
					{Key: "keyboard_layout", Widget: WidgetSelect,
						Help:    "How the phone interprets the USB keyboard. Keep qwerty unless typed text comes out transposed; then switch the phone input language to match, save, and reboot the board.",
						Enum:    keyboardLayoutEnumOptions(),
						Default: defaults.HID.KeyboardLayoutOrDefault()},
					{Key: "input_backend", Widget: WidgetSelect,
						Enum:    enumOptions("hid", "adb"),
						Default: defaults.HID.InputBackend},
					{Key: "keyboard_device", Widget: WidgetText, Default: defaults.HID.KeyboardDevice, Layout: "wide"},
					{Key: "mouse_device", Widget: WidgetText, Default: defaults.HID.MouseDevice, Layout: "wide"},
					{Key: "android_keyboard_device", Widget: WidgetText, Default: defaults.HID.AndroidKeyboardDevice, Layout: "wide"},
					{Key: "frame_socket", Widget: WidgetText, Default: defaults.HID.FrameSocket, Layout: "wide"},
				},
			},
			{
				Name: "search",
				Fields: []FieldMeta{
					{Key: "provider", Widget: WidgetSelect,
						Enum:    enumOptions(searchProviderDuckDuckGo, searchProviderBrave, searchProviderTavily),
						Default: defaults.Search.ProviderOrDefault()},
					{Key: "api_key", Widget: WidgetText, Secret: true, Layout: "wide",
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
					{Key: "base_url", Widget: WidgetText, Placeholder: "http://langfuse.example.com:3000", Layout: "wide",
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "public_key", Widget: WidgetText, Secret: true, Layout: "wide",
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "secret_key", Widget: WidgetText, Secret: true, Layout: "wide",
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "upload_screenshots", Widget: WidgetBoolean,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "upload_timeout_sec", Widget: WidgetNumber,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "max_retry", Widget: WidgetNumber,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "environment", Widget: WidgetText,
						VisibleWhen: all(truthy("telemetry.enabled"))},
					{Key: "tags", Widget: WidgetList, Layout: "wide",
						VisibleWhen: all(truthy("telemetry.enabled"))},
				},
			},
			{
				Name: "live_activity",
				Fields: []FieldMeta{
					{Key: "enabled", Widget: WidgetBoolean, Default: true},
				},
			},
			{
				// The "agent" UI section maps to the top-level Config fields.
				Name: "agent",
				Fields: []FieldMeta{
					{Key: "locale", Widget: WidgetSelect,
						Enum:    enumOptions(localeSimplifiedChinese, localeEnglishUS),
						Default: defaults.LocaleOrDefault()},
					{Key: "input_mode", Widget: WidgetSelect,
						Enum:    enumOptions("text", "stt", "realtime"),
						Default: defaults.InputMode},
					{Key: "vad_backend", Widget: WidgetSelect,
						Enum:        enumOptions(defaultVADBackend, "cpu"),
						Default:     defaults.VADBackend,
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "vad_model_path", Widget: WidgetText,
						Default:     defaults.VADModelPath,
						Layout:      "wide",
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "vad_helper_path", Widget: WidgetText,
						Default:     defaults.VADHelperPath,
						Layout:      "wide",
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "vad_speech_threshold", Widget: WidgetSelect,
						Default:     defaults.VADSpeechThreshold,
						Range:       &Range{Min: 0, Max: 1, Step: 0.05, Precision: 2},
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "silence_ms", Widget: WidgetNumber, Default: defaults.SilenceMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "min_speech_ms", Widget: WidgetNumber, Default: defaults.MinSpeechMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "voice_followup_enabled", Widget: WidgetBoolean, Default: defaults.VoiceFollowupEnabledOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "voice_followup_timeout_ms", Widget: WidgetNumber, Default: defaults.VoiceFollowupTimeoutMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_first_turn_timeout_ms", Widget: WidgetNumber, Default: defaults.VoiceFirstTurnTimeoutMs,
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_max_turns", Widget: WidgetNumber, Default: defaults.VoiceMaxTurns,
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_interrupt_on_wakeup", Widget: WidgetBoolean, Default: defaults.VoiceInterruptOnWakeupOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"), truthy("agent.voice_followup_enabled"))},
					{Key: "voice_streaming_tts_enabled", Widget: WidgetBoolean, Default: defaults.VoiceStreamingTTSEnabledOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "voice_tool_call_speech", Widget: WidgetBoolean, Default: defaults.VoiceToolCallSpeechOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "voice_progress_speech_enabled", Widget: WidgetBoolean, Default: defaults.VoiceProgressSpeechEnabledOrDefault(),
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "voice_max_response_tokens", Widget: WidgetNumber, Default: defaults.VoiceMaxResponseTokens,
						VisibleWhen: all(eq("agent.input_mode", "stt"))},
					{Key: "max_iterations", Widget: WidgetNumber, Default: defaults.MaxIterations},
					{Key: "context_prune_threshold", Label: "Historical prune threshold (fraction)", Widget: WidgetNumber,
						Help:    "Fraction of the usable model input budget that triggers cleanup of expired state and historical tool results, cleaning down to 6/7 of the trigger. Must be 0 or greater than 0 and less than 1; 0 uses 0.5. Capped at context_compaction_threshold so this cheap pass runs before the conversation summary.",
						Default: defaults.ContextPruneThreshold, Placeholder: "0 = automatic (0.5)"},
					{Key: "screenshot_keep_n", Widget: WidgetNumber, Default: defaults.ScreenshotKeepN},
					{Key: "screenshot_prune_interval", Widget: WidgetNumber, Default: defaults.ScreenshotPruneInterval},
					{Key: "screen_stable_timeout_ms", Widget: WidgetNumber, Default: defaults.ScreenStableTimeoutMs},
					{Key: "screen_stable_ms", Widget: WidgetNumber, Default: defaults.ScreenStableMs},
					{Key: "screen_stable_diff_threshold", Widget: WidgetNumber, Default: defaults.ScreenStableDiffThreshold},
					{Key: "custom_instruction", Widget: WidgetTextarea, Layout: "wide"},
					{Key: "additional_prompt", Widget: WidgetTextarea, Layout: "wide"},
				},
			},
		},
	}
	return withDisplayDefaults(metadata)
}
