package main

import (
	"encoding/json"
	"flag"
	"fmt"
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

	// Read config from stdin
	var cfg agent.Config
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&cfg); err != nil {
		writeConfigCheckError("invalid JSON input: " + err.Error())
		return 1
	}

	// Validate config
	result := ValidationResult{
		Valid:  true,
		Errors: []ValidationError{},
	}

	if err := cfg.Validate(); err != nil {
		result.Valid = false
		result.Errors = parseValidationErrors(err)
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
