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
	status.AppStateUpdatedAt = ptrTime(time.Now().Add(-pipBridgeBackgroundStateMaxAge - time.Second))
	if phoneBridgeCanUsePiPBackground(status, "clipboard_read") {
		t.Fatal("stale PiP bridge status should not allow background queue")
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

func ptrTime(value time.Time) *time.Time {
	return &value
}
