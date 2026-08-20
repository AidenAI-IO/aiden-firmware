package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/configdoc"
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

type configUpdateResult struct {
	OK             bool         `json:"ok"`
	Config         webConfigDTO `json:"config"`
	ChangedPaths   []string     `json:"changed_paths"`
	RebootRequired bool         `json:"reboot_required"`
	Error          string       `json:"error,omitempty"`
	ErrorKind      string       `json:"error_kind,omitempty"`
}

const (
	configUpdateErrorInvalid  = "invalid_request"
	configUpdateErrorInternal = "internal"
)

type configUpdateError struct {
	kind string
	err  error
}

func (e *configUpdateError) Error() string { return e.err.Error() }
func (e *configUpdateError) Unwrap() error { return e.err }

func invalidConfigUpdate(err error) error {
	return &configUpdateError{kind: configUpdateErrorInvalid, err: err}
}

func internalConfigUpdate(err error) error {
	return &configUpdateError{kind: configUpdateErrorInternal, err: err}
}

func configUpdateErrorKind(err error) string {
	var updateErr *configUpdateError
	if errors.As(err, &updateErr) {
		return updateErr.kind
	}
	return configUpdateErrorInternal
}

type providerRenames map[string]map[string]string

type ConfigTestResult struct {
	OK         bool              `json:"ok"`
	Results    []ConfigTestCheck `json:"results"`
	Transcript string            `json:"transcript,omitempty"`
}

type ConfigTestCheck struct {
	Check  string `json:"check"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// webConfigDTO is the single definition of the config_web <-> agent wire contract: keys
// are snake_case, agent-level settings live under "agent", and write-only
// credentials report only configured-state flags rather than echoing values.
//
// Keep this struct in lockstep with the config web API; the round-trip is covered
// by TestConfigCheck_WireFormatContract.
type webConfigDTO struct {
	ModelProviders     map[string]modelProviderDTO   `json:"model_providers,omitempty"`
	TTSProviders       map[string]ttsProviderDTO     `json:"tts_providers,omitempty"`
	STTProviders       map[string]sttProviderDTO     `json:"stt_providers,omitempty"`
	Model              modelDTO                      `json:"model"`
	TTS                ttsDTO                        `json:"tts"`
	STT                sttDTO                        `json:"stt"`
	Audio              audioDTO                      `json:"audio"`
	AudioArchive       audioArchiveDTO               `json:"audio_archive"`
	QuickCapture       quickCaptureDTO               `json:"quick_capture"`
	Storage            storageDTO                    `json:"storage"`
	VoiceNotifications voiceNotificationsDTO         `json:"voice_notifications"`
	Device             deviceDTO                     `json:"device"`
	Log                logDTO                        `json:"log"`
	OTA                otaDTO                        `json:"ota"`
	HID                hidDTO                        `json:"hid"`
	Search             searchDTO                     `json:"search"`
	Telemetry          telemetryDTO                  `json:"telemetry"`
	LiveActivity       liveActivityDTO               `json:"live_activity"`
	TerminationPolicy  agent.TerminationPolicyConfig `json:"termination_policy"`
	Agent              agentDTO                      `json:"agent"`
}

// UnmarshalJSON rejects the former top-level providers key so callers do not
// get a successful response for a payload whose provider records were ignored.
func (d *webConfigDTO) UnmarshalJSON(data []byte) error {
	type canonicalDTO webConfigDTO
	if err := json.Unmarshal(data, (*canonicalDTO)(d)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, exists := fields["providers"]; exists {
		return errors.New(`"providers" is unsupported; use "model_providers"`)
	}
	return nil
}

type modelDTO struct {
	Provider             string   `json:"provider"`
	APIKey               string   `json:"api_key,omitempty"`
	Model                string   `json:"model"`
	APIMode              string   `json:"api_mode,omitempty"`
	ReasoningEffort      string   `json:"reasoning_effort"`
	Temperature          *float64 `json:"temperature,omitempty"`
	MaxResponseTokens    int      `json:"max_response_tokens"`
	LogRawHTTP           bool     `json:"log_raw_http"`
	ContextWindow        int      `json:"context_window"`
	ModelMaxOutputTokens int      `json:"model_max_output_tokens"`
}

func (d modelDTO) providerTestRequest() agent.ModelProviderTestRequest {
	return agent.ModelProviderTestRequest{
		Provider:        d.Provider,
		APIKey:          d.APIKey,
		Model:           d.Model,
		APIMode:         d.APIMode,
		Temperature:     d.Temperature,
		ReasoningEffort: d.ReasoningEffort,
	}
}

// modelProviderDTO mirrors a single [model_providers.<name>] section. Named providers hold
// the credentials; a model section references one by putting the provider name
// in its own "provider" field.
type modelProviderDTO struct {
	Type      string `json:"type"`
	APIKey    string `json:"api_key,omitempty"`
	HasAPIKey bool   `json:"has_api_key,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
}

