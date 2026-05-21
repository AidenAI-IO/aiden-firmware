package agent

import (
	"strings"
	"testing"
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
