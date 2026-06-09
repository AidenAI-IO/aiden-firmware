package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestResolvePointerPositionNormalized(t *testing.T) {
	x, y, err := resolvePointerPosition(nil, 500, 250, "normalized", coordinateSpaceNormalized)
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

func TestResolvePointerPositionPixelUsesActiveArea(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	x, y, err := resolvePointerPosition(screen, 960, 540, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	if x != 16410 {
		t.Fatalf("x = %d, want 16410", x)
	}
	if y != 16399 {
		t.Fatalf("y = %d, want 16399", y)
	}
}

func TestResolvePointerPositionPixelRejectsBlackBar(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	_, _, err := resolvePointerPosition(screen, 100, 540, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for pixel coordinates in black bar")
	}
	if !strings.Contains(err.Error(), "outside active screen area") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolvePointerPositionAutoTreatsUnitCoordinatesAsNormalized(t *testing.T) {
	screen := &screenState{}
	screen.Update(1000, 2000)

	x, y, err := resolvePointerPosition(screen, 500, 250, "", coordinateSpaceAuto)
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

func TestResolvePointerPositionPixelRequiresDimensions(t *testing.T) {
	_, _, err := resolvePointerPosition(&screenState{}, 10, 20, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for pixel coordinates without screen dimensions")
	}
}

func TestResolvePointerPositionPixelRejectsOutOfBounds(t *testing.T) {
	screen := &screenState{}
	screen.Update(431, 947)

	_, _, err := resolvePointerPosition(screen, 745, 125, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for out-of-bounds pixel coordinates")
	}
	if !strings.Contains(err.Error(), "outside cached screenshot bounds 431x947") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPointerCoordinateRejectsNonFiniteStringValues(t *testing.T) {
	for _, input := range []string{`"NaN"`, `"Inf"`, `"-Inf"`, `"Infinity"`} {
		var coordinate pointerCoordinate
		err := json.Unmarshal([]byte(input), &coordinate)
		if err == nil {
			t.Fatalf("UnmarshalJSON(%s) succeeded, want finite number error", input)
		}
		if !strings.Contains(err.Error(), "coordinate must be a finite number") {
			t.Fatalf("UnmarshalJSON(%s) error = %v, want finite number error", input, err)
		}
	}
}

func testPointerController(dev *HIDDevice, state *pointerState) *pointerController {
	return &pointerController{dev: dev, state: state}
}

func testTouchscreenPointerController(dev *HIDDevice, state *pointerState) *pointerController {
	return &pointerController{dev: dev, state: state, touchscreen: true}
}

type touchscreenReport struct {
	flags     uint8
	contactID uint8
	x         uint16
	y         uint16
}

func readTouchscreenReports(t *testing.T, dev *HIDDevice, path string) []touchscreenReport {
	t.Helper()

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data)%6 != 0 {
		t.Fatalf("touchscreen report data length = %d, want multiple of 6", len(data))
	}

	reports := make([]touchscreenReport, 0, len(data)/6)
	for i := 0; i < len(data); i += 6 {
		reports = append(reports, touchscreenReport{
			flags:     data[i],
			contactID: data[i+1],
			x:         binary.LittleEndian.Uint16(data[i+2 : i+4]),
			y:         binary.LittleEndian.Uint16(data[i+4 : i+6]),
		})
	}
	return reports
}

func TestTouchGestureSwipeWritesDragSequence(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":100,"y":900},"end":{"x":900,"y":100},"steps":3,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+3+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (pre-move, press, 3 moves, repeated release)", len(reports), 2+3+touchReleaseReportCount)
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
	for i := 5; i < len(reports); i++ {
		if reports[i].x != 29490 || reports[i].y != 3277 || reports[i].buttons != 0x00 {
			t.Fatalf("release report %d = (%d,%d,%d), want (29490,3277,0)", i-4, reports[i].x, reports[i].y, reports[i].buttons)
		}
	}
}

func TestDirectionalSwipeStrengthControlsDistance(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe_up","strength":"tiny","duration_ms":0,"hold_before_ms":0,"hold_after_ms":0}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+10+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+10+touchReleaseReportCount)
	}
	if reports[0].y != 17039 {
		t.Fatalf("tiny swipe_up start y = %d, want 17039", reports[0].y)
	}
	if reports[11].y != 15728 {
		t.Fatalf("tiny swipe_up end y = %d, want 15728", reports[11].y)
	}
}

func TestDirectionalSwipeDistanceOverridesStrength(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe_up","strength":"tiny","distance":200,"duration_ms":0,"hold_before_ms":0,"hold_after_ms":0,"steps":2}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+2+touchReleaseReportCount)
	}
	if reports[0].y != 19660 {
		t.Fatalf("override start y = %d, want 19660", reports[0].y)
	}
	if reports[3].y != 13107 {
		t.Fatalf("override end y = %d, want 13107", reports[3].y)
	}
}

func TestDirectionalSwipeStrengthDefaultsToImmediateRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe_left","strength":"medium","steps":2,"duration_ms":0,"hold_before_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 2+2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d", len(times), 2+2+touchReleaseReportCount)
	}
	firstRelease := len(times) - touchReleaseReportCount
	releaseDelay := times[firstRelease].Sub(times[firstRelease-1])
	if releaseDelay > 200*time.Millisecond {
		t.Fatalf("directional swipe final-move-to-release gap = %v, want no default hold_after_ms delay", releaseDelay)
	}
}