func (d *modelProviderDTO) UnmarshalJSON(data []byte) error {
	type canonical modelProviderDTO
	var fields struct {
		canonical
		Type           *string `json:"type"`
		LegacyProvider string  `json:"provider"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	typePresent, err := jsonFieldPresent(data, "type")
	if err != nil {
		return err
	}
	*d = modelProviderDTO(fields.canonical)
	if typePresent {
		if fields.Type != nil {
			d.Type = *fields.Type
		}
		return nil
	}
	d.Type = fields.LegacyProvider
	return nil
}

// ttsProviderDTO mirrors a single [tts_providers.<name>] section. [tts]
// references one by putting the record name in its own "provider" field. speed
// is absent on purpose: it is a listening preference that stays global on [tts]
// so switching voice never changes playback speed.
type ttsProviderDTO struct {
	Type        string `json:"type"`
	APIKey      string `json:"api_key,omitempty"`
	HasAPIKey   bool   `json:"has_api_key,omitempty"`
	Model       string `json:"model,omitempty"`
	VoiceID     string `json:"voice_id,omitempty"`
	Emotion     string `json:"emotion,omitempty"`
	ReferenceID string `json:"reference_id,omitempty"`
}

func (d *ttsProviderDTO) UnmarshalJSON(data []byte) error {
	type canonical ttsProviderDTO
	var fields struct {
		canonical
		Type           *string `json:"type"`
		LegacyProvider string  `json:"provider"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	typePresent, err := jsonFieldPresent(data, "type")
	if err != nil {
		return err
	}
	*d = ttsProviderDTO(fields.canonical)
	if typePresent {
		if fields.Type != nil {
			d.Type = *fields.Type
		}
		return nil
	}
	d.Type = fields.LegacyProvider
	return nil
}

// sttProviderDTO mirrors a single [stt_providers.<name>] section. language stays
// on [stt]: it holds regardless of which provider transcribes.
type sttProviderDTO struct {
	Type            string `json:"type"`
	APIKey          string `json:"api_key,omitempty"`
	HasAPIKey       bool   `json:"has_api_key,omitempty"`
	Model           string `json:"model,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`
	AppID           string `json:"app_id,omitempty"`
	SecretID        string `json:"secret_id,omitempty"`
	HasSecretID     bool   `json:"has_secret_id,omitempty"`
	SecretKey       string `json:"secret_key,omitempty"`
	HasSecretKey    bool   `json:"has_secret_key,omitempty"`
	Region          string `json:"region,omitempty"`
	EngineModelType string `json:"engine_model_type,omitempty"`
}

func (d *sttProviderDTO) UnmarshalJSON(data []byte) error {
	type canonical sttProviderDTO
	var fields struct {
		canonical
		Type           *string `json:"type"`
		LegacyProvider string  `json:"provider"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	typePresent, err := jsonFieldPresent(data, "type")
	if err != nil {
		return err
	}
	*d = sttProviderDTO(fields.canonical)
	if typePresent {
		if fields.Type != nil {
			d.Type = *fields.Type
		}
		return nil
	}
	d.Type = fields.LegacyProvider
	return nil
}

func jsonFieldPresent(data []byte, key string) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false, err
	}
	_, exists := fields[key]
	return exists, nil
}

type ttsDTO struct {
	Provider    string  `json:"provider"`
	APIKey      string  `json:"api_key,omitempty"`
	Model       string  `json:"model"`
	VoiceID     string  `json:"voice_id"`
	ReferenceID string  `json:"reference_id"`
	Emotion     string  `json:"emotion"`
	Speed       float64 `json:"speed"`
}

func (d ttsDTO) playbackTestRequest(text string) agent.TTSPlaybackTestRequest {
	return agent.TTSPlaybackTestRequest{
		Provider:    d.Provider,
		APIKey:      d.APIKey,
		Model:       d.Model,
		VoiceID:     d.VoiceID,
		ReferenceID: d.ReferenceID,
		Emotion:     d.Emotion,
		Speed:       d.Speed,
		Text:        text,
	}
}

type sttDTO struct {
	Provider        string `json:"provider"`
	Language        string `json:"language"`
	APIKey          string `json:"api_key,omitempty"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url"`
	AppID           string `json:"app_id"`
	SecretID        string `json:"secret_id,omitempty"`
	SecretKey       string `json:"secret_key,omitempty"`
	Region          string `json:"region"`
	EngineModelType string `json:"engine_model_type"`
}

func (d sttDTO) transcriptionTestRequest(wavData []byte) agent.STTTranscriptionTestRequest {
	return agent.STTTranscriptionTestRequest{
		Provider:        d.Provider,
		Language:        d.Language,
		APIKey:          d.APIKey,
		Model:           d.Model,
		BaseURL:         d.BaseURL,
		AppID:           d.AppID,
		SecretID:        d.SecretID,
		SecretKey:       d.SecretKey,
		Region:          d.Region,
		EngineModelType: d.EngineModelType,
		WAVData:         wavData,
	}
}

type audioDTO struct {
	Socket          string `json:"socket"`
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	BitWidth        int    `json:"bit_width"`
	PlaybackBackend string `json:"playback_backend"`
}

type audioArchiveDTO struct {
	Enabled     bool   `json:"enabled"`
	MaxFiles    int    `json:"max_files"`
	MaxSizeMB   int    `json:"max_size_mb"`
	StoragePath string `json:"storage_path"`
}

type quickCaptureDTO struct {
	Enabled         bool   `json:"enabled"`
	GPIOPin         int    `json:"gpio_pin"`
	ScreenMemoryTTL string `json:"screen_memory_ttl"`
}

type storageDTO struct {
	MonitorEnabled       bool                   `json:"monitor_enabled"`
	MountPoint           string                 `json:"mount_point"`
	Device               string                 `json:"device"`
	MinCardFreeMB        int                    `json:"min_card_free_mb"`
	MigrateStartFreePct  int                    `json:"migrate_start_free_pct"`
	MigrateStopFreePct   int                    `json:"migrate_stop_free_pct"`
	RootPath             string                 `json:"root_path"`
	CheckIntervalSeconds int                    `json:"check_interval_seconds"`
	WarningThresholdMB   uint64                 `json:"warning_threshold_mb"`
	CriticalThresholdMB  uint64                 `json:"critical_threshold_mb"`
	EmergencyThresholdMB uint64                 `json:"emergency_threshold_mb"`
	RecoveryHysteresisMB uint64                 `json:"recovery_hysteresis_mb"`
	DegradedMode         storageDegradedModeDTO `json:"degraded_mode"`
	Cleanup              storageCleanupDTO      `json:"cleanup"`
}

type storageDegradedModeDTO struct {
	DisableLLMHTTPLog     bool `json:"disable_llm_http_log"`
	DisableAudioArchive   bool `json:"disable_audio_archive"`
	DisableSessionArchive bool `json:"disable_session_archive"`
	MaxAgentLogMB         int  `json:"max_agent_log_mb"`
}

type storageCleanupDTO struct {
	Enabled                     bool  `json:"enabled"`
	LLMHTTPLogRetentionDays     []int `json:"llm_http_log_retention_days"`
	AudioArchiveRetentionDays   []int `json:"audio_archive_retention_days"`
	SessionArchiveRetentionDays []int `json:"session_archive_retention_days"`
	CleanupRetryIntervalSeconds int   `json:"cleanup_retry_interval_seconds"`
}

type deviceDTO struct {
	Backend    string `json:"backend,omitempty"`
	DeviceType string `json:"device_type"`
}

type voiceNotificationsDTO struct {
	Enabled      *bool                            `json:"enabled"`
	MaxPending   int                              `json:"max_pending"`
	ResponseTail voiceNotificationResponseTailDTO `json:"response_tail"`
	Expiration   voiceNotificationExpirationDTO   `json:"expiration"`
}

type voiceNotificationResponseTailDTO struct {
	Enabled      *bool `json:"enabled"`
	MaxItems     int   `json:"max_items"`
	MaxTextChars int   `json:"max_text_chars"`
}

type voiceNotificationExpirationDTO struct {
	DefaultTTLSeconds int            `json:"default_ttl_seconds"`
	CodeTTLSeconds    map[string]int `json:"code_ttl_seconds"`
}

type logDTO struct {
	LLMHTTPRetentionDays int `json:"llm_http_retention_days"`
}

type otaDTO struct {
	GitHubProxyURL string `json:"github_proxy_url"`
}

type hidDTO struct {
	KeyboardDevice        string `json:"keyboard_device"`
	KeyboardLayout        string `json:"keyboard_layout"`
	MouseDevice           string `json:"mouse_device"`
	AndroidKeyboardDevice string `json:"android_keyboard_device"`
	FrameSocket           string `json:"frame_socket"`
	PointerMode           string `json:"pointer_mode"`
	InputBackend          string `json:"input_backend"`
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
	Enabled *bool `json:"enabled"`
}

type agentDTO struct {
	Locale                     string  `json:"locale"`
	CustomInstruction          string  `json:"custom_instruction"`
	AdditionalPrompt           string  `json:"additional_prompt"`
	InputMode                  string  `json:"input_mode"`
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
	LoadAllTools               bool    `json:"load_all_tools"`
	MaxIterations              int     `json:"max_iterations"`
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
	storage := agent.DefaultConfig().Storage
	storage.MonitorEnabled = d.Storage.MonitorEnabled
	storage.MountPoint = d.Storage.MountPoint
	storage.Device = d.Storage.Device
	storage.MinCardFreeMB = d.Storage.MinCardFreeMB
	storage.MigrateStartFreePct = d.Storage.MigrateStartFreePct
	storage.MigrateStopFreePct = d.Storage.MigrateStopFreePct
	storage.RootPath = d.Storage.RootPath
	storage.CheckIntervalSeconds = d.Storage.CheckIntervalSeconds
	storage.WarningThresholdMB = d.Storage.WarningThresholdMB
	storage.CriticalThresholdMB = d.Storage.CriticalThresholdMB
	storage.EmergencyThresholdMB = d.Storage.EmergencyThresholdMB
	storage.RecoveryHysteresisMB = d.Storage.RecoveryHysteresisMB
	storage.DegradedMode = agent.StorageDegradedModeConfig{
		DisableLLMHTTPLog:     d.Storage.DegradedMode.DisableLLMHTTPLog,
		DisableAudioArchive:   d.Storage.DegradedMode.DisableAudioArchive,
		DisableSessionArchive: d.Storage.DegradedMode.DisableSessionArchive,
		MaxAgentLogMB:         d.Storage.DegradedMode.MaxAgentLogMB,
	}
	storage.Cleanup = agent.StorageCleanupConfig{
		Enabled:                     d.Storage.Cleanup.Enabled,
		LLMHTTPLogRetentionDays:     d.Storage.Cleanup.LLMHTTPLogRetentionDays,
		AudioArchiveRetentionDays:   d.Storage.Cleanup.AudioArchiveRetentionDays,
		SessionArchiveRetentionDays: d.Storage.Cleanup.SessionArchiveRetentionDays,
		CleanupRetryIntervalSeconds: d.Storage.Cleanup.CleanupRetryIntervalSeconds,
	}
	return agent.Config{
		ModelProviders: d.modelProvidersToAgentConfig(),
		TTSProviders:   d.ttsProvidersToAgentConfig(),
		STTProviders:   d.sttProvidersToAgentConfig(),
		Model: agent.ModelConfig{
			Provider:             d.Model.Provider,
			APIKey:               d.Model.APIKey,
			Model:                d.Model.Model,
			APIMode:              d.Model.APIMode,
			Temperature:          d.Model.Temperature,
			MaxResponseTokens:    d.Model.MaxResponseTokens,
			LogRawHTTP:           d.Model.LogRawHTTP,
			ContextWindow:        d.Model.ContextWindow,
			ModelMaxOutputTokens: d.Model.ModelMaxOutputTokens,
		},
		TTS: agent.TTSConfig{
			Provider:    d.TTS.Provider,
			APIKey:      d.TTS.APIKey,
			Model:       d.TTS.Model,
			VoiceID:     d.TTS.VoiceID,
			ReferenceID: d.TTS.ReferenceID,
			Emotion:     d.TTS.Emotion,
			Speed:       d.TTS.Speed,
		},
		STT: agent.STTConfig{
			Provider:        d.STT.Provider,
			Language:        d.STT.Language,
			APIKey:          d.STT.APIKey,
			Model:           d.STT.Model,
			BaseURL:         d.STT.BaseURL,
			AppID:           d.STT.AppID,
			SecretID:        d.STT.SecretID,
			SecretKey:       d.STT.SecretKey,
			Region:          d.STT.Region,
			EngineModelType: d.STT.EngineModelType,
		},
		Audio: agent.AudioConfig{
			Socket:          d.Audio.Socket,
			SampleRate:      d.Audio.SampleRate,
			Channels:        d.Audio.Channels,
			BitWidth:        d.Audio.BitWidth,
			PlaybackBackend: d.Audio.PlaybackBackend,
		},
		AudioArchive: agent.AudioArchiveConfig{
			Enabled:     d.AudioArchive.Enabled,
			MaxFiles:    d.AudioArchive.MaxFiles,
			MaxSizeMB:   d.AudioArchive.MaxSizeMB,
			StoragePath: d.AudioArchive.StoragePath,
		},
		QuickCapture: agent.QuickCaptureConfig{
			Enabled:         boolPtr(d.QuickCapture.Enabled),
			GPIOPin:         d.QuickCapture.GPIOPin,
			ScreenMemoryTTL: d.QuickCapture.ScreenMemoryTTL,
		},
		Storage: storage,
		VoiceNotifications: agent.VoiceNotificationsConfig{
			Enabled:    d.VoiceNotifications.Enabled,
			MaxPending: d.VoiceNotifications.MaxPending,
			ResponseTail: agent.VoiceNotificationResponseTailConfig{
				Enabled:      d.VoiceNotifications.ResponseTail.Enabled,
				MaxItems:     d.VoiceNotifications.ResponseTail.MaxItems,
				MaxTextChars: d.VoiceNotifications.ResponseTail.MaxTextChars,
			},
			Expiration: agent.VoiceNotificationExpirationConfig{
				DefaultTTLSeconds: d.VoiceNotifications.Expiration.DefaultTTLSeconds,
				CodeTTLSeconds:    d.VoiceNotifications.Expiration.CodeTTLSeconds,
			},
		},
		Log: agent.LogConfig{
			LLMHTTPRetentionDays: d.Log.LLMHTTPRetentionDays,
		},
		OTA: agent.OTAConfig{
			GitHubProxyURL: d.OTA.GitHubProxyURL,
		},
		HID: agent.HIDConfig{
			KeyboardDevice:        d.HID.KeyboardDevice,
			KeyboardLayout:        d.HID.KeyboardLayout,
			MouseDevice:           d.HID.MouseDevice,
			AndroidKeyboardDevice: d.HID.AndroidKeyboardDevice,
			FrameSocket:           d.HID.FrameSocket,
			PointerMode:           d.HID.PointerMode,
			InputBackend:          d.HID.InputBackend,
		},
		Device: agent.DeviceConfig{
			Backend:    d.Device.Backend,
			DeviceType: d.Device.DeviceType,
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
			Enabled: d.LiveActivity.Enabled,
		},
		TerminationPolicy:          d.TerminationPolicy,
		Locale:                     d.Agent.Locale,
		Instruction:                d.Agent.CustomInstruction,
		AdditionalPrompt:           d.Agent.AdditionalPrompt,
		InputMode:                  d.Agent.InputMode,
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
		LoadAllTools:               d.Agent.LoadAllTools,
		MaxIterations:              d.Agent.MaxIterations,
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

func modelProviderDTOsFromConfig(providers map[string]agent.ModelProvider) map[string]modelProviderDTO {
	if len(providers) == 0 {
		return nil
	}
	result := make(map[string]modelProviderDTO, len(providers))
	for name, provider := range providers {
		result[name] = modelProviderDTO{
			Type:      provider.Type,
			HasAPIKey: strings.TrimSpace(provider.APIKey) != "",
			BaseURL:   provider.BaseURL,
		}
	}
	return result
}

func (d webConfigDTO) modelProvidersToAgentConfig() map[string]agent.ModelProvider {
	if len(d.ModelProviders) == 0 {
		return nil
	}
	result := make(map[string]agent.ModelProvider, len(d.ModelProviders))
	for name, provider := range d.ModelProviders {
		result[name] = agent.ModelProvider{
			Type:    provider.Type,
			APIKey:  provider.APIKey,
			BaseURL: provider.BaseURL,
		}
		if result[name].APIKey == "" && provider.HasAPIKey {
			result[name] = agent.ModelProvider{Type: provider.Type, APIKey: hasAPIKeyPlaceholder, BaseURL: provider.BaseURL}
		}
	}
	return result
}

func ttsProviderDTOsFromConfig(providers map[string]agent.TTSProvider) map[string]ttsProviderDTO {
	if len(providers) == 0 {
		return nil
	}
	result := make(map[string]ttsProviderDTO, len(providers))
	for name, provider := range providers {
		result[name] = ttsProviderDTO{
			Type:        provider.Type,
			HasAPIKey:   strings.TrimSpace(provider.APIKey) != "",
			Model:       provider.Model,
			VoiceID:     provider.VoiceID,
			Emotion:     provider.Emotion,
			ReferenceID: provider.ReferenceID,
		}
	}
	return result
}

func (d webConfigDTO) ttsProvidersToAgentConfig() map[string]agent.TTSProvider {
	if len(d.TTSProviders) == 0 {
		return nil
	}
	result := make(map[string]agent.TTSProvider, len(d.TTSProviders))
	for name, provider := range d.TTSProviders {
		mapped := agent.TTSProvider{
			Type:        provider.Type,
			APIKey:      provider.APIKey,
			Model:       provider.Model,
			VoiceID:     provider.VoiceID,
			Emotion:     provider.Emotion,
			ReferenceID: provider.ReferenceID,
		}
		if mapped.APIKey == "" && provider.HasAPIKey {
			mapped.APIKey = hasAPIKeyPlaceholder
		}
		result[name] = mapped
	}
	return result
}

func sttProviderDTOsFromConfig(providers map[string]agent.STTProvider) map[string]sttProviderDTO {
	if len(providers) == 0 {
		return nil
	}
	result := make(map[string]sttProviderDTO, len(providers))
	for name, provider := range providers {
		result[name] = sttProviderDTO{
			Type:            provider.Type,
			HasAPIKey:       strings.TrimSpace(provider.APIKey) != "",
			Model:           provider.Model,
			BaseURL:         provider.BaseURL,
			AppID:           provider.AppID,
			HasSecretID:     strings.TrimSpace(provider.SecretID) != "",
			HasSecretKey:    strings.TrimSpace(provider.SecretKey) != "",
			Region:          provider.Region,
			EngineModelType: provider.EngineModelType,
		}
	}
	return result
}

func (d webConfigDTO) sttProvidersToAgentConfig() map[string]agent.STTProvider {
	if len(d.STTProviders) == 0 {
		return nil
	}
	result := make(map[string]agent.STTProvider, len(d.STTProviders))
	for name, provider := range d.STTProviders {
		mapped := agent.STTProvider{
			Type:            provider.Type,
			APIKey:          provider.APIKey,
			Model:           provider.Model,
			BaseURL:         provider.BaseURL,
			AppID:           provider.AppID,
			SecretID:        provider.SecretID,
			SecretKey:       provider.SecretKey,
			Region:          provider.Region,
			EngineModelType: provider.EngineModelType,
		}
		if mapped.APIKey == "" && provider.HasAPIKey {
			mapped.APIKey = hasAPIKeyPlaceholder
		}
		if mapped.SecretID == "" && provider.HasSecretID {
			mapped.SecretID = hasAPIKeyPlaceholder
		}
		if mapped.SecretKey == "" && provider.HasSecretKey {
			mapped.SecretKey = hasAPIKeyPlaceholder
		}
		result[name] = mapped
	}
	return result
}

func webConfigDTOFromAgentConfig(cfg agent.Config) webConfigDTO {
	audioArchive := cfg.AudioArchive
	migrateStartFreePct, migrateStopFreePct := cfg.Storage.MigrateWatermarksOrDefault()

	return webConfigDTO{
		ModelProviders: modelProviderDTOsFromConfig(cfg.ModelProviders),
		TTSProviders:   ttsProviderDTOsFromConfig(cfg.TTSProviders),
		STTProviders:   sttProviderDTOsFromConfig(cfg.STTProviders),
		Model: modelDTO{
			Provider:             cfg.Model.Provider,
			Model:                cfg.Model.Model,
			APIMode:              cfg.Model.APIMode,
			ReasoningEffort:      cfg.Model.ReasoningEffort,
			Temperature:          cfg.Model.Temperature,
			MaxResponseTokens:    cfg.Model.MaxResponseTokens,
			LogRawHTTP:           cfg.Model.LogRawHTTP,
			ContextWindow:        cfg.Model.ContextWindow,
			ModelMaxOutputTokens: cfg.Model.ModelMaxOutputTokens,
		},
		TTS: ttsDTO{
			Provider:    cfg.TTS.Provider,
			Model:       cfg.TTS.Model,
			VoiceID:     cfg.TTS.VoiceID,
			ReferenceID: cfg.TTS.ReferenceID,
			Emotion:     cfg.TTS.Emotion,
			Speed:       cfg.TTS.Speed,
		},
		STT: sttDTO{
			Provider:        cfg.STT.Provider,
			Language:        cfg.STT.Language,
			Model:           cfg.STT.Model,
			BaseURL:         cfg.STT.BaseURL,
			AppID:           cfg.STT.AppID,
			Region:          cfg.STT.Region,
			EngineModelType: cfg.STT.EngineModelType,
		},
		Audio: audioDTO{
			Socket:          cfg.Audio.SocketOrDefault(),
			SampleRate:      cfg.Audio.SampleRateOrDefault(),
			Channels:        cfg.Audio.ChannelsOrDefault(),
			BitWidth:        cfg.Audio.BitWidthOrDefault(),
			PlaybackBackend: cfg.Audio.PlaybackBackendOrDefault(),
		},
		AudioArchive: audioArchiveDTO{
			Enabled:     audioArchive.Enabled,
			MaxFiles:    audioArchive.MaxFilesOrDefault(),
			MaxSizeMB:   audioArchive.MaxSizeMBOrDefault(),
			StoragePath: audioArchive.StoragePathOrDefault(),
		},
		QuickCapture: quickCaptureDTO{
			Enabled:         cfg.QuickCapture.EnabledOrDefault(),
			GPIOPin:         cfg.QuickCapture.GPIOPin,
			ScreenMemoryTTL: cfg.QuickCapture.ScreenMemoryTTLOrDefault(),
		},
		Storage: storageDTO{
			MonitorEnabled:       cfg.Storage.MonitorEnabled,
			MountPoint:           cfg.Storage.MountPointOrDefault(),
			Device:               cfg.Storage.DeviceOrDefault(),
			MinCardFreeMB:        cfg.Storage.MinCardFreeMBOrDefault(),
			MigrateStartFreePct:  migrateStartFreePct,
			MigrateStopFreePct:   migrateStopFreePct,
			RootPath:             cfg.Storage.RootPath,
			CheckIntervalSeconds: cfg.Storage.CheckIntervalSeconds,
			WarningThresholdMB:   cfg.Storage.WarningThresholdMB,
			CriticalThresholdMB:  cfg.Storage.CriticalThresholdMB,
			EmergencyThresholdMB: cfg.Storage.EmergencyThresholdMB,
			RecoveryHysteresisMB: cfg.Storage.RecoveryHysteresisMB,
			DegradedMode: storageDegradedModeDTO{
				DisableLLMHTTPLog:     cfg.Storage.DegradedMode.DisableLLMHTTPLog,
				DisableAudioArchive:   cfg.Storage.DegradedMode.DisableAudioArchive,
				DisableSessionArchive: cfg.Storage.DegradedMode.DisableSessionArchive,
				MaxAgentLogMB:         cfg.Storage.DegradedMode.MaxAgentLogMB,
			},
			Cleanup: storageCleanupDTO{
				Enabled:                     cfg.Storage.Cleanup.Enabled,
				LLMHTTPLogRetentionDays:     cfg.Storage.Cleanup.LLMHTTPLogRetentionDays,
				AudioArchiveRetentionDays:   cfg.Storage.Cleanup.AudioArchiveRetentionDays,
				SessionArchiveRetentionDays: cfg.Storage.Cleanup.SessionArchiveRetentionDays,
				CleanupRetryIntervalSeconds: cfg.Storage.Cleanup.CleanupRetryIntervalSeconds,
			},
		},
		Device: deviceDTO{
			Backend:    cfg.Device.BackendOrDefault(),
			DeviceType: cfg.DeviceTypeOrDefault(),
		},
		VoiceNotifications: voiceNotificationsDTO{
			Enabled:    cfg.VoiceNotifications.Enabled,
			MaxPending: cfg.VoiceNotifications.MaxPendingOrDefault(),
			ResponseTail: voiceNotificationResponseTailDTO{
				Enabled:      cfg.VoiceNotifications.ResponseTail.Enabled,
				MaxItems:     cfg.VoiceNotifications.ResponseTail.MaxItems,
				MaxTextChars: cfg.VoiceNotifications.ResponseTail.MaxTextCharsOrDefault(),
			},
			Expiration: voiceNotificationExpirationDTO{
				DefaultTTLSeconds: cfg.VoiceNotifications.Expiration.DefaultTTLSeconds,
				CodeTTLSeconds:    cfg.VoiceNotifications.Expiration.CodeTTLSeconds,
			},
		},
		Log: logDTO{
			LLMHTTPRetentionDays: cfg.Log.LLMHTTPRetentionDaysOrDefault(),
		},
		OTA: otaDTO{
			GitHubProxyURL: cfg.OTA.GitHubProxyURLOrDefault(),
		},
		HID: hidDTO{
			KeyboardDevice:        cfg.HID.KeyboardDeviceOrDefault(),
			KeyboardLayout:        cfg.HID.KeyboardLayoutOrDefault(),
			MouseDevice:           cfg.HID.MouseDeviceOrDefault(),
			AndroidKeyboardDevice: cfg.HID.AndroidKeyboardDeviceOrDefault(),
			FrameSocket:           cfg.HID.FrameSocketOrDefault(),
			PointerMode:           cfg.PointerModeOrDefault(),
			InputBackend:          cfg.HID.InputBackendOrDefault(),
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
			Enabled: boolPtr(cfg.LiveActivity.EnabledOrDefault()),
		},
		TerminationPolicy: cfg.TerminationPolicyOrDefault(),
		Agent: agentDTO{
			Locale:                     cfg.LocaleOrDefault(),
			CustomInstruction:          customInstructionValue(cfg.Instruction),
			AdditionalPrompt:           cfg.AdditionalPrompt,
			InputMode:                  cfg.InputModeOrDefault(),
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
			LoadAllTools:               cfg.LoadAllTools,
			MaxIterations:              cfg.MaxIterations,
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
	configFlag := fs.String("config", "", "path to a TOML config file")

	if err := fs.Parse(args); err != nil {
		writeConfigCheckError("failed to parse flags: " + err.Error())
		return 1
	}

	if *formatFlag != "json" {
		writeConfigCheckError("only --format=json is supported")
		return 1
	}

	configPath := strings.TrimSpace(*configFlag)
	if *stdinFlag == (configPath != "") {
		writeConfigCheckError("exactly one of --stdin or --config is required")
		return 1
	}

	var result ValidationResult
	if *stdinFlag {
		var decodeErr error
		result, decodeErr = checkConfig(os.Stdin)
		if decodeErr != nil {
			writeConfigCheckError("invalid JSON input: " + decodeErr.Error())
			return 1
		}
	} else {
		result = checkConfigPath(configPath)
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

func checkConfigPath(path string) ValidationResult {
	if _, err := agent.LoadResolvedConfig(path); err != nil {
		return ValidationResult{Valid: false, Errors: parseValidationErrors(err)}
	}
	return ValidationResult{Valid: true, Errors: []ValidationError{}}
}

// checkConfig reads a config_web wire-format payload from r, maps it onto
// agent.Config, and runs the canonical Config.Validate(). It returns the
// structured result, or a non-nil error only when the input is not decodable
// JSON. Splitting this out of runConfigCheck keeps the full
// decode -> map -> validate pipeline testable without driving os.Stdin/Stdout.
func checkConfig(r io.Reader) (ValidationResult, error) {
	// The payload is the config_web wire format defined by webConfigDTO:
	// snake_case keys, agent-level settings nested under an "agent" object, and
	// search exposing only a "has_api_key" boolean instead of the raw key.
	// agent.Config carries only TOML tags, so decoding straight
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
		return result, nil
	}
	// Voice provider records are checked only here, on the save path. Boot stays
	// lenient: a TTS init failure is a warning and the agent still starts, so a
	// device whose provider reference went stale must keep booting. But a save
	// that stores a dangling reference silently loses voice on the next restart,
	// so it has to be rejected while the user is still looking at the form.
	if err := cfg.ValidateVoiceProviders(); err != nil {
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
	configFlag := fs.String("config", "", "path to a TOML config file")

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

func runConfigUpdate(args []string) int {
	fs := flag.NewFlagSet("config-update", flag.ContinueOnError)
	formatFlag := fs.String("format", "json", "output format (only json supported)")
	stdinFlag := fs.Bool("stdin", false, "read JSON merge patch from stdin")
	configFlag := fs.String("config", "", "path to a TOML config file")
	if err := fs.Parse(args); err != nil {
		writeConfigUpdateError(invalidConfigUpdate(err))
		return 1
	}
	if *formatFlag != "json" || !*stdinFlag || strings.TrimSpace(*configFlag) == "" {
		writeConfigUpdateError(invalidConfigUpdate(fmt.Errorf("--config, --stdin and --format=json are required")))
		return 1
	}
	patch, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeConfigUpdateError(internalConfigUpdate(err))
		return 1
	}
	result, err := updateConfigFile(strings.TrimSpace(*configFlag), patch)
	if err != nil {
		writeConfigUpdateError(err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode config-update result: %v\n", err)
		return 1
	}
	return 0
}

func writeConfigUpdateError(err error) {
	_ = json.NewEncoder(os.Stdout).Encode(configUpdateResult{
		OK:        false,
		Error:     err.Error(),
		ErrorKind: configUpdateErrorKind(err),
	})
}

func updateConfigFile(path string, patchJSON []byte) (configUpdateResult, error) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("invalid JSON merge patch: %w", err))
	}
	if patch == nil {
		return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object"))
	}
	if nested, ok := patch["config"]; ok {
		var configPatch map[string]json.RawMessage
		if err := json.Unmarshal(nested, &configPatch); err != nil {
			return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object: %w", err))
		}
		if configPatch == nil {
			return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object"))
		}
		patch = configPatch
	}
	renames, err := takeProviderRenames(patch)
	if err != nil {
		return configUpdateResult{}, invalidConfigUpdate(err)
	}
	resolvedPath, original, fileMode, err := prepareConfigUpdateFile(path)
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(err)
	}
	current, err := agent.LoadResolvedConfig(resolvedPath)
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("load config: %w", err))
	}
	currentDTO := webConfigDTOFromAgentConfig(current)
	if err := normalizeLegacyWebConfigPatch(patch, current); err != nil {
		return configUpdateResult{}, invalidConfigUpdate(err)
	}
	if err := stripReadOnlyStatusFields(patch, currentDTO); err != nil {
		return configUpdateResult{}, internalConfigUpdate(err)
	}
	if err := restoreRenamedProviderCredentials(patch, renames, current); err != nil {
		return configUpdateResult{}, invalidConfigUpdate(err)
	}
	if err := preserveProviderCredentials(patch, current); err != nil {
		return configUpdateResult{}, invalidConfigUpdate(err)
	}
	patch, err = filterNoopWebConfigPatch(patch, currentDTO)
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(err)
	}
	operations, err := configPatchOperations(patch)
	if err != nil {
		return configUpdateResult{}, invalidConfigUpdate(err)
	}
	updated, changed, err := configdoc.Apply(original, operations)
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(err)
	}
	if len(changed) == 0 {
		cfg, err := agent.LoadResolvedConfig(resolvedPath)
		if err != nil {
			return configUpdateResult{}, internalConfigUpdate(err)
		}
		return configUpdateResult{OK: true, Config: webConfigDTOFromAgentConfig(cfg), ChangedPaths: []string{}, RebootRequired: false}, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolvedPath), ".agent.toml.config-update-*.toml")
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("create temporary config: %w", err))
	}
	tmpPath := tmp.Name()
	defer tmp.Close()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("set temporary config mode: %w", err))
	}
	n, err := tmp.Write(updated)
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("write temporary config: %w", err))
	}
	if n != len(updated) {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("write temporary config: %w", io.ErrShortWrite))
	}
	if err := tmp.Sync(); err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("sync temporary config: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("close temporary config: %w", err))
	}
	candidate, err := agent.LoadResolvedConfig(tmpPath)
	if err != nil {
		return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("validate config: %w", err))
	}
	if err := candidate.ValidateVoiceProviders(); err != nil {
		return configUpdateResult{}, invalidConfigUpdate(fmt.Errorf("validate voice providers: %w", err))
	}
	if err := os.Rename(tmpPath, resolvedPath); err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("replace config: %w", err))
	}
	directory, err := os.Open(filepath.Dir(resolvedPath))
	if err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("open config directory: %w", err))
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return configUpdateResult{}, internalConfigUpdate(fmt.Errorf("sync config directory: %w", err))
	}
	return configUpdateResult{
		OK:             true,
		Config:         webConfigDTOFromAgentConfig(candidate),
		ChangedPaths:   changed,
		RebootRequired: requiresConfigReboot(changed),
	}, nil
}

