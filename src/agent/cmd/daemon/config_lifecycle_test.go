package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"aiden-agent/internal/agent"
)

func TestInputLifecycleReloadWaitsForSessionAndKeepsServer(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	cfg.Model.Model = "fake"
	cfg.InputMode = "text"
	cfg.TTS.Provider = ""
	cfg.QuickCapture.Enabled = new(bool)
	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := agent.NewServer(runtime, ":0")
	defer server.Close()
	controller := newInputLifecycle(runtime, server)
	sessionDone := make(chan struct{})
	started := make(chan string, 4)
	controller.newDialog = func(agent.Config) (*agent.AudioDialog, error) { return nil, nil }
	controller.runVoice = func(cfg agent.Config, _ *agent.AudioDialog, shutdown chan os.Signal, stop <-chan struct{}) {
		started <- cfg.InputMode
		select {
		case <-stop:
			if cfg.InputMode == "text" {
				<-sessionDone
			}
		case <-shutdown:
		}
	}
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	<-started
	next := cfg
	next.InputMode = "realtime"
	type result struct {
		finish func(bool)
		err    error
	}
	prepared := make(chan result, 1)
	go func() { finish, err := controller.Prepare(context.Background(), next); prepared <- result{finish, err} }()
	select {
	case <-prepared:
		t.Fatal("reload did not wait for session")
	case <-time.After(15 * time.Millisecond):
	}
	close(sessionDone)
	answer := <-prepared
	if answer.err != nil {
		t.Fatal(answer.err)
	}
	answer.finish(true)
	select {
	case mode := <-started:
		if mode != "realtime" {
			t.Fatalf("mode=%s", mode)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement loop did not start")
	}
	if controller.server != server || controller.runtime != runtime {
		t.Fatal("replaced persistent server or runtime")
	}
}

func TestInputLifecycleFailedReplacementRestoresOldLoop(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	cfg.Model.Model = "fake"
	cfg.InputMode = "text"
	cfg.TTS.Provider = ""
	cfg.QuickCapture.Enabled = new(bool)
	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := agent.NewServer(runtime, ":0")
	defer server.Close()
	controller := newInputLifecycle(runtime, server)
	started := make(chan string, 4)
	controller.newDialog = func(cfg agent.Config) (*agent.AudioDialog, error) {
		if cfg.InputMode == "stt" {
			return nil, errors.New("VAD unavailable")
		}
		return nil, nil
	}
	controller.runVoice = func(cfg agent.Config, _ *agent.AudioDialog, shutdown chan os.Signal, stop <-chan struct{}) {
		started <- cfg.InputMode
		select {
		case <-stop:
		case <-shutdown:
		}
	}
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	<-started
	next := cfg
	next.InputMode = "stt"
	if _, err := controller.Prepare(context.Background(), next); err == nil {
		t.Fatal("expected failed VAD construction")
	}
	if mode := <-started; mode != "text" {
		t.Fatalf("restored %s, want text", mode)
	}
}

func TestInputLifecycleMissingVADHelperKeepsTextLoop(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	cfg.InputMode = "text"
	cfg.TTS.Provider = ""
	cfg.QuickCapture.Enabled = new(bool)
	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	server := agent.NewServer(runtime, ":0")
	defer server.Close()
	controller := newInputLifecycle(runtime, server)
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	next := cfg
	next.InputMode = "stt"
	next.VADHelperPath = "/missing-vad-helper"
	if _, err := controller.Prepare(context.Background(), next); err == nil {
		t.Fatal("missing helper accepted")
	}
	select {
	case <-controller.done:
		t.Fatal("old text loop was not restored")
	default:
	}
	if controller.cfg.InputMode != "text" {
		t.Fatal("failed preparation changed mode")
	}
}