func TestDirectionalSwipeRejectsInvalidStrength(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe_up","strength":"huge"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, `unsupported strength: "huge"`) {
		t.Fatalf("Call output = %q, want unsupported strength error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestMouseMoveAutoFallsBackToAbsoluteWithoutScreenDimensions(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseMoveTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"x":2000,"y":3000}`)
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
	if reports[0].x != 2000 || reports[0].y != 3000 || reports[0].buttons != 0 {
		t.Fatalf("report = (%d,%d,%d), want (2000,3000,0)", reports[0].x, reports[0].y, reports[0].buttons)
	}
	if reports[0].wheel != 0 {
		t.Fatalf("wheel = %d, want 0", reports[0].wheel)
	}
}

func TestMouseClickAcceptsStringCoordinates(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseClickTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"x":"500","y":"250","coord_space":"normalized"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (pre-move, press, repeated release)", len(reports), 2+touchReleaseReportCount)
	}
	if reports[0].x != 16384 || reports[0].y != 8192 {
		t.Fatalf("pre-move point = (%d,%d), want (16384,8192)", reports[0].x, reports[0].y)
	}
}

func TestTouchGestureTapAcceptsStringCoordinates(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":"500","y":"250"}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (pre-move, press, repeated release)", len(reports), 2+touchReleaseReportCount)
	}
	if reports[0].x != 16384 || reports[0].y != 8192 {
		t.Fatalf("pre-move point = (%d,%d), want (16384,8192)", reports[0].x, reports[0].y)
	}
}

func TestTouchscreenTapWritesTouchDownAndUp(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":250}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readTouchscreenReports(t, dev, path)
	if len(reports) != 1+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (down, repeated up)", len(reports), 1+touchReleaseReportCount)
	}
	if reports[0].flags != 0x03 || reports[0].contactID != 1 || reports[0].x != 16384 || reports[0].y != 8192 {
		t.Fatalf("down report = %+v, want touch at (16384,8192)", reports[0])
	}
	for i := 1; i < len(reports); i++ {
		if reports[i].flags != 0x00 || reports[i].contactID != 1 || reports[i].x != 16384 || reports[i].y != 8192 {
			t.Fatalf("up report %d = %+v, want release at (16384,8192)", i, reports[i])
		}
	}
}

func TestTouchscreenSwipeWritesTouchSequence(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"drag","start":{"x":200,"y":500},"end":{"x":800,"y":500},"steps":2,"duration_ms":0,"hold_before_ms":0}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readTouchscreenReports(t, dev, path)
	if len(reports) != 2+2+touchReleaseReportCount-1 {
		t.Fatalf("len(reports) = %d, want down + 2 moves + repeated releases", len(reports))
	}
	if reports[0].flags != 0x03 || reports[0].x != 6553 {
		t.Fatalf("down report = %+v, want start touch", reports[0])
	}
	if reports[2].flags != 0x03 || reports[2].x != 26214 {
		t.Fatalf("final move = %+v, want end while touching", reports[2])
	}
	last := reports[len(reports)-1]
	if last.flags != 0x00 || last.x != 26214 {
		t.Fatalf("last release = %+v, want release at end", last)
	}
}