func prepareConfigUpdateFile(path string) (string, []byte, os.FileMode, error) {
	_, err := os.Lstat(path)
	if err == nil {
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, 0, fmt.Errorf("resolve config path: %w", err)
		}
		original, err := os.ReadFile(resolvedPath)
		if err != nil {
			return "", nil, 0, fmt.Errorf("read config: %w", err)
		}
		resolvedInfo, err := os.Stat(resolvedPath)
		if err != nil {
			return "", nil, 0, fmt.Errorf("stat config: %w", err)
		}
		return resolvedPath, original, resolvedInfo.Mode().Perm(), nil
	}
	if !os.IsNotExist(err) {
		return "", nil, 0, fmt.Errorf("read config path: %w", err)
	}

	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", nil, 0, fmt.Errorf("resolve config directory: %w", err)
	}
	dirInfo, err := os.Stat(resolvedDir)
	if err != nil {
		return "", nil, 0, fmt.Errorf("stat config directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return "", nil, 0, fmt.Errorf("config parent must be a directory: %s", resolvedDir)
	}
	return filepath.Join(resolvedDir, filepath.Base(path)), nil, 0o640, nil
}

// stripReadOnlyStatusFields removes unchanged has_* markers emitted by GET
// /api/config. They describe write-only credentials and are not writable TOML
// fields, but a complete GET response must still be safe to submit as a patch.
// Changed or malformed markers remain in the patch and are rejected normally.
func stripReadOnlyStatusFields(values map[string]json.RawMessage, current webConfigDTO) error {
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	var currentValues map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &currentValues); err != nil {
		return err
	}
	stripMatchingReadOnlyStatusFields(values, currentValues)
	return nil
}

