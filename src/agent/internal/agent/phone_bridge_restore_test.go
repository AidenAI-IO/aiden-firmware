package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPhoneBridgeRestorerReturnsForegroundFromDynamicIsland(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	bridge.mu.Unlock()

	restorer := NewPhoneBridgeRestorer(bridge, nil)
	restorer.waitTimeout = time.Second
	tapped := false
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		tapped = true
		go func() {
			time.Sleep(10 * time.Millisecond)
			bridge.mu.Lock()
			bridge.connected = true
			bridge.platform = "ios"
			bridge.appState = "active"
			bridge.returnEntry = "none"
			bridge.returnEntrySeen = true
			bridge.returnEntryOK = false
			bridge.lastHeartbeatAt = time.Now()
			bridge.mu.Unlock()
		}()
		return nil
	}

	restored, err := restorer.EnsureForeground(context.Background())
	if err != nil {
		t.Fatalf("EnsureForeground() error = %v", err)
	}
	if !restored {
		t.Fatal("EnsureForeground() restored = false, want true")
	}
	if !tapped {
		t.Fatal("return entry was not tapped")
	}
	if !phoneBridgeReadyForCommand(bridge.Status()) {
		t.Fatalf("bridge status not ready after restore: %+v", bridge.Status())
	}
}

func TestPhoneBridgeRestorerDoesNotTapWithoutReturnEntry(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "none"
	bridge.returnEntryOK = false
	bridge.mu.Unlock()

	restorer := NewPhoneBridgeRestorer(bridge, nil)
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		t.Fatal("tapReturnEntry should not be called without a return entry")
		return nil
	}

	restored, err := restorer.EnsureForeground(context.Background())
	if err == nil {
		t.Fatal("EnsureForeground() error = nil, want missing return entry error")
	}
	if restored {
		t.Fatal("EnsureForeground() restored = true, want false")
	}
	if !strings.Contains(err.Error(), "no supported Dynamic Island return entry") {
		t.Fatalf("EnsureForeground() error = %v, want return entry message", err)
	}
}

func TestPhoneBridgeRestorerDoesNotTapLockScreenLiveActivity(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "live_activity"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	bridge.mu.Unlock()

	restorer := NewPhoneBridgeRestorer(bridge, nil)
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		t.Fatal("tapReturnEntry should not be called for lock-screen Live Activity")
		return nil
	}

	restored, err := restorer.EnsureForeground(context.Background())
	if err == nil {
		t.Fatal("EnsureForeground() error = nil, want unsupported return entry error")
	}
	if restored {
		t.Fatal("EnsureForeground() restored = true, want false")
	}
	if !strings.Contains(err.Error(), "no supported Dynamic Island return entry") {
		t.Fatalf("EnsureForeground() error = %v, want unsupported Dynamic Island message", err)
	}
}

func TestPhoneBridgeReadyForCommandRejectsBackgroundIOS(t *testing.T) {
	if phoneBridgeReadyForCommand(PhoneBridgeStatus{Connected: true, Platform: "ios", AppState: "background"}) {
		t.Fatal("background iOS bridge should not be considered ready for foreground command")
	}
	if !phoneBridgeReadyForCommand(PhoneBridgeStatus{Connected: true, Platform: "ios", AppState: "active"}) {
		t.Fatal("active iOS bridge should be ready")
	}
	if !phoneBridgeReadyForCommand(PhoneBridgeStatus{Connected: true, Platform: "android", AppState: "background"}) {
		t.Fatal("connected non-iOS bridge should remain ready")
	}
}

func TestPhoneBridgeCanUsePiPBackgroundOnlyForSafeDataCommands(t *testing.T) {
	enabled := true
	status := PhoneBridgeStatus{
		Platform:          "ios",
		AppState:          "background",
		AppStateUpdatedAt: ptrTime(time.Now()),
		PipBridgeEnabled:  &enabled,
	}
	if !phoneBridgeCanUsePiPBackground(status, "clipboard_read") {
		t.Fatal("clipboard_read should be allowed in iOS PiP background bridge mode")
	}
	if phoneBridgeCanUsePiPBackground(status, "open_app") {
		t.Fatal("open_app must not be allowed in iOS PiP background bridge mode")
	}

	status.AppState = "active"
	if phoneBridgeCanUsePiPBackground(status, "clipboard_read") {
		t.Fatal("foreground app should use WebSocket path, not PiP background queue")
	}

	disabled := false
	status.AppState = "background"
	status.PipBridgeEnabled = &disabled
	if phoneBridgeCanUsePiPBackground(status, "clipboard_read") {
		t.Fatal("disabled PiP bridge mode should not allow background queue")
	}

	status.PipBridgeEnabled = &enabled
	status.AppStateUpdatedAt = ptrTime(time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second))
	if phoneBridgeCanUsePiPBackground(status, "clipboard_read") {
		t.Fatal("stale PiP bridge status should not allow background queue")
	}
}

