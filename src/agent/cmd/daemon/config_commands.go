package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/configupdate"
)

type webConfigDTO = configupdate.Config
type modelDTO = configupdate.Model
type modelProviderDTO = configupdate.ModelProvider
type ttsProviderDTO = configupdate.TTSProvider
type sttProviderDTO = configupdate.STTProvider
type ttsDTO = configupdate.TTS
type sttDTO = configupdate.STT
type audioDTO = configupdate.Audio
type audioArchiveDTO = configupdate.AudioArchive
type frameServiceDTO = configupdate.FrameService
type quickCaptureDTO = configupdate.QuickCapture
type storageDTO = configupdate.Storage
type storageDegradedModeDTO = configupdate.StorageDegradedMode
type storageCleanupDTO = configupdate.StorageCleanup
type deviceDTO = configupdate.Device
type voiceNotificationsDTO = configupdate.VoiceNotifications
type voiceNotificationResponseTailDTO = configupdate.VoiceNotificationResponseTail
type voiceNotificationExpirationDTO = configupdate.VoiceNotificationExpiration
type logDTO = configupdate.Log
type otaDTO = configupdate.OTA
type hidDTO = configupdate.HID
type searchDTO = configupdate.Search
type telemetryDTO = configupdate.Telemetry
type liveActivityDTO = configupdate.LiveActivity
type agentDTO = configupdate.Agent

func webConfigDTOFromAgentConfig(cfg agent.Config) webConfigDTO {
	return configupdate.FromAgentConfig(cfg)
}

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
	cfg := dto.ToAgentConfig()

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
	return configupdate.FromAgentConfig(cfg), nil
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
	return runConfigUpdateIO(args, os.Stdin, os.Stdout, os.Stderr)
}

func runConfigUpdateIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config-update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	formatFlag := fs.String("format", "json", "output format (only json supported)")
	stdinFlag := fs.Bool("stdin", false, "read JSON merge patch from stdin")
	configFlag := fs.String("config", "", "path to a TOML config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		writeConfigUpdateError(stdout, err, configupdate.ErrorKindInvalidRequest)
		return 1
	}
	if *formatFlag != "json" || !*stdinFlag || strings.TrimSpace(*configFlag) == "" {
		writeConfigUpdateError(stdout, fmt.Errorf("--config, --stdin and --format=json are required"), configupdate.ErrorKindInvalidRequest)
		return 1
	}
	patch, err := io.ReadAll(stdin)
	if err != nil {
		writeConfigUpdateError(stdout, err, configupdate.ErrorKindInternal)
		return 1
	}
	result, err := configupdate.NewService().Update(strings.TrimSpace(*configFlag), patch)
	if err != nil {
		writeConfigUpdateError(stdout, err, configupdate.ErrorKind(err))
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode config-update result: %v\n", err)
		return 1
	}
	return 0
}

func writeConfigUpdateError(w io.Writer, err error, kind string) {
	_ = json.NewEncoder(w).Encode(configupdate.Result{
		OK:        false,
		Error:     err.Error(),
		ErrorKind: kind,
	})
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

		result, err := agent.RunModelProviderTest(ctx, cfg, modelValues.ProviderTestRequest())
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

		playback, err := agent.RunTTSPlaybackTest(ctx, cfg, ttsValues.PlaybackTestRequest(input.Text))
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
		result, err := agent.RunSTTProviderTest(ctx, cfg, sttValues.TranscriptionTestRequest(nil))
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
	transcription, err := agent.RunSTTTranscriptionTest(ctx, cfg, sttValues.TranscriptionTestRequest(wavData))
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
		// The thinking-budget constraints name the limit they are compared
		// against, so this check must precede those fields to blame the value
		// the user can actually change.
	} else if strings.Contains(errMsg, "model.thinking_budget_tokens") {
		field = "model.thinking_budget_tokens"
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
	} else if strings.Contains(errMsg, "audio.backend") {
		field = "audio.backend"
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