func TestKeyboardTextAcceptsBareTextFallback(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTextTool{dev: dev}

	out, err := tool.Call(context.Background(), `App Store`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != len("App Store")*16 {
		t.Fatalf("expected 2 keyboard reports per ASCII character, got %d bytes", len(data))
	}
}

func TestKeyboardTextRejectsUnsupportedCharactersWithoutPartialTyping(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTextTool{dev: dev}

	out, err := tool.Call(context.Background(), `{"text":"A™中文B"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, `unsupported characters: "™中文"`) {
		t.Fatalf("unexpected output: %q", out)
	}

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("unsupported text must not type a partial prefix, got %d bytes", len(data))
	}
}

func TestPostActionScreenshotToolReturnsScreenshotJSON(t *testing.T) {
	action := &stubTool{name: "keyboard_tap", output: "ok"}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionScreenshotTool(action, screenshot, 0)

	out, err := tool.Call(context.Background(), `{"keys":["enter"]}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out == "ok" {
		t.Fatalf("Call output = %q, want screenshot JSON", out)
	}

	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid post-action screenshot JSON: %v", err)
	}
	if result.ActionOutput != "ok" {
		t.Fatalf("ActionOutput = %q, want ok", result.ActionOutput)
	}
	if result.Width != 320 || result.Height != 240 || result.Format != "jpeg" || result.Data != "ZmFrZQ==" {
		t.Fatalf("unexpected screenshot result: %#v", result)
	}
	if len(action.inputs) != 1 || action.inputs[0] != `{"keys":["enter"]}` {
		t.Fatalf("action inputs = %#v", action.inputs)
	}
	if len(screenshot.inputs) != 1 || screenshot.inputs[0] != "{}" {
		t.Fatalf("screenshot inputs = %#v", screenshot.inputs)
	}
	visual, ok := tool.(visualObservationTool)
	if !ok || !visual.ReturnsVisualObservation() {
		t.Fatalf("post-action tool must be a visual observation tool")
	}
}

func TestPostActionScreenshotToolFallsBackScreenshotWhenScreenUnstable(t *testing.T) {
	action := &stubTool{name: "touch_gesture", output: "ok"}
	waitStable := &stubTool{
		name:   "wait_for_stable_screen",
		output: `{"ok":true,"stable":false,"elapsed_ms":3001,"last_diff":18.5}`,
	}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionStableScreenshotTool(action, waitStable, screenshot, 0, ScreenStableDefaults{TimeoutMs: 3000, StableMs: 500})

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":500}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("Call output = %q, want success with fallback screenshot", out)
	}

	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid post-action screenshot JSON: %v", err)
	}
	if result.ActionOutput != "ok" {
		t.Fatalf("ActionOutput = %q, want ok", result.ActionOutput)
	}
	if result.ScreenStable == nil || *result.ScreenStable {
		t.Fatalf("ScreenStable = %#v, want false", result.ScreenStable)
	}
	if result.StableWaitMs == nil || *result.StableWaitMs != 3001 {
		t.Fatalf("StableWaitMs = %#v, want 3001", result.StableWaitMs)
	}
	if result.LastDiff == nil || *result.LastDiff != 18.5 {
		t.Fatalf("LastDiff = %#v, want 18.5", result.LastDiff)
	}
	if result.Data != "ZmFrZQ==" {
		t.Fatalf("screenshot data = %q, want fallback capture", result.Data)
	}
	if len(waitStable.inputs) != 1 || waitStable.inputs[0] != `{"timeout_ms":3000,"stable_ms":500,"diff_threshold":5}` {
		t.Fatalf("wait stable inputs = %#v", waitStable.inputs)
	}
	if len(screenshot.inputs) != 1 {
		t.Fatalf("screenshot should still be called, got inputs %#v", screenshot.inputs)
	}
}