func TestPhoneBridgeCanUseFGSBackgroundOnlyForSafeDataCommands(t *testing.T) {
	enabled := true
	status := PhoneBridgeStatus{
		Platform:           "android",
		AppState:           "background",
		AppStateUpdatedAt:  ptrTime(time.Now()),
		FgsBridgeEnabled:   &enabled,
		FgsBridgeUpdatedAt: ptrTime(time.Now()),
	}
	if !phoneBridgeCanUseFGSBackground(status, "clipboard_read") {
		t.Fatal("clipboard_read should be allowed in Android FGS background bridge mode")
	}
	if phoneBridgeCanUseFGSBackground(status, "open_app") {
		t.Fatal("open_app must not be allowed in Android FGS background bridge mode")
	}
	if phoneBridgeToolAvailable(status, toolBridgeOpenApp) {
		t.Fatal("bridge_open_app tool must be hidden while Android FGS background bridge mode is active")
	}
	if !phoneBridgeToolAvailable(status, toolBridgeClipboard) {
		t.Fatal("bridge_clipboard tool should be available through Android FGS background queue")
	}

	status.AppState = "active"
	if phoneBridgeCanUseFGSBackground(status, "clipboard_read") {
		t.Fatal("foreground app should use WebSocket path, not FGS background queue")
	}

	disabled := false
	status.AppState = "background"
	status.FgsBridgeEnabled = &disabled
	if phoneBridgeCanUseFGSBackground(status, "clipboard_read") {
		t.Fatal("disabled FGS bridge mode should not allow background queue")
	}

	status.Platform = "ios"
	status.FgsBridgeEnabled = &enabled
	if phoneBridgeCanUseFGSBackground(status, "clipboard_read") {
		t.Fatal("FGS bridge mode should only apply to Android")
	}

	status.Platform = "android"
	status.FgsBridgeUpdatedAt = ptrTime(time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second))
	if phoneBridgeCanUseFGSBackground(status, "clipboard_read") {
		t.Fatal("stale FGS bridge status should not allow background queue")
	}
}

func TestPhoneBridgeCannotRestoreFromDynamicIslandWhenPiPBackgroundEnabled(t *testing.T) {
	enabled := true
	available := true
	status := PhoneBridgeStatus{
		Platform:             "ios",
		AppState:             "background",
		ReturnEntry:          "dynamic_island",
		ReturnEntryAvailable: &available,
		PipBridgeEnabled:     &enabled,
	}
	if phoneBridgeCanRestoreFromReturnEntry(status) {
		t.Fatal("PiP background bridge mode hides Dynamic Island and must block restore")
	}

	enabled = false
	if !phoneBridgeCanRestoreFromReturnEntry(status) {
		t.Fatal("visible Dynamic Island entry should allow restore when PiP bridge mode is disabled")
	}
}

