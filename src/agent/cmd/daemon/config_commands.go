package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"aiden-agent/internal/agent"
)

// ValidationError represents a single validation error with field path and message
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationResult is the output format for config-check command
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors"`
}

// webConfigDTO mirrors the JSON produced by config_web.cpp's config_to_json().
// It is the single definition of the config_web <-> agent wire contract: keys
// are snake_case, agent-level settings live under "agent", and search reports
// only whether a key is present (has_api_key) rather than echoing the key.
//
// Keep this struct in lockstep with config_to_json(); the round-trip is covered
// by TestConfigCheck_WireFormatContract.
type webConfigDTO struct {
	Model        modelDTO        `json:"model"`
	ModelText    modelDTO        `json:"model_text"`
	TTS          ttsDTO          `json:"tts"`
	STT          sttDTO          `json:"stt"`
	Audio        audioDTO        `json:"audio"`
	AudioArchive audioArchiveDTO `json:"audio_archive"`
	Benchmark    benchmarkDTO    `json:"benchmark"`
	Log          logDTO          `json:"log"`
	HID          hidDTO          `json:"hid"`
	Search       searchDTO       `json:"search"`
	Telemetry    telemetryDTO    `json:"telemetry"`
	LiveActivity liveActivityDTO `json:"live_activity"`
	Agent        agentDTO        `json:"agent"`
}

type modelDTO struct {
	Provider             string  `json:"provider"`
	APIKey               string  `json:"api_key"`
	Model                string  `json:"model"`
	BaseURL              string  `json:"base_url"`
	TokenEnv             string  `json:"token_env"`
	Temperature          float64 `json:"temperature"`
	MaxResponseTokens    int     `json:"max_response_tokens"`
	ContextWindow        int     `json:"context_window"`
	ModelMaxOutputTokens int     `json:"model_max_output_tokens"`
}

type ttsDTO struct {
	Provider string  `json:"provider"`
	APIKey   string  `json:"api_key"`
	Model    string  `json:"model"`
	VoiceID  string  `json:"voice_id"`
	Emotion  string  `json:"emotion"`
	Speed    float64 `json:"speed"`
}

type sttDTO struct {
	Provider        string `json:"provider"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url"`
	SecretID        string `json:"secret_id"`
	SecretKey       string `json:"secret_key"`
	Region          string `json:"region"`
	EngineModelType string `json:"engine_model_type"`
}