func stripMatchingReadOnlyStatusFields(values, current map[string]json.RawMessage) {
	for key, raw := range values {
		if strings.HasPrefix(key, "has_") {
			if existing, ok := current[key]; ok && jsonValuesEqual(raw, existing) {
				delete(values, key)
			}
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var child, currentChild map[string]json.RawMessage
		if json.Unmarshal(raw, &child) != nil {
			continue
		}
		if currentRaw, ok := current[key]; !ok || json.Unmarshal(currentRaw, &currentChild) != nil {
			continue
		}
		stripMatchingReadOnlyStatusFields(child, currentChild)
		if encoded, err := json.Marshal(child); err == nil {
			values[key] = encoded
		}
	}
}

func normalizeLegacyWebConfigPatch(patch map[string]json.RawMessage, current agent.Config) error {
	for _, section := range []string{"model_providers", "tts_providers", "stt_providers"} {
		raw, ok := patch[section]
		if !ok {
			continue
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(raw, &records); err != nil || records == nil {
			continue
		}
		for name, rawRecord := range records {
			if bytes.Equal(bytes.TrimSpace(rawRecord), []byte("null")) {
				continue
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(rawRecord, &record); err != nil || record == nil {
				continue
			}
			if _, hasType := record["type"]; !hasType {
				if legacyType, hasLegacyType := record["provider"]; hasLegacyType {
					record["type"] = legacyType
				}
			}
			delete(record, "provider")
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[name] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}

	if rawAgent, ok := patch["agent"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rawAgent, &fields) == nil && fields != nil {
			delete(fields, "default_platform")
			delete(fields, "instruction")
			if len(fields) == 0 {
				delete(patch, "agent")
			} else if encoded, err := json.Marshal(fields); err == nil {
				patch["agent"] = encoded
			}
		}
	}

	rawModel, ok := patch["model"]
	if !ok {
		return nil
	}
	var model map[string]json.RawMessage
	if err := json.Unmarshal(rawModel, &model); err != nil || model == nil {
		return nil
	}
	delete(model, "base_url")
	rawKey, hasKey := model["api_key"]
	if hasKey {
		var apiKey string
		if err := json.Unmarshal(rawKey, &apiKey); err == nil {
			model["api_key"] = json.RawMessage("null")
		}
		if strings.TrimSpace(apiKey) != "" {
			provider := current.Model.Provider
			if rawProvider, ok := model["provider"]; ok {
				_ = json.Unmarshal(rawProvider, &provider)
			}
			if strings.TrimSpace(provider) != "" {
				if err := addLegacyModelProviderCredential(patch, current, provider, apiKey); err != nil {
					return err
				}
			}
		}
	}
	if len(model) == 0 {
		delete(patch, "model")
	} else {
		encoded, err := json.Marshal(model)
		if err != nil {
			return err
		}
		patch["model"] = encoded
	}
	return nil
}

func addLegacyModelProviderCredential(patch map[string]json.RawMessage, current agent.Config, provider, apiKey string) error {
	var records map[string]json.RawMessage
	if raw, ok := patch["model_providers"]; ok {
		if err := json.Unmarshal(raw, &records); err != nil {
			return fmt.Errorf("model_providers patch must be an object: %w", err)
		}
		if records == nil {
			return fmt.Errorf("model_providers patch must be an object")
		}
	}
	if records == nil {
		records = make(map[string]json.RawMessage)
	}
	var record map[string]json.RawMessage
	if raw, ok := records[provider]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		_ = json.Unmarshal(raw, &record)
	}
	if record == nil {
		record = make(map[string]json.RawMessage)
		if existing, ok := current.ModelProviders[provider]; ok {
			typeJSON, _ := json.Marshal(existing.Type)
			record["type"] = typeJSON
		} else {
			typeJSON, _ := json.Marshal(provider)
			record["type"] = typeJSON
		}
	}
	keyJSON, _ := json.Marshal(apiKey)
	record["api_key"] = keyJSON
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return err
	}
	records[provider] = encodedRecord
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		return err
	}
	patch["model_providers"] = encodedRecords
	return nil
}