func TestSendRoutedBridgeCommandChoosesDeliveryPath(t *testing.T) {
	t.Run("foreground websocket", func(t *testing.T) {
		bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
			if cmd.Type != "clipboard_read" {
				t.Errorf("command type = %q, want clipboard_read", cmd.Type)
			}
			return BridgeCommandResponse{Method: "foreground"}
		})

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, restored, err := sendRoutedBridgeCommand(ctx, bridge, nil, BridgeCommand{
			ID:        "route_foreground",
			Type:      "clipboard_read",
			TimeoutMs: 1000,
		})
		if err != nil {
			t.Fatalf("sendRoutedBridgeCommand() error = %v", err)
		}
		if restored {
			t.Fatal("sendRoutedBridgeCommand() restored = true, want false")
		}
		if resp.Method != "foreground" {
			t.Fatalf("response method = %q, want foreground", resp.Method)
		}
	})

	t.Run("pip background queue", func(t *testing.T) {
		bridge := NewPhoneBridge(nil)
		t.Cleanup(func() { bridge.queue.Stop() })
		bridge.mu.Lock()
		bridge.platform = "ios"
		bridge.appState = "background"
		bridge.appStateAt = time.Now()
		bridge.pipBridgeEnabled = true
		bridge.pipBridgeSeen = true
		bridge.mu.Unlock()

		tapCalled := false
		restorer := NewPhoneBridgeRestorer(bridge, nil)
		restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
			tapCalled = true
			return nil
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			commands := bridge.queue.PollForPhone("ios", "", 10)
			if len(commands) != 1 {
				t.Errorf("expected one queued command, got %d", len(commands))
				return
			}
			if commands[0].Type != "clipboard_read" {
				t.Errorf("queued command type = %q, want clipboard_read", commands[0].Type)
				return
			}
			if err := bridge.queue.SubmitResult(BridgeCommandResponse{
				ID:     commands[0].ID,
				Method: "queued",
			}); err != nil {
				t.Errorf("SubmitResult() error = %v", err)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, restored, err := sendRoutedBridgeCommand(ctx, bridge, restorer, BridgeCommand{
			ID:        "route_queued",
			Type:      "clipboard_read",
			TimeoutMs: 1000,
		})
		if err != nil {
			t.Fatalf("sendRoutedBridgeCommand() error = %v", err)
		}
		if restored {
			t.Fatal("sendRoutedBridgeCommand() restored = true, want false")
		}
		if tapCalled {
			t.Fatal("return entry tap should not be used for PiP background queue")
		}
		if resp.Method != "queued" {
			t.Fatalf("response method = %q, want queued", resp.Method)
		}
	})

	t.Run("android fgs background queue", func(t *testing.T) {
		bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
			t.Errorf("websocket should not receive command in Android FGS background mode: %+v", cmd)
			return BridgeCommandResponse{Method: "unexpected_websocket"}
		})
		bridge.mu.Lock()
		bridge.platform = "android"
		bridge.appState = "background"
		bridge.appStateAt = time.Now()
		bridge.fgsBridgeEnabled = true
		bridge.fgsBridgeSeen = true
		bridge.fgsBridgeAt = time.Now()
		bridge.mu.Unlock()

		tapCalled := false
		restorer := NewPhoneBridgeRestorer(bridge, nil)
		restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
			tapCalled = true
			return nil
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			commands := bridge.queue.PollForPhone("android", "", 10)
			if len(commands) != 1 {
				t.Errorf("expected one queued command, got %d", len(commands))
				return
			}
			if commands[0].Type != "clipboard_read" {
				t.Errorf("queued command type = %q, want clipboard_read", commands[0].Type)
				return
			}
			if err := bridge.queue.SubmitResult(BridgeCommandResponse{
				ID:     commands[0].ID,
				Method: "queued_android_fgs",
			}); err != nil {
				t.Errorf("SubmitResult() error = %v", err)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, restored, err := sendRoutedBridgeCommand(ctx, bridge, restorer, BridgeCommand{
			ID:        "route_android_fgs",
			Type:      "clipboard_read",
			TimeoutMs: 1000,
		})
		if err != nil {
			t.Fatalf("sendRoutedBridgeCommand() error = %v", err)
		}
		if restored {
			t.Fatal("sendRoutedBridgeCommand() restored = true, want false")
		}
		if tapCalled {
			t.Fatal("return entry tap should not be used for Android FGS background queue")
		}
		if resp.Method != "queued_android_fgs" {
			t.Fatalf("response method = %q, want queued_android_fgs", resp.Method)
		}
	})

	t.Run("restore then websocket", func(t *testing.T) {
		bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
			if cmd.Type != "clipboard_read" {
				t.Errorf("command type = %q, want clipboard_read", cmd.Type)
			}
			return BridgeCommandResponse{Method: "restored"}
		})
		bridge.mu.Lock()
		bridge.platform = "ios"
		bridge.appState = "background"
		bridge.returnEntry = "dynamic_island"
		bridge.returnEntrySeen = true
		bridge.returnEntryOK = true
		bridge.mu.Unlock()

		tapCalled := false
		restorer := NewPhoneBridgeRestorer(bridge, nil)
		restorer.waitTimeout = time.Second
		restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
			tapCalled = true
			bridge.mu.Lock()
			bridge.appState = "active"
			bridge.returnEntry = "none"
			bridge.returnEntrySeen = true
			bridge.returnEntryOK = false
			bridge.lastHeartbeatAt = time.Now()
			bridge.mu.Unlock()
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, restored, err := sendRoutedBridgeCommand(ctx, bridge, restorer, BridgeCommand{
			ID:        "route_restored",
			Type:      "clipboard_read",
			TimeoutMs: 1000,
		})
		if err != nil {
			t.Fatalf("sendRoutedBridgeCommand() error = %v", err)
		}
		if !restored {
			t.Fatal("sendRoutedBridgeCommand() restored = false, want true")
		}
		if !tapCalled {
			t.Fatal("return entry tap was not used before routed command")
		}
		if resp.Method != "restored" {
			t.Fatalf("response method = %q, want restored", resp.Method)
		}
	})
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
