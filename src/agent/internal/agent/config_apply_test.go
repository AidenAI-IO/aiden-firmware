package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForConfigApplied(t *testing.T, r *Runtime) ConfigApplyStatus {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status := r.ConfigApplyStatus()
		if !status.Pending {
			if status.Error != "" {
				t.Fatalf("apply failed: %s", status.Error)
			}
			return status
		}
		select {
		case <-deadline:
			t.Fatal("config application did not finish")
		case <-ticker.C:
		}
	}
}

func TestConfigApplyWaitsForRunAndKeepsRuntimeIdentity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	r := &Runtime{config: cfg, models: NewModelManager(cfg.Model, ProxyConfig{})}
	defer r.Close()
	unlock, err := r.lockRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := cfg
	next.MaxIterations = 12
	status := r.QueueConfig(next, 1)
	if !status.Pending {
		t.Fatal("save did not queue")
	}
	if r.ConfigSnapshot().MaxIterations == 12 {
		t.Fatal("changed a running task")
	}
	identity := r.models
	unlock()
	status = waitForConfigApplied(t, r)
	if !status.Applied || status.AppliedRevision != 1 || r.ConfigSnapshot().MaxIterations != 12 || r.models != identity {
		t.Fatalf("unexpected state %+v", status)
	}
}

func TestConfigApplyPreservesUSBIdentityUntilReboot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	r := &Runtime{config: cfg}
	defer r.Close()
	next := cfg
	next.Device.DeviceType = "Android"
	next.HID.KeyboardLayout = "azerty"
	next.MaxIterations = 21
	r.QueueConfig(next, 2)
	status := waitForConfigApplied(t, r)
	active := r.ConfigSnapshot()
	if !status.RebootRequired || status.Applied || status.AppliedRevision == 2 || active.Device != cfg.Device || active.HID.KeyboardLayout != cfg.HID.KeyboardLayout || active.MaxIterations != 21 {
		t.Fatalf("USB state changed: %+v", status)
	}
	// A later ordinary save must keep the pending reboot visible.
	next.MaxIterations = 22
	r.QueueConfig(next, 3)
	if !waitForConfigApplied(t, r).RebootRequired {
		t.Fatal("lost pending reboot")
	}
	next = cfg
	next.MaxIterations = 23
	r.QueueConfig(next, 4)
	if waitForConfigApplied(t, r).RebootRequired {
		t.Fatal("reverting USB settings should clear reboot")
	}
}

func TestConfigApplyFailureResumesOldComponents(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	r := &Runtime{config: cfg}
	defer r.Close()
	var committed bool
	r.SetConfigPreparer(func(context.Context, Config) (func(bool), error) { return func(ok bool) { committed = ok }, nil })
	next := cfg
	next.TTS.Provider = "not-a-provider"
	if err := r.ApplyConfigSnapshot(next); err == nil {
		t.Fatal("invalid provider accepted")
	}
	if committed || r.ConfigSnapshot().TTS.Provider != cfg.TTS.Provider {
		t.Fatal("failed apply replaced active settings")
	}
}

func TestConfigApplyCoalescesPendingRevisions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	r := &Runtime{config: cfg}
	defer r.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.SetConfigPreparer(func(context.Context, Config) (func(bool), error) {
		once.Do(func() { close(entered); <-release })
		return nil, nil
	})
	next := cfg
	next.MaxIterations = 1
	r.QueueConfig(next, 1)
	<-entered
	next.MaxIterations = 2
	r.QueueConfig(next, 2)
	next.MaxIterations = 3
	r.QueueConfig(next, 3)
	close(release)
	status := waitForConfigApplied(t, r)
	if status.AppliedRevision != 3 || r.ConfigSnapshot().MaxIterations != 3 {
		t.Fatalf("lost latest revision %+v", status)
	}
}

func TestConfigApplyPreparationFailureLeavesSnapshotActive(t *testing.T) {
	cfg := DefaultConfig()
	r := &Runtime{config: cfg}
	r.SetConfigPreparer(func(context.Context, Config) (func(bool), error) { return nil, errors.New("cannot open audio") })
	next := cfg
	next.Audio.Socket = "/new.sock"
	if err := r.ApplyConfigSnapshot(next); err == nil {
		t.Fatal("expected prepare failure")
	}
	if r.ConfigSnapshot().Audio.Socket != cfg.Audio.Socket {
		t.Fatal("snapshot changed before prepare succeeded")
	}
}

func TestModelConfigReplacementUpdatesExistingConsumers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.Provider = "fake"
	cfg.Model.Model = "first"
	manager := NewModelManager(cfg.Model, ProxyConfig{})
	before := manager.Spec()
	next := cfg.Model
	next.Model = "second"
	next.ContextWindow = 8192
	manager.replaceConfig(next, t.TempDir())
	after := manager.Spec()
	if before.Name == after.Name || after.Name != "second" || after.ContextWindow != 8192 {
		t.Fatalf("stale model spec: %+v", after)
	}
}