func takeProviderRenames(patch map[string]json.RawMessage) (providerRenames, error) {
	raw, exists := patch["_provider_renames"]
	if !exists {
		return nil, nil
	}
	delete(patch, "_provider_renames")
	var sections map[string]map[string]string
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("_provider_renames must be an object: %w", err)
	}
	if sections == nil {
		return nil, fmt.Errorf("_provider_renames must be an object")
	}
	for section, renames := range sections {
		if section != "model_providers" && section != "tts_providers" && section != "stt_providers" {
			return nil, fmt.Errorf("_provider_renames has unsupported section %s", section)
		}
		if renames == nil {
			return nil, fmt.Errorf("_provider_renames.%s must be an object", section)
		}
		oldNames := make(map[string]struct{}, len(renames))
		for newName, oldName := range renames {
			if strings.TrimSpace(newName) == "" || strings.TrimSpace(oldName) == "" || newName == oldName {
				return nil, fmt.Errorf("invalid provider rename in %s", section)
			}
			if _, exists := oldNames[oldName]; exists {
				return nil, fmt.Errorf("provider rename source %s.%s is used more than once", section, oldName)
			}
			oldNames[oldName] = struct{}{}
		}
	}
	return sections, nil
}

func restoreRenamedProviderCredentials(patch map[string]json.RawMessage, renames providerRenames, current agent.Config) error {
	for section, sectionRenames := range renames {
		rawSection, ok := patch[section]
		if !ok {
			return fmt.Errorf("provider rename in %s requires a provider patch", section)
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(rawSection, &records); err != nil {
			return fmt.Errorf("%s provider patch must be an object: %w", section, err)
		}
		for newName, oldName := range sectionRenames {
			if !providerRecordExists(current, section, oldName) {
				return fmt.Errorf("provider rename source %s.%s does not exist", section, oldName)
			}
			if providerRecordExists(current, section, newName) {
				return fmt.Errorf("provider rename target %s.%s already exists", section, newName)
			}
			oldRaw, oldDeleted := records[oldName]
			if !oldDeleted || !bytes.Equal(bytes.TrimSpace(oldRaw), []byte("null")) {
				return fmt.Errorf("provider rename in %s must delete %s", section, oldName)
			}
			newRaw, newExists := records[newName]
			if !newExists || bytes.Equal(bytes.TrimSpace(newRaw), []byte("null")) {
				return fmt.Errorf("provider rename in %s must create %s", section, newName)
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(newRaw, &record); err != nil {
				return fmt.Errorf("provider rename target %s.%s must be an object: %w", section, newName, err)
			}
			for key, value := range providerCredentialValues(current, section, oldName) {
				if value == "" {
					continue
				}
				if submitted, ok := record[key]; ok && !credentialNeedsPreservation(submitted, value) {
					continue
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				record[key] = encoded
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[newName] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}
	return nil
}

func providerRecordExists(config agent.Config, section, name string) bool {
	switch section {
	case "model_providers":
		_, ok := config.ModelProviders[name]
		return ok
	case "tts_providers":
		_, ok := config.TTSProviders[name]
		return ok
	case "stt_providers":
		_, ok := config.STTProviders[name]
		return ok
	default:
		return false
	}
}

func preserveProviderCredentials(patch map[string]json.RawMessage, current agent.Config) error {
	for _, section := range []string{"model_providers", "tts_providers", "stt_providers"} {
		rawSection, ok := patch[section]
		if !ok {
			continue
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(rawSection, &records); err != nil {
			return fmt.Errorf("%s provider patch must be an object: %w", section, err)
		}
		for name, rawRecord := range records {
			if bytes.Equal(bytes.TrimSpace(rawRecord), []byte("null")) {
				continue
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(rawRecord, &record); err != nil {
				continue
			}
			for key, previous := range providerCredentialValues(current, section, name) {
				if previous == "" {
					continue
				}
				submitted, exists := record[key]
				if !exists || !credentialNeedsPreservation(submitted, previous) {
					continue
				}
				encoded, err := json.Marshal(previous)
				if err != nil {
					return err
				}
				record[key] = encoded
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[name] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}
	return nil
}

func credentialNeedsPreservation(raw json.RawMessage, previous string) bool {
	var submitted string
	if json.Unmarshal(raw, &submitted) != nil {
		return false
	}
	return strings.TrimSpace(submitted) == "" || submitted == maskCredential(previous)
}

func maskCredential(value string) string {
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-4:]
}

func providerCredentialValues(config agent.Config, section, name string) map[string]string {
	switch section {
	case "model_providers":
		return map[string]string{"api_key": config.ModelProviders[name].APIKey}
	case "tts_providers":
		return map[string]string{"api_key": config.TTSProviders[name].APIKey}
	case "stt_providers":
		provider := config.STTProviders[name]
		return map[string]string{
			"api_key":    provider.APIKey,
			"secret_id":  provider.SecretID,
			"secret_key": provider.SecretKey,
		}
	default:
		return nil
	}
}

func filterNoopWebConfigPatch(patch map[string]json.RawMessage, current webConfigDTO) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, err
	}
	return filterNoopObject(patch, values), nil
}

func filterNoopObject(patch, current map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage)
	for key, raw := range patch {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			result[key] = raw
			continue
		}
		var child map[string]json.RawMessage
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &child) == nil {
			var currentChild map[string]json.RawMessage
			if currentRaw, ok := current[key]; ok && json.Unmarshal(currentRaw, &currentChild) == nil {
				filtered := filterNoopObject(child, currentChild)
				if len(filtered) == 0 {
					continue
				}
				encoded, _ := json.Marshal(filtered)
				result[key] = encoded
				continue
			}
		}
		if existing, ok := current[key]; ok && jsonValuesEqual(raw, existing) {
			continue
		}
		result[key] = raw
	}
	return result
}

func jsonValuesEqual(left, right json.RawMessage) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func configPatchOperations(patch map[string]json.RawMessage) ([]configdoc.Operation, error) {
	if err := validateWebConfigPatch(patch); err != nil {
		return nil, err
	}
	var operations []configdoc.Operation
	sections := make([]string, 0, len(patch))
	for key := range patch {
		sections = append(sections, key)
	}
	sort.Strings(sections)
	for _, section := range sections {
		if err := flattenConfigPatch([]string{section}, patch[section], &operations); err != nil {
			return nil, err
		}
	}
	return operations, nil
}

func flattenConfigPatch(path []string, raw json.RawMessage, operations *[]configdoc.Operation) error {
	path = tomlPathForWebPath(path)
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if len(path) >= 2 && strings.HasSuffix(path[0], "_providers") && len(path) == 2 {
			*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), DeleteTable: true})
		} else {
			*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), Delete: true})
		}
		return nil
	}
	var object map[string]json.RawMessage
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := flattenConfigPatch(append(append([]string(nil), path...), key), object[key], operations); err != nil {
				return err
			}
		}
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid patch at %s: %w", strings.Join(path, "."), err)
	}
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return fmt.Errorf("invalid patch at %s: %w", strings.Join(path, "."), err)
	}
	*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), Value: normalized})
	return nil
}

