package agent

import (
	"path/filepath"
	"testing"
)

func TestAudioDialogInputReplacementUsesEffectiveVADSettings(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Config)
		reuse  bool
	}{
		{"implicit silence", func(c *Config) { c.SilenceMs = 0 }, true},
		{"negative silence", func(c *Config) { c.SilenceMs = -1 }, true},
		{"implicit minimum speech", func(c *Config) { c.MinSpeechMs = 0 }, true},
		{"negative minimum speech", func(c *Config) { c.MinSpeechMs = -1 }, true},
		{"implicit threshold", func(c *Config) { c.VADSpeechThreshold = 0 }, true},
		{"changed silence", func(c *Config) { c.SilenceMs += 100 }, false},
		{"changed minimum speech", func(c *Config) { c.MinSpeechMs += 100 }, false},
		{"changed threshold", func(c *Config) { c.VADSpeechThreshold = 0.7 }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.VADHelperPath = filepath.Join(t.TempDir(), "unavailable-helper")
			newDialog := func(config Config) *AudioDialog {
				t.Helper()
				vad, err := NewAudioVAD(AudioVADConfig{
					SampleRate: config.Audio.SampleRateOrDefault(),
					SilenceMs:  config.SilenceMs, MinSpeechMs: config.MinSpeechMs,
					Backend: config.VADBackend, ModelPath: config.VADModelPath,
					HelperPath: config.VADHelperPath, SpeechThreshold: config.VADSpeechThreshold,
				})
				if err != nil {
					t.Fatal(err)
				}
				dialog := &AudioDialog{config: config, vad: vad}
				t.Cleanup(func() { _ = dialog.Close() })
				return dialog
			}
			previous := newDialog(cfg)
			original := previous.vad
			tt.change(&cfg)
			replacement := newDialog(cfg)
			commit, err := replacement.PrepareInputReplacement(previous)
			if previous.vad != original {
				t.Fatal("preparation changed the original VAD owner")
			}
			if !tt.reuse {
				if err == nil || commit != nil {
					t.Fatal("changed VAD settings must validate the unavailable helper")
				}
				return
			}
			if err != nil || commit == nil {
				t.Fatalf("equivalent settings must reuse VAD without starting a helper: %v", err)
			}
			commit()
			if replacement.vad != original || previous.vad != nil {
				t.Fatal("commit did not transfer VAD ownership")
			}
		})
	}
}