func TestPostActionScreenshotToolOmitsLastDiffWhenStableWaitOmitsIt(t *testing.T) {
	action := &stubTool{name: "touch_gesture", output: "ok"}
	waitStable := &stubTool{
		name:   "wait_for_stable_screen",
		output: `{"ok":true,"stable":true,"elapsed_ms":600}`,
	}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionStableScreenshotTool(action, waitStable, screenshot, 0, ScreenStableDefaults{})

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":500}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid post-action screenshot JSON: %v", err)
	}
	if result.LastDiff != nil {
		t.Fatalf("LastDiff = %#v, want omitted", result.LastDiff)
	}
}

func TestPostActionScreenshotToolSkipsScreenshotOnActionErrorOutput(t *testing.T) {
	action := &stubTool{name: "mouse_click", output: "error: invalid input"}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionScreenshotTool(action, screenshot, 0)

	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "error: invalid input" {
		t.Fatalf("Call output = %q, want original error output", out)
	}
	if len(screenshot.inputs) != 0 {
		t.Fatalf("screenshot should not be called on action error, got inputs %#v", screenshot.inputs)
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

func TestHIDDeviceWriteTimesOutWhenFDWouldBlock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux nonblocking pipe semantics")
	}
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if err := unix.SetNonblock(int(readFile.Fd()), true); err != nil {
		t.Fatalf("SetNonblock(read): %v", err)
	}
	if err := unix.SetNonblock(int(writeFile.Fd()), true); err != nil {
		t.Fatalf("SetNonblock(write): %v", err)
	}
	defer readFile.Close()
	defer writeFile.Close()

	buf := make([]byte, 4096)
	for {
		_, err := unix.Write(int(writeFile.Fd()), buf)
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatalf("fill pipe: %v", err)
		}
	}

	dev := &HIDDevice{
		path:         "blocked-hid",
		file:         writeFile,
		writeTimeout: 20 * time.Millisecond,
	}
	start := time.Now()
	err = dev.Write([]byte{1})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked write returned after %v, want bounded timeout", elapsed)
	}
}

func TestMouseScrollUsesLastPointerPosition(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	state := &pointerState{}
	moveTool := &MouseMoveTool{pc: testPointerController(dev, state), screen: &screenState{}}
	scrollTool := &MouseScrollTool{pc: testPointerController(dev, state)}

	if out, err := moveTool.Call(context.Background(), `{"x":2000,"y":3000}`); err != nil || out != "ok" {
		t.Fatalf("move output=%q err=%v", out, err)
	}
	if out, err := scrollTool.Call(context.Background(), `{"delta":-3}`); err != nil || out != "ok" {
		t.Fatalf("scroll output=%q err=%v", out, err)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	if reports[1].buttons != 0 || reports[1].x != 2000 || reports[1].y != 3000 || reports[1].wheel != -3 {
		t.Fatalf("scroll report = (%d,%d,%d,%d), want (0,2000,3000,-3)", reports[1].buttons, reports[1].x, reports[1].y, reports[1].wheel)
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

func TestKeyboardTapSendsModifierOnly(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev}

	out, err := tool.Call(context.Background(), `{"keys":["meta"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 16 {
		t.Fatalf("report bytes = %d, want 16 (modifier + release)", len(data))
	}

	chord := data[0:8]
	release := data[8:16]
	if chord[0] != 0x08 {
		t.Fatalf("chord modifier = 0x%02x, want 0x08", chord[0])
	}
	for i := 2; i < 8; i++ {
		if chord[i] != 0 {
			t.Fatalf("chord key slot [%d] = 0x%02x, want 0", i, chord[i])
		}
	}
	for i := range release {
		if release[i] != 0 {
			t.Fatalf("release report = %v, want all zeros", release)
		}
	}
}

func TestKeyboardTapSendsModifierChordWithHold(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev}

	out, err := tool.Call(context.Background(), `{"keys":["meta","q"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 16 {
		t.Fatalf("report bytes = %d, want 16 (chord + release)", len(data))
	}

	chord := data[0:8]
	release := data[8:16]

	if chord[0] != 0x08 || chord[2] != 0x14 {
		t.Fatalf("chord report = %v, want modifier 0x08 and key q(0x14)", chord)
	}
	for i := range release {
		if release[i] != 0 {
			t.Fatalf("release report = %v, want all zeros", release)
		}
	}
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

	pc := testPointerController(dev, &pointerState{})
	if err := tapPointer(pc, 100, 200, 0x01); err != nil {
		t.Fatalf("tapPointer error: %v", err)
	}

	times := w.writeTimes()
	if len(times) != 2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d (pre-move, press, repeated release)", len(times), 2+touchReleaseReportCount)
	}
	gap := times[2].Sub(times[1])
	if gap < 50*time.Millisecond {
		t.Fatalf("gap between press and release = %v, want >= 50ms", gap)
	}
}

func TestTapPointerSettlesCursorBeforePress(t *testing.T) {
	dev, w := newTimedHIDDevice()

	pc := testPointerController(dev, &pointerState{})
	if err := tapPointer(pc, 100, 200, 0x01); err != nil {
		t.Fatalf("tapPointer error: %v", err)
	}

	times := w.writeTimes()
	if len(times) != 2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d", len(times), 2+touchReleaseReportCount)
	}
	settleGap := times[1].Sub(times[0])
	if settleGap < 60*time.Millisecond {
		t.Fatalf("pre-move to press gap = %v, want >= 60ms (cursor settle)", settleGap)
	}
}

func TestMouseClickToolHoldsBetweenPressAndRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &MouseClickTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"x":500,"y":500,"coord_space":"normalized"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d (pre-move, press, repeated release)", len(times), 2+touchReleaseReportCount)
	}
	gap := times[2].Sub(times[1])
	if gap < 50*time.Millisecond {
		t.Fatalf("gap between press and release = %v, want >= 50ms", gap)
	}
}

func TestTouchGestureTapAcceptsHoldMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":500},"hold_ms":150}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d (pre-move, press, repeated release)", len(times), 2+touchReleaseReportCount)
	}
	gap := times[2].Sub(times[1])
	if gap < 130*time.Millisecond {
		t.Fatalf("press-to-release gap = %v, want >= 130ms", gap)
	}
}