func validateWebConfigPatch(patch map[string]json.RawMessage) error {
	return validatePatchObject(reflect.TypeOf(webConfigDTO{}), patch, nil)
}

func validatePatchObject(typ reflect.Type, patch map[string]json.RawMessage, path []string) error {
	if typ.Kind() == reflect.Map {
		elementType := typ.Elem()
		for elementType.Kind() == reflect.Pointer {
			elementType = elementType.Elem()
		}
		for key, raw := range patch {
			entryPath := append(path, key)
			if isProviderMapPath(path) && !isBareTOMLKey(key) {
				return fmt.Errorf("invalid %s name %q: expected bare TOML key", strings.Join(path, "."), key)
			}
			if isCodeTTLMapPath(path) && !isBareTOMLKey(key) {
				return fmt.Errorf("%s: expected bare TOML key", strings.Join(entryPath, "."))
			}
			if strings.HasPrefix(key, "has_") {
				return fmt.Errorf("%s is a read-only status field", strings.Join(entryPath, "."))
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			if elementType.Kind() != reflect.Struct && elementType.Kind() != reflect.Map {
				if err := validateJSONScalarType(raw, elementType, entryPath); err != nil {
					return err
				}
				continue
			}
			var child map[string]json.RawMessage
			if err := json.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			if err := validatePatchObject(elementType, child, append(path, key)); err != nil {
				return err
			}
		}
		return nil
	}
	for key, raw := range patch {
		if len(path) == 0 && key == "providers" {
			return fmt.Errorf("agent config field providers is unsupported; use model_providers")
		}
		if strings.HasPrefix(key, "has_") {
			return fmt.Errorf("%s is a read-only status field", strings.Join(append(path, key), "."))
		}
		fieldType, found := jsonFieldType(typ, key)
		if !found {
			return fmt.Errorf("unknown config field %s", strings.Join(append(path, key), "."))
		}
		base := fieldType
		for base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if base.Kind() == reflect.Struct || base.Kind() == reflect.Map {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			continue
		}
		if base.Kind() == reflect.Struct || base.Kind() == reflect.Map {
			var child map[string]json.RawMessage
			if err := json.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			if err := validatePatchObject(base, child, append(path, key)); err != nil {
				return err
			}
		} else if err := validateJSONScalarType(raw, base, append(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONScalarType(raw json.RawMessage, typ reflect.Type, path []string) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%s: invalid JSON value", strings.Join(path, "."))
	}
	if isNonNegativeIntegerPath(path) {
		number, ok := value.(json.Number)
		integer, err := number.Int64()
		if !ok || err != nil || integer < 0 {
			return fmt.Errorf("%s: expected non-negative integer", strings.Join(path, "."))
		}
	}
	expected := "value"
	valid := false
	switch typ.Kind() {
	case reflect.String:
		expected = "string"
		_, valid = value.(string)
	case reflect.Bool:
		expected = "bool"
		_, valid = value.(bool)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		expected = "number"
		number, ok := value.(json.Number)
		if !ok {
			break
		}
		if err := validateIntegerNumber(number, typ); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(path, "."), err)
		}
		valid = true
	case reflect.Float32, reflect.Float64:
		expected = "number"
		number, ok := value.(json.Number)
		if !ok {
			break
		}
		parsed, err := strconv.ParseFloat(string(number), typ.Bits())
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return fmt.Errorf("%s: number is out of range", strings.Join(path, "."))
		}
		valid = true
	case reflect.Slice, reflect.Array:
		expected = "array"
		_, valid = value.([]any)
	}
	if !valid {
		return fmt.Errorf("%s: expected %s, got %s", strings.Join(path, "."), expected, jsonValueType(value))
	}
	return nil
}

