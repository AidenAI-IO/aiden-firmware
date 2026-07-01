package agent

import (
	"context"
	"errors"
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

func TestPhoneBridgeRestorerSuppressesRepeatedTapAfterFailure(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	bridge.mu.Unlock()

	now := time.Date(2026, 6, 29, 8, 0, 0, 0, time.UTC)
	restorer := NewPhoneBridgeRestorer(bridge, nil)
	restorer.failureCache = time.Minute
	restorer.now = func() time.Time { return now }
	taps := 0
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		taps++
		return errors.New("open /dev/hidg1: no such device or address")
	}

	if restored, err := restorer.EnsureForeground(context.Background()); err == nil || restored {
		t.Fatalf("first EnsureForeground() restored=%v err=%v, want tap failure", restored, err)
	}
	now = now.Add(10 * time.Second)
	restored, err := restorer.EnsureForeground(context.Background())
	if err == nil || restored {
		t.Fatalf("second EnsureForeground() restored=%v err=%v, want cached failure", restored, err)
	}
	if taps != 1 {
		t.Fatalf("tap attempts = %d, want one attempt during failure cache", taps)
	}
	var suppressed *phoneBridgeRestoreSuppressedError
	if !errors.As(err, &suppressed) {
		t.Fatalf("second error = %T %v, want suppressed restore error", err, err)
	}
	var tapErr *phoneBridgeReturnEntryTapError
	if !errors.As(err, &tapErr) {
		t.Fatalf("second error = %T %v, want wrapped tap error", err, err)
	}
	te := phoneBridgeCommandPreconditionToolError(bridge.Status(), err)
	if te == nil || te.Code != CodeAppBackgrounded {
		t.Fatalf("cached restore error tool error = %#v, want app_backgrounded", te)
	}

	now = now.Add(time.Minute)
	if restored, err := restorer.EnsureForeground(context.Background()); err == nil || restored {
		t.Fatalf("expired-cache EnsureForeground() restored=%v err=%v, want another tap failure", restored, err)
	}
	if taps != 2 {
		t.Fatalf("tap attempts after cache expiry = %d, want 2", taps)
	}
}

func TestPhoneBridgeRestorerDoesNotCacheCanceledTap(t *testing.T) {
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
	restorer.failureCache = time.Minute
	taps := 0
	restorer.tapReturnEntry = func(ctx context.Context, status PhoneBridgeStatus) error {
		taps++
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if restored, err := restorer.EnsureForeground(ctx); !errors.Is(err, context.Canceled) || restored {
		t.Fatalf("canceled EnsureForeground() restored=%v err=%v, want context.Canceled without restore", restored, err)
	}
	if restorer.lastFailure.err != nil {
		t.Fatalf("canceled tap cached failure: %v", restorer.lastFailure.err)
	}

	if restored, err := restorer.EnsureForeground(ctx); !errors.Is(err, context.Canceled) || restored {
		t.Fatalf("second canceled EnsureForeground() restored=%v err=%v, want fresh context.Canceled", restored, err)
	}
	if taps != 2 {
		t.Fatalf("tap attempts = %d, want retry after uncached cancellation", taps)
	}
}

func TestPhoneBridgeRestorerDoesNotCacheCanceledWait(t *testing.T) {
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
	restorer.failureCache = time.Minute
	taps := 0
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		taps++
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if restored, err := restorer.EnsureForeground(ctx); !errors.Is(err, context.Canceled) || !restored {
		t.Fatalf("canceled wait EnsureForeground() restored=%v err=%v, want context.Canceled after tap", restored, err)
	}
	if restorer.lastFailure.err != nil {
		t.Fatalf("canceled wait cached failure: %v", restorer.lastFailure.err)
	}

	if restored, err := restorer.EnsureForeground(ctx); !errors.Is(err, context.Canceled) || !restored {
		t.Fatalf("second canceled wait EnsureForeground() restored=%v err=%v, want fresh context.Canceled", restored, err)
	}
	if taps != 2 {
		t.Fatalf("tap attempts = %d, want retry after uncached cancellation", taps)
	}
}

func TestPhoneBridgeRestorerTapUsesSurfaceMapping(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screen := &screenState{}
	screen.UpdateActiveArea(1280, 720, screenActiveArea{X: 0, Y: 72, Width: 1280, Height: 576, Valid: true})
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()
	restorer := NewPhoneBridgeRestorer(bridge, testTouchscreenPointerController(dev, &pointerState{}), screen)

	err := restorer.tap(context.Background(), PhoneBridgeStatus{ReturnEntry: "dynamic_island"})
	if err != nil {
		t.Fatal(err)
	}

	reports := readTouchscreenReports(t, dev, path)
	if len(reports) == 0 {
		t.Fatal("expected touchscreen reports")
	}
	expectedX := scalePixelToAbsolute(float64(1280-1)*0.5, 1280)
	expectedY := scalePixelToAbsolute(72+(float64(576-1)*0.03), 720)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedY) {
		t.Fatalf("tap = (%d,%d), want active-area mapped (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedY)
	}
	rawX, rawY := normalizedToAbsolutePoint(returnEntryDynamicIslandX, returnEntryDynamicIslandY)
	if reports[0].x == uint16(rawX) && reports[0].y == uint16(rawY) {
		t.Fatalf("tap used raw normalized mapping (%d,%d), want surface-aware mapping", rawX, rawY)
	}
}
