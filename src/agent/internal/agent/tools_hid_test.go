package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestResolvePointerPositionNormalized(t *testing.T) {
	x, y, err := resolvePointerPosition(nil, 0.5, 0.25, "normalized", coordinateSpaceNormalized)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	if x != 16384 {
		t.Fatalf("x = %d, want 16384", x)
	}
	if y != 8192 {
		t.Fatalf("y = %d, want 8192", y)
	}
}

func TestResolvePointerPositionPixelUsesScreenDimensions(t *testing.T) {
	screen := &screenState{}
	screen.Update(1000, 2000)

	x, y, err := resolvePointerPosition(screen, 500, 1000, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	if x != 16400 {
		t.Fatalf("x = %d, want 16400", x)
	}
	if y != 16392 {
		t.Fatalf("y = %d, want 16392", y)
	}
}

func TestResolvePointerPositionPixelRequiresDimensions(t *testing.T) {
	_, _, err := resolvePointerPosition(&screenState{}, 10, 20, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for pixel coordinates without screen dimensions")
	}
}

func TestTouchGestureSwipeWritesDragSequence(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":0.1,"y":0.9},"end":{"x":0.9,"y":0.1},"steps":3,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 6 {
		t.Fatalf("len(reports) = %d, want 6 (pre-move, press, 3 moves, release)", len(reports))
	}
	if reports[0].buttons != 0x00 {
		t.Fatalf("pre-move buttons = %d, want 0", reports[0].buttons)
	}
	if reports[0].x != 3277 || reports[0].y != 29490 {
		t.Fatalf("pre-move point = (%d,%d), want (3277,29490)", reports[0].x, reports[0].y)
	}
	if reports[1].buttons != 0x01 {
		t.Fatalf("press buttons = %d, want 1", reports[1].buttons)
	}
	if reports[1].x != 3277 || reports[1].y != 29490 {
		t.Fatalf("press point = (%d,%d), want (3277,29490)", reports[1].x, reports[1].y)
	}
	if reports[4].x != 29490 || reports[4].y != 3277 || reports[4].buttons != 0x01 {
		t.Fatalf("final move = (%d,%d,%d), want (29490,3277,1)", reports[4].x, reports[4].y, reports[4].buttons)
	}
	if reports[5].x != 29490 || reports[5].y != 3277 || reports[5].buttons != 0x00 {
		t.Fatalf("release = (%d,%d,%d), want (29490,3277,0)", reports[5].x, reports[5].y, reports[5].buttons)
	}
}

func TestMouseMoveAutoFallsBackToAbsoluteWithoutScreenDimensions(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseMoveTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"x":123,"y":456}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1", len(reports))
	}
	if reports[0].x != 123 || reports[0].y != 456 || reports[0].buttons != 0 {
		t.Fatalf("report = (%d,%d,%d), want (123,456,0)", reports[0].x, reports[0].y, reports[0].buttons)
	}
	if reports[0].wheel != 0 {
		t.Fatalf("wheel = %d, want 0", reports[0].wheel)
	}
}