func validateIntegerNumber(number json.Number, typ reflect.Type) error {
	text := string(number)
	if strings.ContainsAny(text, ".eE") {
		return fmt.Errorf("expected integer")
	}
	if typ.Kind() >= reflect.Uint && typ.Kind() <= reflect.Uint64 {
		if _, err := strconv.ParseUint(text, 10, typ.Bits()); err != nil {
			return fmt.Errorf("integer is out of range")
		}
		return nil
	}
	if _, err := strconv.ParseInt(text, 10, typ.Bits()); err != nil {
		return fmt.Errorf("integer is out of range")
	}
	return nil
}

func isNonNegativeIntegerPath(path []string) bool {
	joined := strings.Join(path, ".")
	if joined == "voice_notifications.max_pending" ||
		joined == "voice_notifications.response_tail.max_items" ||
		joined == "voice_notifications.response_tail.max_text_chars" ||
		joined == "voice_notifications.expiration.default_ttl_seconds" {
		return true
	}
	return len(path) == 4 && path[0] == "voice_notifications" &&
		path[1] == "expiration" && path[2] == "code_ttl_seconds"
}

func jsonValueType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return "value"
	}
}

func isProviderMapPath(path []string) bool {
	return len(path) == 1 && (path[0] == "model_providers" || path[0] == "tts_providers" || path[0] == "stt_providers")
}