type audioDTO struct {
	Socket     string `json:"socket"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	BitWidth   int    `json:"bit_width"`
}

type audioArchiveDTO struct {
	Enabled     bool   `json:"enabled"`
	MaxFiles    int    `json:"max_files"`
	MaxSizeMB   int    `json:"max_size_mb"`
	StoragePath string `json:"storage_path"`
}

type benchmarkDTO struct {
	JudgeModel   string `json:"judge_model"`
	APIKey       string `json:"api_key"`
	BenchmarkDir string `json:"benchmark_dir"`
}

type logDTO struct {
	LLMHTTPRetentionDays int `json:"llm_http_retention_days"`
}

type hidDTO struct {
	KeyboardDevice string `json:"keyboard_device"`
	MouseDevice    string `json:"mouse_device"`
	FrameSocket    string `json:"frame_socket"`
	PointerMode    string `json:"pointer_mode"`
}

type searchDTO struct {
	APIKey    string `json:"api_key,omitempty"`
	Provider  string `json:"provider"`
	HasAPIKey bool   `json:"has_api_key"`
}

type telemetryDTO struct {
	Enabled           bool     `json:"enabled"`
	Provider          string   `json:"provider"`
	BaseURL           string   `json:"base_url"`
	PublicKey         string   `json:"public_key"`
	SecretKey         string   `json:"secret_key"`
	UploadScreenshots bool     `json:"upload_screenshots"`
	UploadTimeoutSec  int      `json:"upload_timeout_sec"`
	MaxRetry          int      `json:"max_retry"`
	Tags              []string `json:"tags"`
	Environment       string   `json:"environment"`
}

type liveActivityDTO struct {
	Enabled          *bool  `json:"enabled"`
	RelayURL         string `json:"relay_url"`
	RelayAPIKey      string `json:"relay_api_key,omitempty"`
	HasRelayAPIKey   bool   `json:"has_relay_api_key"`
	BoardID          string `json:"board_id"`
	PhoneID          string `json:"phone_id"`
	BundleID         string `json:"bundle_id"`
	Topic            string `json:"topic"`
	Environment      string `json:"environment"`
	TeamID           string `json:"team_id"`
	KeyID            string `json:"key_id"`
	PrivateKeyPath   string `json:"private_key_path"`
	PrivateKeyPEM    string `json:"private_key_pem,omitempty"`
	HasPrivateKeyPEM bool   `json:"has_private_key_pem"`
	TimeoutSec       int    `json:"timeout_sec"`
}

type agentDTO struct {
	CustomInstruction          string  `json:"custom_instruction"`
	AdditionalPrompt           string  `json:"additional_prompt"`
	InputMode                  string  `json:"input_mode"`
	TriggerMode                string  `json:"trigger_mode"`
	VADBackend                 string  `json:"vad_backend"`
	VADModelPath               string  `json:"vad_model_path"`
	VADHelperPath              string  `json:"vad_helper_path"`
	VADSpeechThreshold         float64 `json:"vad_speech_threshold"`
	SilenceMs                  int     `json:"silence_ms"`
	MinSpeechMs                int     `json:"min_speech_ms"`
	VoiceFollowupEnabled       bool    `json:"voice_followup_enabled"`
	VoiceFollowupTimeoutMs     int     `json:"voice_followup_timeout_ms"`
	VoiceFirstTurnTimeoutMs    int     `json:"voice_first_turn_timeout_ms"`
	VoiceMaxTurns              int     `json:"voice_max_turns"`
	VoiceInterruptOnWakeup     bool    `json:"voice_interrupt_on_wakeup"`
	VoiceStreamingTTSEnabled   bool    `json:"voice_streaming_tts_enabled"`
	VoiceToolCallSpeech        bool    `json:"voice_tool_call_speech"`
	VoiceProgressSpeechEnabled bool    `json:"voice_progress_speech_enabled"`
	VoiceMaxResponseTokens     int     `json:"voice_max_response_tokens"`
	MaxIterations              int     `json:"max_iterations"`
	ForceSimpleLoop            bool    `json:"force_simple_loop"`
	ScreenshotKeepN            int     `json:"screenshot_keep_n"`
	ScreenshotPruneInterval    int     `json:"screenshot_prune_interval"`
	ScreenStableTimeoutMs      int     `json:"screen_stable_timeout_ms"`
	ScreenStableMs             int     `json:"screen_stable_ms"`
	ScreenStableDiffThreshold  float64 `json:"screen_stable_diff_threshold"`
}

// hasAPIKeyPlaceholder is substituted for a real key when the wire payload
// reports has_api_key=true. config_web never echoes the stored secret, so this
// lets Validate()'s "key is required" checks pass when a key is in fact saved,
// without the validator ever seeing the secret value.
const hasAPIKeyPlaceholder = "***"

// toAgentConfig maps the wire DTO onto agent.Config so the canonical
// Config.Validate() can run against it.
func (d webConfigDTO) toAgentConfig() agent.Config {
	searchKey := ""
	if strings.TrimSpace(d.Search.APIKey) != "" {
		searchKey = d.Search.APIKey
	} else if d.Search.HasAPIKey {
		searchKey = hasAPIKeyPlaceholder
	}
	liveActivityRelayAPIKey := d.LiveActivity.RelayAPIKey
	if strings.TrimSpace(liveActivityRelayAPIKey) == "" && d.LiveActivity.HasRelayAPIKey {
		liveActivityRelayAPIKey = hasAPIKeyPlaceholder
	}
	liveActivityPrivateKeyPEM := d.LiveActivity.PrivateKeyPEM
	if strings.TrimSpace(liveActivityPrivateKeyPEM) == "" && d.LiveActivity.HasPrivateKeyPEM {
		liveActivityPrivateKeyPEM = hasAPIKeyPlaceholder
	}

	return agent.Config{
		Model: agent.ModelConfig{
			Provider:             d.Model.Provider,
			APIKey:               d.Model.APIKey,
			Model:                d.Model.Model,
			BaseURL:              d.Model.BaseURL,
			TokenEnv:             d.Model.TokenEnv,
			Temperature:          d.Model.Temperature,
			MaxResponseTokens:    d.Model.MaxResponseTokens,
			ContextWindow:        d.Model.ContextWindow,
			ModelMaxOutputTokens: d.Model.ModelMaxOutputTokens,
		},
		ModelText: agent.ModelConfig{
			Provider:             d.ModelText.Provider,
			APIKey:               d.ModelText.APIKey,
			Model:                d.ModelText.Model,
			BaseURL:              d.ModelText.BaseURL,
			TokenEnv:             d.ModelText.TokenEnv,
			Temperature:          d.ModelText.Temperature,
			MaxResponseTokens:    d.ModelText.MaxResponseTokens,
			ContextWindow:        d.ModelText.ContextWindow,
			ModelMaxOutputTokens: d.ModelText.ModelMaxOutputTokens,
		},
		TTS: agent.TTSConfig{
			Provider: d.TTS.Provider,
			APIKey:   d.TTS.APIKey,
			Model:    d.TTS.Model,
			VoiceID:  d.TTS.VoiceID,
			Emotion:  d.TTS.Emotion,
			Speed:    d.TTS.Speed,
		},
		STT: agent.STTConfig{
			Provider:        d.STT.Provider,
			APIKey:          d.STT.APIKey,
			Model:           d.STT.Model,
			BaseURL:         d.STT.BaseURL,
			SecretID:        d.STT.SecretID,
			SecretKey:       d.STT.SecretKey,
			Region:          d.STT.Region,
			EngineModelType: d.STT.EngineModelType,
		},
		Audio: agent.AudioConfig{
			Socket:     d.Audio.Socket,
			SampleRate: d.Audio.SampleRate,
			Channels:   d.Audio.Channels,
			BitWidth:   d.Audio.BitWidth,
		},
		AudioArchive: agent.AudioArchiveConfig{
			Enabled:     d.AudioArchive.Enabled,
			MaxFiles:    d.AudioArchive.MaxFiles,
			MaxSizeMB:   d.AudioArchive.MaxSizeMB,
			StoragePath: d.AudioArchive.StoragePath,
		},
		Benchmark: agent.BenchmarkConfig{
			JudgeModel: d.Benchmark.JudgeModel,
			APIKey:     d.Benchmark.APIKey,
			Dir:        d.Benchmark.BenchmarkDir,
		},
		Log: agent.LogConfig{
			LLMHTTPRetentionDays: d.Log.LLMHTTPRetentionDays,
		},
		HID: agent.HIDConfig{
			KeyboardDevice: d.HID.KeyboardDevice,
			MouseDevice:    d.HID.MouseDevice,
			FrameSocket:    d.HID.FrameSocket,
			PointerMode:    d.HID.PointerMode,
		},
		Search: agent.SearchConfig{
			Provider: d.Search.Provider,
			APIKey:   searchKey,
		},
		Telemetry: agent.TelemetryConfig{
			Enabled:           boolPtr(d.Telemetry.Enabled),
			Provider:          d.Telemetry.Provider,
			BaseURL:           d.Telemetry.BaseURL,
			PublicKey:         d.Telemetry.PublicKey,
			SecretKey:         d.Telemetry.SecretKey,
			UploadScreenshots: boolPtr(d.Telemetry.UploadScreenshots),
			UploadTimeoutSec:  d.Telemetry.UploadTimeoutSec,
			MaxRetry:          d.Telemetry.MaxRetry,
			Tags:              d.Telemetry.Tags,
			Environment:       d.Telemetry.Environment,
		},
		LiveActivity: agent.LiveActivityConfig{
			Enabled:        d.LiveActivity.Enabled,
			RelayURL:       d.LiveActivity.RelayURL,
			RelayAPIKey:    liveActivityRelayAPIKey,
			BoardID:        d.LiveActivity.BoardID,
			PhoneID:        d.LiveActivity.PhoneID,
			BundleID:       d.LiveActivity.BundleID,
			Topic:          d.LiveActivity.Topic,
			Environment:    d.LiveActivity.Environment,
			TeamID:         d.LiveActivity.TeamID,
			KeyID:          d.LiveActivity.KeyID,
			PrivateKeyPath: d.LiveActivity.PrivateKeyPath,
			PrivateKeyPEM:  liveActivityPrivateKeyPEM,
			TimeoutSec:     d.LiveActivity.TimeoutSec,
		},
		Instruction:                d.Agent.CustomInstruction,
		AdditionalPrompt:           d.Agent.AdditionalPrompt,
		InputMode:                  d.Agent.InputMode,
		TriggerMode:                d.Agent.TriggerMode,
		VADBackend:                 d.Agent.VADBackend,
		VADModelPath:               d.Agent.VADModelPath,
		VADHelperPath:              d.Agent.VADHelperPath,
		VADSpeechThreshold:         d.Agent.VADSpeechThreshold,
		SilenceMs:                  d.Agent.SilenceMs,
		MinSpeechMs:                d.Agent.MinSpeechMs,
		VoiceFollowupEnabled:       boolPtr(d.Agent.VoiceFollowupEnabled),
		VoiceFollowupTimeoutMs:     d.Agent.VoiceFollowupTimeoutMs,
		VoiceFirstTurnTimeoutMs:    d.Agent.VoiceFirstTurnTimeoutMs,
		VoiceMaxTurns:              d.Agent.VoiceMaxTurns,
		VoiceInterruptOnWakeup:     boolPtr(d.Agent.VoiceInterruptOnWakeup),
		VoiceStreamingTTSEnabled:   boolPtr(d.Agent.VoiceStreamingTTSEnabled),
		VoiceToolCallSpeech:        boolPtr(d.Agent.VoiceToolCallSpeech),
		VoiceProgressSpeechEnabled: boolPtr(d.Agent.VoiceProgressSpeechEnabled),
		VoiceMaxResponseTokens:     d.Agent.VoiceMaxResponseTokens,
		MaxIterations:              d.Agent.MaxIterations,
		ForceSimpleLoop:            d.Agent.ForceSimpleLoop,
		ScreenshotKeepN:            d.Agent.ScreenshotKeepN,
		ScreenshotPruneInterval:    d.Agent.ScreenshotPruneInterval,
		ScreenStableTimeoutMs:      d.Agent.ScreenStableTimeoutMs,
		ScreenStableMs:             d.Agent.ScreenStableMs,
		ScreenStableDiffThreshold:  d.Agent.ScreenStableDiffThreshold,
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func webConfigDTOFromAgentConfig(cfg agent.Config) webConfigDTO {
	audioArchive := cfg.AudioArchive

	return webConfigDTO{
		Model: modelDTO{
			Provider:             cfg.Model.Provider,
			APIKey:               cfg.Model.APIKey,
			Model:                cfg.Model.Model,
			BaseURL:              cfg.Model.BaseURL,
			TokenEnv:             cfg.Model.TokenEnv,
			Temperature:          cfg.Model.Temperature,
			MaxResponseTokens:    cfg.Model.MaxResponseTokens,
			ContextWindow:        cfg.Model.ContextWindow,
			ModelMaxOutputTokens: cfg.Model.ModelMaxOutputTokens,
		},
		ModelText: modelDTO{
			Provider:             cfg.ModelText.Provider,
			APIKey:               cfg.ModelText.APIKey,
			Model:                cfg.ModelText.Model,
			BaseURL:              cfg.ModelText.BaseURL,
			TokenEnv:             cfg.ModelText.TokenEnv,
			Temperature:          cfg.ModelText.Temperature,
			MaxResponseTokens:    cfg.ModelText.MaxResponseTokens,
			ContextWindow:        cfg.ModelText.ContextWindow,
			ModelMaxOutputTokens: cfg.ModelText.ModelMaxOutputTokens,
		},
		TTS: ttsDTO{
			Provider: cfg.TTS.Provider,
			APIKey:   cfg.TTS.APIKey,
			Model:    cfg.TTS.Model,
			VoiceID:  cfg.TTS.VoiceID,
			Emotion:  cfg.TTS.Emotion,
			Speed:    cfg.TTS.Speed,
		},
		STT: sttDTO{
			Provider:        cfg.STT.Provider,
			APIKey:          cfg.STT.APIKey,
			Model:           cfg.STT.Model,
			BaseURL:         cfg.STT.BaseURL,
			SecretID:        cfg.STT.SecretID,
			SecretKey:       cfg.STT.SecretKey,
			Region:          cfg.STT.Region,
			EngineModelType: cfg.STT.EngineModelType,
		},
		Audio: audioDTO{
			Socket:     cfg.Audio.SocketOrDefault(),
			SampleRate: cfg.Audio.SampleRateOrDefault(),
			Channels:   cfg.Audio.ChannelsOrDefault(),
			BitWidth:   cfg.Audio.BitWidthOrDefault(),
		},
		AudioArchive: audioArchiveDTO{
			Enabled:     audioArchive.Enabled,
			MaxFiles:    audioArchive.MaxFilesOrDefault(),
			MaxSizeMB:   audioArchive.MaxSizeMBOrDefault(),
			StoragePath: audioArchive.StoragePathOrDefault(),
		},
		Benchmark: benchmarkDTO{
			JudgeModel:   cfg.Benchmark.JudgeModel,
			APIKey:       cfg.Benchmark.APIKey,
			BenchmarkDir: cfg.Benchmark.Dir,
		},
		Log: logDTO{
			LLMHTTPRetentionDays: cfg.Log.LLMHTTPRetentionDaysOrDefault(),
		},
		HID: hidDTO{
			KeyboardDevice: cfg.HID.KeyboardDeviceOrDefault(),
			MouseDevice:    cfg.HID.MouseDeviceOrDefault(),
			FrameSocket:    cfg.HID.FrameSocketOrDefault(),
			PointerMode:    cfg.HID.PointerModeOrDefault(),
		},
		Search: searchDTO{
			Provider:  cfg.Search.ProviderOrDefault(),
			HasAPIKey: strings.TrimSpace(cfg.Search.APIKey) != "",
		},
		Telemetry: telemetryDTO{
			Enabled:           cfg.Telemetry.EnabledOrDefault(),
			Provider:          cfg.Telemetry.ProviderOrDefault(),
			BaseURL:           cfg.Telemetry.BaseURL,
			PublicKey:         cfg.Telemetry.PublicKey,
			SecretKey:         cfg.Telemetry.SecretKey,
			UploadScreenshots: cfg.Telemetry.UploadScreenshotsOrDefault(),
			UploadTimeoutSec:  int(cfg.Telemetry.UploadTimeoutOrDefault().Seconds()),
			MaxRetry:          cfg.Telemetry.MaxRetryOrDefault(),
			Tags:              cfg.Telemetry.Tags,
			Environment:       cfg.Telemetry.EnvironmentOrDefault(),
		},
		LiveActivity: liveActivityDTO{
			Enabled:          boolPtr(cfg.LiveActivity.EnabledOrDefault()),
			RelayURL:         cfg.LiveActivity.RelayURL,
			HasRelayAPIKey:   strings.TrimSpace(cfg.LiveActivity.RelayAPIKey) != "",
			BoardID:          cfg.LiveActivity.BoardIDOrDefault(),
			PhoneID:          cfg.LiveActivity.PhoneID,
			BundleID:         cfg.LiveActivity.BundleID,
			Topic:            cfg.LiveActivity.Topic,
			Environment:      cfg.LiveActivity.EnvironmentOrDefault(),
			TeamID:           cfg.LiveActivity.TeamID,
			KeyID:            cfg.LiveActivity.KeyID,
			PrivateKeyPath:   cfg.LiveActivity.PrivateKeyPath,
			HasPrivateKeyPEM: strings.TrimSpace(cfg.LiveActivity.PrivateKeyPEM) != "",
			TimeoutSec:       int(cfg.LiveActivity.TimeoutOrDefault().Seconds()),
		},
		Agent: agentDTO{
			CustomInstruction:          customInstructionValue(cfg.Instruction),
			AdditionalPrompt:           cfg.AdditionalPrompt,
			InputMode:                  cfg.InputModeOrDefault(),
			TriggerMode:                cfg.TriggerModeOrDefault(),
			VADBackend:                 cfg.VADBackendOrDefault(),
			VADModelPath:               cfg.VADModelPath,
			VADHelperPath:              cfg.VADHelperPath,
			VADSpeechThreshold:         cfg.VADSpeechThreshold,
			SilenceMs:                  cfg.SilenceMs,
			MinSpeechMs:                cfg.MinSpeechMs,
			VoiceFollowupEnabled:       cfg.VoiceFollowupEnabledOrDefault(),
			VoiceFollowupTimeoutMs:     int(cfg.VoiceFollowupTimeoutOrDefault().Milliseconds()),
			VoiceFirstTurnTimeoutMs:    int(cfg.VoiceFirstTurnTimeoutOrDefault().Milliseconds()),
			VoiceMaxTurns:              cfg.VoiceMaxTurns,
			VoiceInterruptOnWakeup:     cfg.VoiceInterruptOnWakeupOrDefault(),
			VoiceStreamingTTSEnabled:   cfg.VoiceStreamingTTSEnabledOrDefault(),
			VoiceToolCallSpeech:        cfg.VoiceToolCallSpeechOrDefault(),
			VoiceProgressSpeechEnabled: cfg.VoiceProgressSpeechEnabledOrDefault(),
			VoiceMaxResponseTokens:     cfg.VoiceMaxResponseTokensOrDefault(),
			MaxIterations:              cfg.MaxIterations,
			ForceSimpleLoop:            cfg.ForceSimpleLoop,
			ScreenshotKeepN:            cfg.ScreenshotKeepN,
			ScreenshotPruneInterval:    cfg.ScreenshotPruneInterval,
			ScreenStableTimeoutMs:      cfg.ScreenStableTimeoutMs,
			ScreenStableMs:             cfg.ScreenStableMs,
			ScreenStableDiffThreshold:  cfg.ScreenStableDiffThreshold,
		},
	}
}

func customInstructionValue(instruction string) string {
	if strings.TrimSpace(instruction) == agent.DefaultConfig().Instruction {
		return ""
	}
	return instruction
}

// runConfigCheck implements the `agent config-check` subcommand
// It reads JSON config from stdin, validates it, and outputs structured JSON result
func runConfigCheck(args []string) int {
	fs := flag.NewFlagSet("config-check", flag.ExitOnError)
	formatFlag := fs.String("format", "json", "output format (only json supported)")
	stdinFlag := fs.Bool("stdin", false, "read config from stdin")

	if err := fs.Parse(args); err != nil {
		writeConfigCheckError("failed to parse flags: " + err.Error())
		return 1
	}

	if *formatFlag != "json" {
		writeConfigCheckError("only --format=json is supported")
		return 1
	}

	if !*stdinFlag {
		writeConfigCheckError("--stdin flag is required")
		return 1
	}

	result, decodeErr := checkConfig(os.Stdin)
	if decodeErr != nil {
		writeConfigCheckError("invalid JSON input: " + decodeErr.Error())
		return 1
	}

	// Output result as JSON
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode result: %v\n", err)
		return 1
	}

	if result.Valid {
		return 0
	}
	return 1
}

// checkConfig reads a config_web wire-format payload from r, maps it onto
// agent.Config, and runs the canonical Config.Validate(). It returns the
// structured result, or a non-nil error only when the input is not decodable
// JSON. Splitting this out of runConfigCheck keeps the full
// decode -> map -> validate pipeline testable without driving os.Stdin/Stdout.
func checkConfig(r io.Reader) (ValidationResult, error) {
	// The payload is the wire format produced by config_web.cpp's
	// config_to_json(): snake_case keys, agent-level settings nested under an
	// "agent" object, and search exposing only a "has_api_key" boolean instead
	// of the raw key. agent.Config carries only TOML tags, so decoding straight
	// into it silently drops every snake_case / nested field and validates a
	// near-empty config. Decode into a DTO that mirrors the wire format, then
	// map it onto agent.Config before validating.
	var dto webConfigDTO
	if err := json.NewDecoder(r).Decode(&dto); err != nil {
		return ValidationResult{}, err
	}
	cfg := dto.toAgentConfig()

	result := ValidationResult{
		Valid:  true,
		Errors: []ValidationError{},
	}
	if err := cfg.Validate(); err != nil {
		result.Valid = false
		result.Errors = parseValidationErrors(err)
	}
	return result, nil
}

// runConfigMeta implements the `agent config-meta` subcommand. It outputs the
// config field metadata (widget, enum, range, default, secret, visibility
// rules) as JSON for the config web UI to consume.
func runConfigMeta(args []string) int {
	fs := flag.NewFlagSet("config-meta", flag.ExitOnError)
	formatFlag := fs.String("format", "json", "output format (only json supported)")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		return 1
	}

	if *formatFlag != "json" {
		fmt.Fprintln(os.Stderr, "only --format=json is supported")
		return 1
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(agent.ConfigMeta()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode metadata: %v\n", err)
		return 1
	}

	return 0
}

func resolvedWebConfigDTO(configPath string) (webConfigDTO, error) {
	cfg, err := agent.LoadResolvedConfig(configPath)
	if err != nil {
		return webConfigDTO{}, err
	}
	return webConfigDTOFromAgentConfig(cfg), nil
}

// runConfig implements the `agent config` subcommand. It reads the current
// agent.toml over the canonical defaults and emits the resolved config in the
// config_web wire format.
func runConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	formatFlag := fs.String("format", "json", "output format (only json supported)")
	configFlag := fs.String("config", "", "path to agent.toml or config directory")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse flags: %v\n", err)
		return 1
	}

	if *formatFlag != "json" {
		fmt.Fprintln(os.Stderr, "only --format=json is supported")
		return 1
	}

	if strings.TrimSpace(*configFlag) == "" {
		fmt.Fprintln(os.Stderr, "--config is required")
		return 1
	}

	dto, err := resolvedWebConfigDTO(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(dto); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode config: %v\n", err)
		return 1
	}

	return 0
}

// parseValidationErrors converts a validation error into structured field errors
// The Config.Validate() returns simple error strings, we parse them to extract field names
func parseValidationErrors(err error) []ValidationError {
	if err == nil {
		return []ValidationError{}
	}

	errMsg := err.Error()
	errors := []ValidationError{}

	// Try to extract field name from common error patterns
	// Pattern 1: "search.provider is required when..."
	// Pattern 2: "invalid search.provider: ..."
	// Pattern 3: "model.provider is required"
	// Pattern 4: "vad_speech_threshold must be in [0,1]"

	// For now, return the whole error as a single validation error
	// We can enhance this later to parse specific field names
	field := ""
	message := errMsg

	// Try to extract field from error message
	// Common patterns in Config.Validate():
	if strings.Contains(errMsg, "search.provider") || strings.Contains(errMsg, "search provider") {
		field = "search.provider"
	} else if strings.Contains(errMsg, "search.api_key") || strings.Contains(errMsg, "search api_key") {
		field = "search.api_key"
	} else if strings.Contains(errMsg, "model.provider") {
		field = "model.provider"
	} else if strings.Contains(errMsg, "model.max_response_tokens") {
		field = "model.max_response_tokens"
	} else if strings.Contains(errMsg, "model.context_window") {
		field = "model.context_window"
	} else if strings.Contains(errMsg, "model.model_max_output_tokens") {
		field = "model.model_max_output_tokens"
	} else if strings.Contains(errMsg, "model.model") {
		field = "model.model"
	} else if strings.Contains(errMsg, "stt.provider") {
		field = "stt.provider"
	} else if strings.Contains(errMsg, "tts.provider") {
		field = "tts.provider"
	} else if strings.Contains(errMsg, "input_mode") {
		field = "input_mode"
	} else if strings.Contains(errMsg, "trigger_mode") {
		field = "trigger_mode"
	} else if strings.Contains(errMsg, "hid.pointer_mode") || strings.Contains(errMsg, "pointer_mode") {
		field = "hid.pointer_mode"
	} else if strings.Contains(errMsg, "max_iterations") {
		field = "max_iterations"
	} else if strings.Contains(errMsg, "vad_speech_threshold") {
		field = "vad_speech_threshold"
	} else if strings.Contains(errMsg, "voice_followup_timeout_ms") {
		field = "voice_followup_timeout_ms"
	} else if strings.Contains(errMsg, "voice_first_turn_timeout_ms") {
		field = "voice_first_turn_timeout_ms"
	} else if strings.Contains(errMsg, "voice_max_turns") {
		field = "voice_max_turns"
	} else if strings.Contains(errMsg, "voice_max_response_tokens") {
		field = "voice_max_response_tokens"
	} else if strings.Contains(errMsg, "screenshot_keep_n") {
		field = "screenshot_keep_n"
	} else if strings.Contains(errMsg, "screenshot_prune_interval") {
		field = "screenshot_prune_interval"
	} else if strings.Contains(errMsg, "screen_stable_timeout_ms") {
		field = "screen_stable_timeout_ms"
	} else if strings.Contains(errMsg, "screen_stable_ms") {
		field = "screen_stable_ms"
	} else if strings.Contains(errMsg, "screen_stable_diff_threshold") {
		field = "screen_stable_diff_threshold"
	} else if strings.Contains(errMsg, "audio.sample_rate") {
		field = "audio.sample_rate"
	} else if strings.Contains(errMsg, "audio.channels") {
		field = "audio.channels"
	} else if strings.Contains(errMsg, "audio.bit_width") {
		field = "audio.bit_width"
	} else if strings.Contains(errMsg, "telemetry.base_url") {
		field = "telemetry.base_url"
	} else if strings.Contains(errMsg, "telemetry.public_key") {
		field = "telemetry.public_key"
	} else if strings.Contains(errMsg, "telemetry.secret_key") {
		field = "telemetry.secret_key"
	} else if strings.Contains(errMsg, "telemetry.provider") {
		field = "telemetry.provider"
	} else if strings.Contains(errMsg, "telemetry.upload_timeout_sec") {
		field = "telemetry.upload_timeout_sec"
	} else if strings.Contains(errMsg, "telemetry.max_retry") {
		field = "telemetry.max_retry"
	} else if strings.Contains(errMsg, "log.llm_http_retention_days") {
		field = "log.llm_http_retention_days"
	} else if strings.Contains(errMsg, "live_activity.relay_url") {
		field = "live_activity.relay_url"
	} else if strings.Contains(errMsg, "live_activity.environment") {
		field = "live_activity.environment"
	} else if strings.Contains(errMsg, "live_activity.timeout_sec") {
		field = "live_activity.timeout_sec"
	} else if strings.Contains(errMsg, "live_activity.bundle_id") || strings.Contains(errMsg, "live_activity.topic") {
		field = "live_activity.bundle_id"
	}

	errors = append(errors, ValidationError{
		Field:   field,
		Message: message,
	})

	return errors
}

// writeConfigCheckError writes an error message as a JSON ValidationResult
func writeConfigCheckError(message string) {
	result := ValidationResult{
		Valid: false,
		Errors: []ValidationError{
			{
				Field:   "",
				Message: message,
			},
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.Encode(result)
}