func TestTouchGestureSwipeAppliesDefaultHoldBeforeMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	// duration_ms=0 keeps the per-step delay at 0 so only the hold_before_ms
	// shows up between the press and the first move step.
	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":10,"y":500},"end":{"x":500,"y":500},"steps":2,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, move1, move2, repeated release.
	times := w.writeTimes()
	if len(times) < 4 {
		t.Fatalf("len(times) = %d, want >= 4", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap < 30*time.Millisecond {
		t.Fatalf("swipe press-to-first-move gap = %v, want >= 30ms", gap)
	}
}

func TestTouchGestureSwipeDefaultsUseSlowerMotionAndImmediateRelease(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":100,"y":500},"end":{"x":900,"y":500}}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, 24 default move steps, repeated release.
	times := w.writeTimes()
	if len(times) != defaultSwipeSteps+2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d", len(times), defaultSwipeSteps+2+touchReleaseReportCount)
	}
	moveStart := 2
	firstRelease := len(times) - touchReleaseReportCount
	lastMove := firstRelease - 1
	moveDuration := times[lastMove].Sub(times[moveStart])
	if moveDuration < 550*time.Millisecond {
		t.Fatalf("swipe move duration = %v, want >= 550ms", moveDuration)
	}
	releaseDelay := times[firstRelease].Sub(times[lastMove])
	if releaseDelay > 200*time.Millisecond {
		t.Fatalf("swipe final-move-to-release gap = %v, want no default hold_after_ms delay", releaseDelay)
	}
}

func TestTouchGestureSwipeAcceptsHoldAfterMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":100,"y":500},"end":{"x":900,"y":500},"steps":2,"duration_ms":0,"hold_before_ms":0,"hold_after_ms":120}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	times := w.writeTimes()
	if len(times) != 2+2+touchReleaseReportCount {
		t.Fatalf("len(times) = %d, want %d (pre-move, press, 2 moves, repeated release)", len(times), 2+2+touchReleaseReportCount)
	}
	firstRelease := len(times) - touchReleaseReportCount
	releaseDelay := times[firstRelease].Sub(times[firstRelease-1])
	if releaseDelay < 100*time.Millisecond {
		t.Fatalf("swipe explicit hold_after_ms gap = %v, want >= 100ms", releaseDelay)
	}
}

