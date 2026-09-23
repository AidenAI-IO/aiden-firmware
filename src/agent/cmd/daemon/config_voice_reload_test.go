package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"aiden-agent/internal/agent"
)

func TestActiveVoiceConfigIgnoresInactiveProviderSettings(t *testing.T) {
	base := agent.DefaultConfig()
	base.InputMode = "realtime"
	changed := base
	changed.STT.Provider = "openai-whisper"
	changed.TTS.Provider = "fish-audio"
	if !reflect.DeepEqual(activeVoiceConfig(base), activeVoiceConfig(changed)) {
		t.Fatal("inactive STT/TTS settings changed realtime voice lifecycle")
	}

	base.InputMode = "stt"
	changed = base
	changed.VoiceModel.Provider = "gemini"
	if !reflect.DeepEqual(activeVoiceConfig(base), activeVoiceConfig(changed)) {
		t.Fatal("inactive realtime settings changed STT voice lifecycle")
	}

	changed.InputMode = "realtime"
	if reflect.DeepEqual(activeVoiceConfig(base), activeVoiceConfig(changed)) {
		t.Fatal("input mode change did not change active voice lifecycle")
	}
}

// Startup leaves VAD lazy. A provider-only save must not add a new hardware
// prerequisite, even when the driver is unavailable before the first recording.
func TestInputLifecycleProviderReloadDoesNotInitializeUnchangedVAD(t *testing.T) {
	for _, field := range []string{"tts", "stt", "realtime"} {
		t.Run(field, func(t *testing.T) {
			cfg := agent.DefaultConfig()
			cfg.ConfigDir = t.TempDir()
			cfg.Model.Provider, cfg.Model.Model = "fake", "fake"
			cfg.InputMode, cfg.STT.Provider, cfg.TTS.Provider = "stt", "", ""
			cfg.QuickCapture.Enabled = new(bool)
			cfg.VADHelperPath = filepath.Join(t.TempDir(), "vad-unavailable")
			if err := os.WriteFile(cfg.VADHelperPath, []byte("#!/bin/sh\nprintf 'ERR encoder rknn_init failed: -1\\n'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			r, err := agent.NewRuntime(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			s := agent.NewServer(r, ":0")
			defer s.Close()
			c := newInputLifecycle(r, s)
			c.runVoice = func(_ agent.Config, _ *agent.AudioDialog, shutdown chan os.Signal, stop <-chan struct{}) {
				select {
				case <-stop:
				case <-shutdown:
				}
			}
			if err := c.Start(cfg); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			r.SetConfigPreparer(c.Prepare)
			next := cfg
			switch field {
			case "tts":
				next.TTS.Provider, next.TTS.APIKey = "fish-audio", "test-key"
			case "stt":
				next.STT.Provider, next.STT.APIKey = "openai", "test-key"
			case "realtime":
				next.VoiceModel.Provider, next.VoiceModel.APIKey = "gemini", "test-key"
			}
			if err := r.ApplyConfigSnapshot(next); err != nil {
				t.Fatalf("provider-only reload attempted VAD initialization: %v", err)
			}
			if got := r.ConfigSnapshot(); got.TTS != next.TTS || got.STT != next.STT || got.VoiceModel != next.VoiceModel {
				t.Fatal("new provider not published")
			}
		})
	}
}

func TestInputLifecycleInactiveProviderKeepsVoiceLoop(t *testing.T) {
	for _, mode := range []string{"stt", "realtime"} {
		t.Run(mode, func(t *testing.T) {
			cfg := agent.DefaultConfig()
			cfg.ConfigDir = t.TempDir()
			cfg.Model.Provider, cfg.Model.Model = "fake", "fake"
			cfg.InputMode, cfg.TTS.Provider = mode, ""
			cfg.QuickCapture.Enabled = new(bool)
			r, err := agent.NewRuntime(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			s := agent.NewServer(r, ":0")
			defer s.Close()
			c := newInputLifecycle(r, s)
			c.newDialog = func(agent.Config) (*agent.AudioDialog, error) { return nil, nil }
			c.runVoice = func(_ agent.Config, _ *agent.AudioDialog, shutdown chan os.Signal, _ <-chan struct{}) { <-shutdown }
			if err := c.Start(cfg); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			stop := c.stop
			next := cfg
			if mode == "stt" {
				next.VoiceModel.Provider = "gemini"
			} else {
				next.TTS.APIKey = "replacement-key"
				next.STT.Language = "en"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			finish, err := c.Prepare(ctx, next)
			if err != nil {
				t.Fatalf("inactive provider blocked on active conversation: %v", err)
			}
			finish(true)
			select {
			case <-stop:
				t.Fatal("inactive provider drained voice loop")
			default:
			}
		})
	}
}