func TestKeyboardTextReportsUnsupportedCharacters(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTextTool{dev: dev}

	out, err := tool.Call(context.Background(), `{"text":"A™B"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != `ok; skipped unsupported characters: "™"` {
		t.Fatalf("unexpected output: %q", out)
	}

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 32 {
		t.Fatalf("expected 4 keyboard reports for 2 ASCII characters, got %d bytes", len(data))
	}
}

func TestHIDDeviceWriteRetriesAfterEndpointShutdown(t *testing.T) {
	first := &fakeHIDWriteCloser{writeErr: syscall.ESHUTDOWN}
	second := &fakeHIDWriteCloser{}
	openCount := 0

	dev := &HIDDevice{
		path: "fake-hid",
		open: func(string) (io.WriteCloser, error) {
			openCount++
			if openCount == 1 {
				return first, nil
			}
			return second, nil
		},
	}

	if err := dev.Write([]byte{1, 2, 3}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if openCount != 2 {
		t.Fatalf("openCount = %d, want 2", openCount)
	}
	if !first.closed {
		t.Fatalf("expected first writer to be closed after retryable failure")
	}
	if second.writeCount != 1 {
		t.Fatalf("second writer writeCount = %d, want 1", second.writeCount)
	}
}

func TestHIDDeviceWriteReturnsNonRetryableError(t *testing.T) {
	dev := &HIDDevice{
		path: "fake-hid",
		open: func(string) (io.WriteCloser, error) {
			return &fakeHIDWriteCloser{writeErr: errors.New("permission denied")}, nil
		},
	}

	err := dev.Write([]byte{1})
	if err == nil {
		t.Fatal("expected write error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMouseScrollUsesLastPointerPosition(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	state := &pointerState{}
	moveTool := &MouseMoveTool{dev: dev, screen: &screenState{}, state: state}
	scrollTool := &MouseScrollTool{dev: dev, state: state}

	if out, err := moveTool.Call(context.Background(), `{"x":123,"y":456}`); err != nil || out != "ok" {
		t.Fatalf("move output=%q err=%v", out, err)
	}
	if out, err := scrollTool.Call(context.Background(), `{"delta":-3}`); err != nil || out != "ok" {
		t.Fatalf("scroll output=%q err=%v", out, err)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	if reports[1].buttons != 0 || reports[1].x != 123 || reports[1].y != 456 || reports[1].wheel != -3 {
		t.Fatalf("scroll report = (%d,%d,%d,%d), want (0,123,456,-3)", reports[1].buttons, reports[1].x, reports[1].y, reports[1].wheel)
	}
}

type mouseReport struct {
	buttons uint8
	x       uint16
	y       uint16
	wheel   int8
}

func newTestHIDDevice(t *testing.T) (*HIDDevice, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "hid.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.Close()

	return NewHIDDevice(path), path
}

func readMouseReports(t *testing.T, dev *HIDDevice, path string) []mouseReport {
	t.Helper()

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data)%6 != 0 {
		t.Fatalf("mouse report data length = %d, want multiple of 6", len(data))
	}

	reports := make([]mouseReport, 0, len(data)/6)
	for i := 0; i < len(data); i += 6 {
		reports = append(reports, mouseReport{
			buttons: data[i],
			x:       binary.LittleEndian.Uint16(data[i+1 : i+3]),
			y:       binary.LittleEndian.Uint16(data[i+3 : i+5]),
			wheel:   int8(data[i+5]),
		})
	}
	return reports
}

type fakeHIDWriteCloser struct {
	closed     bool
	writeCount int
	writeErr   error
}

func (f *fakeHIDWriteCloser) Write(p []byte) (int, error) {
	f.writeCount++
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeHIDWriteCloser) Close() error {
	f.closed = true
	return nil
}

// timestampedHIDWriter records the time of each successful write so timing
// behaviour (e.g. tap hold, swipe hold_before_ms) can be asserted in tests.
type timestampedHIDWriter struct {
	mu     sync.Mutex
	closed bool
	times  []time.Time
}

func (w *timestampedHIDWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.times = append(w.times, time.Now())
	return len(p), nil
}

func (w *timestampedHIDWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *timestampedHIDWriter) writeTimes() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]time.Time, len(w.times))
	copy(out, w.times)
	return out
}

func newTimedHIDDevice() (*HIDDevice, *timestampedHIDWriter) {
	w := &timestampedHIDWriter{}
	dev := &HIDDevice{
		path: "timed-hid",
		open: func(string) (io.WriteCloser, error) {
			return w, nil
		},
	}
	return dev, w
}

func TestTapPointerHoldsBetweenPressAndRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()

	if err := tapPointer(dev, &pointerState{}, 100, 200, 0x01); err != nil {
		t.Fatalf("tapPointer error: %v", err)
	}

	times := w.writeTimes()
	if len(times) != 3 {
		t.Fatalf("len(times) = %d, want 3 (pre-move, press, release)", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap < 50*time.Millisecond {
		t.Fatalf("gap between press and release = %v, want >= 50ms", gap)
	}
}

func TestTapPointerSettlesCursorBeforePress(t *testing.T) {
	dev, w := newTimedHIDDevice()

	if err := tapPointer(dev, &pointerState{}, 100, 200, 0x01); err != nil {
		t.Fatalf("tapPointer error: %v", err)
	}

	times := w.writeTimes()
	if len(times) != 3 {
		t.Fatalf("len(times) = %d, want 3", len(times))
	}
	settleGap := times[1].Sub(times[0])
	if settleGap < 60*time.Millisecond {
		t.Fatalf("pre-move to press gap = %v, want >= 60ms (cursor settle)", settleGap)
	}
}

func TestMouseClickToolHoldsBetweenPressAndRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &MouseClickTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"x":0.5,"y":0.5,"coord_space":"normalized"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 3 {
		t.Fatalf("len(times) = %d, want 3 (pre-move, press, release)", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap < 50*time.Millisecond {
		t.Fatalf("gap between press and release = %v, want >= 50ms", gap)
	}
}

func TestTouchGestureTapAcceptsHoldMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":0.5,"y":0.5},"hold_ms":150}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 3 {
		t.Fatalf("len(times) = %d, want 3 (pre-move, press, release)", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap < 130*time.Millisecond {
		t.Fatalf("press-to-release gap = %v, want >= 130ms", gap)
	}
}

func TestTouchGestureSwipeAppliesDefaultHoldBeforeMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	// duration_ms=0 keeps the per-step delay at 0 so only the hold_before_ms
	// shows up between the press and the first move step.
	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":0.01,"y":0.5},"end":{"x":0.5,"y":0.5},"steps":2,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, move1, move2, release
	times := w.writeTimes()
	if len(times) < 4 {
		t.Fatalf("len(times) = %d, want >= 4", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap < 30*time.Millisecond {
		t.Fatalf("swipe press-to-first-move gap = %v, want >= 30ms", gap)
	}
}

func TestTouchGestureSwipeDefaultsUseSlowerMotionAndDelayedRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":0.1,"y":0.5},"end":{"x":0.9,"y":0.5}}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, 24 default move steps, release.
	times := w.writeTimes()
	if len(times) != defaultSwipeSteps+3 {
		t.Fatalf("len(times) = %d, want %d", len(times), defaultSwipeSteps+3)
	}
	moveStart := 2
	lastMove := len(times) - 2
	moveDuration := times[lastMove].Sub(times[moveStart])
	if moveDuration < 550*time.Millisecond {
		t.Fatalf("swipe move duration = %v, want >= 550ms", moveDuration)
	}
	releaseDelay := times[len(times)-1].Sub(times[lastMove])
	if releaseDelay < 270*time.Millisecond {
		t.Fatalf("swipe final-move-to-release gap = %v, want >= 270ms", releaseDelay)
	}
}

func TestTouchGestureDragKeepsZeroHoldBeforeMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}, state: &pointerState{}}

	out, err := tool.Call(context.Background(), `{"type":"drag","start":{"x":0.1,"y":0.1},"end":{"x":0.9,"y":0.9},"steps":2,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, move1, move2, release. Drag must not inherit
	// the swipe hold defaults, so the press-to-first-move and final-move-to-
	// release gaps are both ~0.
	times := w.writeTimes()
	if len(times) < 4 {
		t.Fatalf("len(times) = %d, want >= 4", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap > 20*time.Millisecond {
		t.Fatalf("drag press-to-first-move gap = %v, want < 20ms (drag must not inherit swipe hold)", gap)
	}
	releaseGap := times[len(times)-1].Sub(times[len(times)-2])
	if releaseGap > 20*time.Millisecond {
		t.Fatalf("drag final-move-to-release gap = %v, want < 20ms (drag must not inherit swipe release hold)", releaseGap)
	}
}

func TestDragPointerReleasesOnMoveError(t *testing.T) {
	failAfter := 3 // settle + press + first move = 3rd write fails
	writer := &countingFailWriter{failAt: failAfter}
	dev := &HIDDevice{
		path: "fail-hid",
		open: func(string) (io.WriteCloser, error) {
			return writer, nil
		},
	}

	start := resolvedPointerPoint{x: 100, y: 100}
	end := resolvedPointerPoint{x: 200, y: 200}
	err := dragPointer(dev, &pointerState{}, start, end, 0x01, 0, 0, 0, 3)
	if err == nil {
		t.Fatal("expected error from dragPointer")
	}
	// The release report must have been attempted even though a move failed.
	if writer.writeCount < failAfter+1 {
		t.Fatalf("writeCount = %d, expected at least %d (release must be attempted after move failure)", writer.writeCount, failAfter+1)
	}
}

type countingFailWriter struct {
	mu         sync.Mutex
	writeCount int
	failAt     int
	closed     bool
}

func (w *countingFailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeCount++
	if w.writeCount == w.failAt {
		return 0, errors.New("simulated write failure")
	}
	return len(p), nil
}

func (w *countingFailWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func TestScreenStateDimensionsWithAge(t *testing.T) {
	screen := &screenState{}

	if _, _, _, ok := screen.DimensionsWithAge(); ok {
		t.Fatal("expected ok=false before Update")
	}

	screen.Update(800, 1600)
	w, h, age, ok := screen.DimensionsWithAge()
	if !ok {
		t.Fatal("expected ok=true after Update")
	}
	if w != 800 || h != 1600 {
		t.Fatalf("dims = %dx%d, want 800x1600", w, h)
	}
	if age > time.Second {
		t.Fatalf("fresh age = %v, want < 1s", age)
	}
}

func TestResolvePointerPositionPixelRejectsStaleDimensions(t *testing.T) {
	screen := &screenState{}
	screen.Update(1000, 2000)

	// Backdate the cache to look older than the staleness threshold.
	screen.mu.Lock()
	screen.updatedAt = time.Now().Add(-2 * screenDimensionsStaleAfter)
	screen.mu.Unlock()

	_, _, err := resolvePointerPosition(screen, 500, 1000, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for stale pixel coordinates")
	}
	if !strings.Contains(err.Error(), "stale") && !strings.Contains(err.Error(), "old") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolvePointerPositionAutoFallsBackWhenStale(t *testing.T) {
	screen := &screenState{}
	screen.Update(1000, 2000)
	screen.mu.Lock()
	screen.updatedAt = time.Now().Add(-2 * screenDimensionsStaleAfter)
	screen.mu.Unlock()

	// Auto must not error on stale cache; it falls back to treating values as
	// absolute HID coordinates, matching the cold-start behaviour.
	x, y, err := resolvePointerPosition(screen, 123, 456, "", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("expected no error on stale auto, got %v", err)
	}
	if x != 123 || y != 456 {
		t.Fatalf("auto fallback = (%d,%d), want (123,456)", x, y)
	}
}