func TestTouchGestureBackStartsAtLeftPhysicalEdge(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"back","steps":2,"duration_ms":0,"hold_before_ms":0,"hold_after_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (pre-move, press, 2 moves, repeated release)", len(reports), 2+2+touchReleaseReportCount)
	}
	if reports[0].x > 100 {
		t.Fatalf("back start x = %d, want near left physical edge", reports[0].x)
	}
	if reports[0].y != uint16(absMouseMaxPos/2+1) {
		t.Fatalf("back start y = %d, want center", reports[0].y)
	}
	if reports[3].x < uint16(absMouseMaxPos*70/100) {
		t.Fatalf("back end x = %d, want a long swipe across the screen", reports[3].x)
	}
	for i := 4; i < len(reports); i++ {
		if reports[i].buttons != 0 {
			t.Fatalf("back release report %d buttons = %d, want 0", i-3, reports[i].buttons)
		}
	}
}

func TestTouchGestureHomeStartsAtBottomPhysicalEdge(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"home","steps":2,"duration_ms":0,"hold_before_ms":0,"hold_after_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d (pre-move, press, 2 moves, repeated release)", len(reports), 2+2+touchReleaseReportCount)
	}
	if reports[0].y < uint16(absMouseMaxPos*995/1000) {
		t.Fatalf("home start y = %d, want near bottom physical edge", reports[0].y)
	}
	if reports[0].x != uint16(absMouseMaxPos/2+1) {
		t.Fatalf("home start x = %d, want center", reports[0].x)
	}
	if reports[3].y > uint16(absMouseMaxPos/4) {
		t.Fatalf("home end y = %d, want a long upward swipe", reports[3].y)
	}
	for i := 4; i < len(reports); i++ {
		if reports[i].buttons != 0 {
			t.Fatalf("home release report %d buttons = %d, want 0", i-3, reports[i].buttons)
		}
	}
}

func TestTouchGestureDescriptionDocumentsEdgeGestureAliases(t *testing.T) {
	desc := (&TouchGestureTool{}).Description()
	for _, want := range []string{`"back"`, `"home"`, "x=1", "y=999"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestTouchGestureDefaultEdgeGestureRejectsInvalidCoordSpace(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"back","coord_space":"typo"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(out, `unsupported coord_space: "typo"`) {
		t.Fatalf("output = %q, want unsupported coord_space error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestTouchGestureDragKeepsZeroHoldBeforeMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"drag","start":{"x":100,"y":100},"end":{"x":900,"y":900},"steps":2,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}

	// Writes: pre-move, press, move1, move2, repeated release. Drag must not inherit
	// the swipe hold defaults, so the press-to-first-move and final-move-to-
	// first-release gaps are both ~0.
	times := w.writeTimes()
	if len(times) < 4 {
		t.Fatalf("len(times) = %d, want >= 4", len(times))
	}
	gap := times[2].Sub(times[1])
	if gap > 20*time.Millisecond {
		t.Fatalf("drag press-to-first-move gap = %v, want < 20ms (drag must not inherit swipe hold)", gap)
	}
	firstRelease := len(times) - touchReleaseReportCount
	releaseGap := times[firstRelease].Sub(times[firstRelease-1])
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
	pc := testPointerController(dev, &pointerState{})
	err := dragPointer(pc, start, end, 0x01, 0, 0, 0, 3)
	if err == nil {
		t.Fatal("expected error from dragPointer")
	}
	// The release report must have been attempted even though a move failed.
	if writer.writeCount < failAfter+touchReleaseReportCount {
		t.Fatalf("writeCount = %d, expected at least %d (release must be attempted after move failure)", writer.writeCount, failAfter+touchReleaseReportCount)
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
	x, y, err := resolvePointerPosition(screen, 2000, 3000, "", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("expected no error on stale auto, got %v", err)
	}
	if x != 2000 || y != 3000 {
		t.Fatalf("auto fallback = (%d,%d), want (2000,3000)", x, y)
	}
}
