package agent

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidateAcceptsAudioWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax"},
		InputMode:   " audio ",
		TriggerMode: " wakeup ",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.InputModeOrDefault(); got != "audio" {
		t.Fatalf("InputModeOrDefault() = %q, want audio", got)
	}
	if got := cfg.TriggerModeOrDefault(); got != "wakeup" {
		t.Fatalf("TriggerModeOrDefault() = %q, want wakeup", got)
	}
}

func TestConfigValidateRejectsInvalidTriggerMode(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax"},
		InputMode:   "audio",
		TriggerMode: "gpio",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid trigger_mode error")
	}
	if !strings.Contains(err.Error(), "invalid trigger_mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidateRejectsWakeupForTextInput(t *testing.T) {
	tests := []struct {
		name      string
		inputMode string
	}{
		{name: "default text", inputMode: ""},
		{name: "explicit text", inputMode: " text "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Model:       ModelConfig{Provider: "fake"},
				InputMode:   tt.inputMode,
				TriggerMode: " wakeup ",
			}

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected incompatible trigger_mode/input_mode error")
			}
			if !strings.Contains(err.Error(), "incompatible trigger_mode") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigValidateRequiresTTSForAudioWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "   "},
		InputMode:   "audio",
		TriggerMode: "wakeup",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected missing TTS provider error")
	}
	if !strings.Contains(err.Error(), "tts.provider is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidateRejectsUnsupportedAudioFormatForVoiceInput(t *testing.T) {
	tests := []struct {
		name    string
		audio   AudioConfig
		wantErr string
	}{
		{
			name:    "stereo",
			audio:   AudioConfig{Channels: 2},
			wantErr: "audio.channels must be 1",
		},
		{
			name:    "eight bit",
			audio:   AudioConfig{BitWidth: 8},
			wantErr: "audio.bit_width must be 16",
		},
		{
			name:    "too low sample rate",
			audio:   AudioConfig{SampleRate: 1},
			wantErr: "audio.sample_rate must be at least 8000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Model:       ModelConfig{Provider: "fake"},
				TTS:         TTSConfig{Provider: "minimax"},
				Audio:       tt.audio,
				InputMode:   "audio",
				TriggerMode: "wakeup",
			}

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected audio format validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestVoiceSessionConfigDefaults(t *testing.T) {
	cfg := Config{}

	if !cfg.VoiceSessionEnabledOrDefault() {
		t.Fatal("VoiceSessionEnabledOrDefault() = false, want true")
	}
	if cfg.VoiceFirstTurnTimeoutOrDefault() != 10*time.Second {
		t.Fatalf("VoiceFirstTurnTimeoutOrDefault() = %s, want 10s", cfg.VoiceFirstTurnTimeoutOrDefault())
	}
	if cfg.VoiceFollowupTimeoutOrDefault() != 6*time.Second {
		t.Fatalf("VoiceFollowupTimeoutOrDefault() = %s, want 6s", cfg.VoiceFollowupTimeoutOrDefault())
	}
	if !cfg.VoiceInterruptOnWakeupOrDefault() {
		t.Fatal("VoiceInterruptOnWakeupOrDefault() = false, want true")
	}
	if cfg.VoiceInterruptListenDuringTTSOrDefault() {
		t.Fatal("VoiceInterruptListenDuringTTSOrDefault() = true, want false")
	}
	if !cfg.VoiceStreamingTTSEnabledOrDefault() {
		t.Fatal("VoiceStreamingTTSEnabledOrDefault() = false, want true")
	}
	if !cfg.VoiceToolCallSpeechOrDefault() {
		t.Fatal("VoiceToolCallSpeechOrDefault() = false, want true")
	}
	if cfg.VoiceMaxResponseTokensOrDefault() != 400 {
		t.Fatalf("VoiceMaxResponseTokensOrDefault() = %d, want 400", cfg.VoiceMaxResponseTokensOrDefault())
	}
}

func TestVoiceSessionConfigOverrides(t *testing.T) {
	disabled := false
	interruptDisabled := false
	listenDuringTTS := true
	streamingDisabled := false
	toolSpeech := false
	cfg := Config{
		VoiceSessionEnabled:           &disabled,
		VoiceFirstTurnTimeoutMs:       1234,
		VoiceFollowupTimeoutMs:        5678,
		VoiceInterruptOnWakeup:        &interruptDisabled,
		VoiceInterruptListenDuringTTS: &listenDuringTTS,
		VoiceStreamingTTSEnabled:      &streamingDisabled,
		VoiceToolCallSpeech:           &toolSpeech,
		VoiceMaxResponseTokens:        123,
	}

	if cfg.VoiceSessionEnabledOrDefault() {
		t.Fatal("VoiceSessionEnabledOrDefault() = true, want false")
	}
	if cfg.VoiceFirstTurnTimeoutOrDefault() != 1234*time.Millisecond {
		t.Fatalf("VoiceFirstTurnTimeoutOrDefault() = %s, want 1234ms", cfg.VoiceFirstTurnTimeoutOrDefault())
	}
	if cfg.VoiceFollowupTimeoutOrDefault() != 5678*time.Millisecond {
		t.Fatalf("VoiceFollowupTimeoutOrDefault() = %s, want 5678ms", cfg.VoiceFollowupTimeoutOrDefault())
	}
	if cfg.VoiceInterruptOnWakeupOrDefault() {
		t.Fatal("VoiceInterruptOnWakeupOrDefault() = true, want false")
	}
	if !cfg.VoiceInterruptListenDuringTTSOrDefault() {
		t.Fatal("VoiceInterruptListenDuringTTSOrDefault() = false, want true")
	}
	if cfg.VoiceStreamingTTSEnabledOrDefault() {
		t.Fatal("VoiceStreamingTTSEnabledOrDefault() = true, want false")
	}
	if cfg.VoiceToolCallSpeechOrDefault() {
		t.Fatal("VoiceToolCallSpeechOrDefault() = true, want false")
	}
	if cfg.VoiceMaxResponseTokensOrDefault() != 123 {
		t.Fatalf("VoiceMaxResponseTokensOrDefault() = %d, want 123", cfg.VoiceMaxResponseTokensOrDefault())
	}
}

func TestVoiceSessionConfigValidationRejectsNegativeValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "negative followup timeout",
			cfg: Config{
				Model:                  ModelConfig{Provider: "fake"},
				VoiceFollowupTimeoutMs: -1,
			},
			want: "voice_followup_timeout_ms must be >= 0",
		},
		{
			name: "negative first turn timeout",
			cfg: Config{
				Model:                   ModelConfig{Provider: "fake"},
				VoiceFirstTurnTimeoutMs: -1,
			},
			want: "voice_first_turn_timeout_ms must be >= 0",
		},
		{
			name: "negative max turns",
			cfg: Config{
				Model:         ModelConfig{Provider: "fake"},
				VoiceMaxTurns: -1,
			},
			want: "voice_max_turns must be >= 0",
		},
		{
			name: "negative voice max response tokens",
			cfg: Config{
				Model:                  ModelConfig{Provider: "fake"},
				VoiceMaxResponseTokens: -1,
			},
			want: "voice_max_response_tokens must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want contains %q", err, tt.want)
			}
		})
	}
}