func isCodeTTLMapPath(path []string) bool {
	return len(path) == 3 && path[0] == "voice_notifications" && path[1] == "expiration" && path[2] == "code_ttl_seconds"
}

func isBareTOMLKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func jsonFieldType(typ reflect.Type, name string) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == name {
			return field.Type, true
		}
	}
	return nil, false
}

func tomlPathForWebPath(path []string) []string {
	if len(path) < 2 || path[0] != "agent" {
		return path
	}
	return append([]string{path[1]}, path[2:]...)
}

func normalizeJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			f, err := typed.Float64()
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, fmt.Errorf("number is out of range")
			}
			return f, nil
		}
		if i, err := typed.Int64(); err == nil {
			return i, nil
		}
		if u, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("integer is out of range")
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func requiresConfigReboot(paths []string) bool {
	for _, path := range paths {
		if path == "device.device_type" || path == "hid.keyboard_layout" {
			return true
		}
	}
	return false
}

type configTestInput struct {
	Section     string          `json:"section"`
	Values      json.RawMessage `json:"values"`
	Text        string          `json:"text"`
	AudioBase64 string          `json:"audio_base64"`
}

// runConfigTest implements provider checks through the same runtime registries
// and adapters used by the agent itself.
func runConfigTest(args []string) int {
	fs := flag.NewFlagSet("config-test", flag.ExitOnError)
	formatFlag := fs.String("format", "json", "output format (only json supported)")
	stdinFlag := fs.Bool("stdin", false, "read test request from stdin")
	sectionFlag := fs.String("section", "", "config section to test")
	configFlag := fs.String("config", "", "path to a TOML config file")
	timeoutFlag := fs.Duration("timeout", 45*time.Second, "test timeout")

	if err := fs.Parse(args); err != nil {
		writeConfigTestResult(configTestFailure("request", "failed to parse flags: "+err.Error()))
		return 1
	}
	if *formatFlag != "json" {
		writeConfigTestResult(configTestFailure("request", "only --format=json is supported"))
		return 1
	}
	if !*stdinFlag {
		writeConfigTestResult(configTestFailure("request", "--stdin flag is required"))
		return 1
	}
	if strings.TrimSpace(*configFlag) == "" {
		writeConfigTestResult(configTestFailure("request", "--config is required"))
		return 1
	}

	var input configTestInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		writeConfigTestResult(configTestFailure("request", "invalid JSON input: "+err.Error()))
		return 1
	}
	section := strings.TrimSpace(input.Section)
	if section == "" {
		section = strings.TrimSpace(*sectionFlag)
	}
	if section != "model" && section != "tts" && section != "stt" {
		writeConfigTestResult(configTestFailure("request", "unsupported section: "+section))
		return 1
	}

	cfg, err := agent.LoadResolvedConfig(*configFlag)
	if err != nil {
		writeConfigTestResult(configTestFailure("load_config", err.Error()))
		return 1
	}

	if len(input.Values) == 0 || string(input.Values) == "null" {
		writeConfigTestResult(configTestFailure("request", "missing values object"))
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()
	result := executeConfigTest(ctx, cfg, input, section)
	writeConfigTestResult(result)
	if result.OK {
		return 0
	}
	return 1
}

func executeConfigTest(ctx context.Context, cfg agent.Config, input configTestInput, section string) ConfigTestResult {
	if section == "model" {
		var modelValues modelDTO
		if err := json.Unmarshal(input.Values, &modelValues); err != nil {
			return configTestFailure("request", "invalid model values: "+err.Error())
		}

		result, err := agent.RunModelProviderTest(ctx, cfg, modelValues.providerTestRequest())
		if err != nil {
			detail := err.Error()
			if result.Provider != "" {
				detail = fmt.Sprintf("[provider=%s model=%s] %s", result.Provider, result.Model, detail)
			}
			return configTestFailure("provider_request", detail)
		}
		return ConfigTestResult{
			OK: true,
			Results: []ConfigTestCheck{{
				Check:  "provider_request",
				Passed: true,
				Detail: fmt.Sprintf("received a response from %s (model: %s)", result.Provider, result.Model),
			}},
		}
	}

	if section == "tts" {
		var ttsValues ttsDTO
		if err := json.Unmarshal(input.Values, &ttsValues); err != nil {
			return configTestFailure("request", "invalid tts values: "+err.Error())
		}

		playback, err := agent.RunTTSPlaybackTest(ctx, cfg, ttsValues.playbackTestRequest(input.Text))
		if err != nil {
			detail := err.Error()
			if ttsValues.Provider != "" {
				detail = fmt.Sprintf("[provider=%s model=%s voice=%s] %s", ttsValues.Provider, ttsValues.Model, ttsValues.VoiceID, detail)
			}
			return configTestFailure("tts_playback", detail)
		}
		return ConfigTestResult{
			OK: true,
			Results: []ConfigTestCheck{{
				Check:  "tts_playback",
				Passed: true,
				Detail: fmt.Sprintf("played %q with %s", playback.Text, playback.Provider),
			}},
		}
	}
	if section != "stt" {
		return configTestFailure("request", "unsupported section: "+section)
	}

	var sttValues sttDTO
	if err := json.Unmarshal(input.Values, &sttValues); err != nil {
		return configTestFailure("request", "invalid stt values: "+err.Error())
	}
	if strings.TrimSpace(input.AudioBase64) == "" {
		result, err := agent.RunSTTProviderTest(ctx, cfg, sttValues.transcriptionTestRequest(nil))
		if err != nil {
			return configTestFailure("provider_config", err.Error())
		}
		return ConfigTestResult{
			OK: true,
			Results: []ConfigTestCheck{{
				Check:  "provider_config",
				Passed: true,
				Detail: fmt.Sprintf("created the %s STT client", result.Provider),
			}},
		}
	}
	wavData, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input.AudioBase64))
	if err != nil {
		return configTestFailure("request", "invalid audio_base64: "+err.Error())
	}
	transcription, err := agent.RunSTTTranscriptionTest(ctx, cfg, sttValues.transcriptionTestRequest(wavData))
	if err != nil {
		return configTestFailure("stt_transcription", err.Error())
	}
	return ConfigTestResult{
		OK:         true,
		Transcript: transcription.Transcript,
		Results: []ConfigTestCheck{{
			Check:  "stt_transcription",
			Passed: true,
			Detail: fmt.Sprintf("transcribed audio with %s", transcription.Provider),
		}},
	}
}

func configTestFailure(check, detail string) ConfigTestResult {
	return ConfigTestResult{
		OK: false,
		Results: []ConfigTestCheck{{
			Check:  check,
			Passed: false,
			Detail: detail,
		}},
	}
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
	} else if strings.Contains(errMsg, "model.api_mode") {
		field = "model.api_mode"
	} else if strings.Contains(errMsg, "model.max_response_tokens") {
		field = "model.max_response_tokens"
	} else if strings.Contains(errMsg, "model.context_window") {
		field = "model.context_window"
	} else if strings.Contains(errMsg, "model.model_max_output_tokens") {
		field = "model.model_max_output_tokens"
	} else if strings.Contains(errMsg, "model.model") {
		field = "model.model"
	} else if strings.Contains(errMsg, "device.device_type") {
		field = "device.device_type"
	} else if strings.Contains(errMsg, "stt.provider") {
		field = "stt.provider"
	} else if strings.Contains(errMsg, "tts.provider") {
		field = "tts.provider"
	} else if strings.Contains(errMsg, "input_mode") {
		field = "input_mode"
	} else if strings.Contains(errMsg, "hid.keyboard_layout") || strings.Contains(errMsg, "keyboard_layout") {
		field = "hid.keyboard_layout"
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
	} else if strings.Contains(errMsg, "audio.playback_backend") {
		field = "audio.playback_backend"
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

func writeConfigTestResult(result ConfigTestResult) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(result)
}