func TestConfigApplyToolSwapWaitsForOperationAndPreservesState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	r, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	old := r.toolSnapshot()
	old.screen.Update(640, 480)
	old.screen.UpdateScreenshot([]byte("jpeg"), 640, 480)
	shell, _ := old.Get("shell")
	r.configOperations.RLock()
	next := r.ConfigSnapshot()
	next.HID.FrameSocket = "/new-frame.sock"
	r.QueueConfig(next, 10)
	time.Sleep(15 * time.Millisecond)
	if r.toolSnapshot() != old {
		t.Fatal("retired tools during an operation")
	}
	r.configOperations.RUnlock()
	waitForConfigApplied(t, r)
	current := r.toolSnapshot()
	if current == old {
		t.Fatal("tools were not replaced")
	}
	if got, _ := current.Get("shell"); got != shell {
		t.Fatal("replaced persistent shell tool")
	}
	if _, _, ok := current.screen.Dimensions(); ok {
		t.Fatal("kept old capture calibration")
	}
	if _, _, _, _, ok := current.screen.LatestScreenshot(0); ok {
		t.Fatal("kept retired screenshot")
	}
	if current.phoneBridge != r.phoneBridge {
		t.Fatal("lost Phone Bridge identity")
	}
}

func TestModelConfigReplacementKeepsInflightRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body.Model == "first" {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": body.Model}}}})
	}))
	defer provider.Close()
	cfg := ModelConfig{Provider: "openai", Model: "first", APIKey: "test", BaseURL: provider.URL + "/v1", APIMode: "chat_completions", ContextWindow: 8192, ModelMaxOutputTokens: 1024}
	manager := NewModelManager(cfg, ProxyConfig{})
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() { value, err := manager.Call(context.Background(), "hello"); done <- result{value, err} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request never arrived")
	}
	cfg.Model = "second"
	commit, err := manager.prepareReplacement(cfg, t.TempDir())
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	commit()
	value, err := manager.Call(context.Background(), "hello")
	close(release)
	if err != nil || !strings.Contains(value, "second") {
		t.Fatalf("new request=%q %v", value, err)
	}
	previous := <-done
	if previous.err != nil || !strings.Contains(previous.text, "first") {
		t.Fatalf("inflight request=%+v", previous)
	}
}

func TestConfigApplyTogglesLiveActivityAndNotifications(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigDir = t.TempDir()
	cfg.Model.Provider = "fake"
	cfg.LiveActivity.Enabled = new(bool)
	r, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s := NewServer(r, ":0")
	defer s.Close()
	r.SetConfigPreparer(s.PrepareConfig)
	identity := s.liveActivity
	if identity.StartTask("disabled", "test") != nil {
		t.Fatal("disabled activity started")
	}
	next := r.ConfigSnapshot()
	next.LiveActivity.Enabled = nil
	next.VoiceNotifications.Enabled = new(bool)
	if err := r.ApplyConfigSnapshot(next); err != nil {
		t.Fatal(err)
	}
	if identity != s.liveActivity || identity.StartTask("enabled", "test") == nil {
		t.Fatal("activity did not enable online")
	}
	event := VoiceNotificationEvent{Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device"}
	if err := r.voiceNotifications.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if r.voiceNotifications.PrepareNotification(context.Background()).Text != "" {
		t.Fatal("disabled notifications delivered")
	}
	next.VoiceNotifications.Enabled = nil
	if err := r.ApplyConfigSnapshot(next); err != nil {
		t.Fatal(err)
	}
	if err := r.voiceNotifications.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if r.voiceNotifications.PrepareNotification(context.Background()).Text == "" {
		t.Fatal("notifications did not enable online")
	}
}

// TestConfigApplyWorkerLogsOutcome keeps online application observable in
// agent.log: a successful reload used to leave no trace there, so a field
// report of "the setting did not take effect" could not be told apart from a
// save that never reached the runtime.
func TestConfigApplyWorkerLogsOutcome(t *testing.T) {
	// The worker publishes the status before it logs, so the log assertion
	// joins the goroutine instead of polling the buffer.
	settle := func(r *Runtime) ConfigApplyStatus {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			status := r.ConfigApplyStatus()
			if !status.Pending {
				return status
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("config application did not finish")
		return ConfigApplyStatus{}
	}

	t.Run("applied", func(t *testing.T) {
		var output bytes.Buffer
		r := &Runtime{config: Config{ConfigDir: t.TempDir()}}
		r.logger = &Logger{logger: log.New(&output, "", 0)}
		next := r.ConfigSnapshot()
		next.Locale = "zh-CN"
		r.QueueConfig(next, 987654321)
		waitForConfigApplied(t, r)
		r.StopConfigReloads()
		for _, want := range []string{"[INFO] [agent] [runtime] config_applied", "revision=987654321", "reboot_required=false"} {
			if line := output.String(); !strings.Contains(line, want) {
				t.Fatalf("log %q missing %q", line, want)
			}
		}
	})

	t.Run("failed", func(t *testing.T) {
		var output bytes.Buffer
		r := &Runtime{config: Config{ConfigDir: t.TempDir()}}
		r.logger = &Logger{logger: log.New(&output, "", 0)}
		next := r.ConfigSnapshot()
		next.ConfigDir = t.TempDir()
		r.QueueConfig(next, 42)
		if status := settle(r); status.State != "failed" {
			t.Fatalf("status=%+v", status)
		}
		r.StopConfigReloads()
		for _, want := range []string{"[WARN] [agent] [runtime] config_apply_failed", "revision=42", "config directory cannot be reloaded"} {
			if line := output.String(); !strings.Contains(line, want) {
				t.Fatalf("log %q missing %q", line, want)
			}
		}
	})
}
