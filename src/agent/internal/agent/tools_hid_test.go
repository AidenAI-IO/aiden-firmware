package agent

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

func TestToolSetUpdateDeviceEnvironmentTracksPhoneScreenInfo(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	env := &PhoneEnvironment{
		Screen: PhoneScreenInfo{
			WidthPixels:        intPtr(1080),
			HeightPixels:       intPtr(1920),
			NativeWidthPixels:  intPtr(1080),
			NativeHeightPixels: intPtr(1920),
		},
	}

	tools.UpdateDeviceEnvironment(env)

	screen := tools.screen.PhoneScreenInfo()
	if screen.WidthPixels == nil || screen.HeightPixels == nil {
		t.Fatalf("screen info = %+v, want width/height pixels", screen)
	}
	if *screen.WidthPixels != 1080 || *screen.HeightPixels != 1920 {
		t.Fatalf("screen pixels = %v x %v, want 1080 x 1920", *screen.WidthPixels, *screen.HeightPixels)
	}

	tools.UpdateDeviceEnvironment(nil)

	screen = tools.screen.PhoneScreenInfo()
	if screen.WidthPixels != nil || screen.HeightPixels != nil || screen.NativeWidthPixels != nil || screen.NativeHeightPixels != nil || screen.Width != nil || screen.Height != nil {
		t.Fatalf("screen info should be cleared, got %+v", screen)
	}
}

func TestHIDToolsExposeStructuredSchemas(t *testing.T) {
	for name, tool := range map[string]structuredInputTool{
		"keyboard_tap":  &KeyboardTapTool{},
		"keyboard_text": &KeyboardTextTool{},
		"mouse_click":   &MouseClickTool{},
		"mouse_move":    &MouseMoveTool{},
		"mouse_scroll":  &MouseScrollTool{},
		"touch_gesture": &TouchGestureTool{},
	} {
		schema := tool.ArgsSchema()
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s missing schema properties: %#v", name, schema)
		}
	}
}

func TestKeyboardTapSchemaRequiresKeysArray(t *testing.T) {
	schema := (&KeyboardTapTool{}).ArgsSchema()
	props := schema["properties"].(map[string]any)
	keys := props["keys"].(map[string]any)
	if keys["type"] != "array" {
		t.Fatalf("keys schema type = %#v, want array", keys["type"])
	}
	description, _ := keys["description"].(string)
	for _, want := range []string{"Use backspace for ordinary text deletion", "delete is forward-delete"} {
		if !strings.Contains(description, want) {
			t.Fatalf("keys schema description missing %q: %s", want, description)
		}
	}
	items := keys["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("keys items type = %#v, want string", items["type"])
	}
}

func TestTouchGestureSchemaRequiresNamedPointCoordinates(t *testing.T) {
	schema := (&TouchGestureTool{}).ArgsSchema()
	props := schema["properties"].(map[string]any)
	point := props["point"].(map[string]any)
	if point["type"] != "object" {
		t.Fatalf("point type = %#v, want object", point["type"])
	}
	pointProps := point["properties"].(map[string]any)
	if _, ok := pointProps["x"]; !ok {
		t.Fatalf("point schema missing x: %#v", pointProps)
	}
	if _, ok := pointProps["y"]; !ok {
		t.Fatalf("point schema missing y: %#v", pointProps)
	}
}

type recordingADBRunner struct {
	commands [][]string
	timeouts []time.Duration
	handler  func(args []string) (string, error)
}

func (r *recordingADBRunner) run(ctx context.Context, _ string, args ...string) ([]byte, []byte, error) {
	copied := append([]string(nil), args...)
	r.commands = append(r.commands, copied)
	if deadline, ok := ctx.Deadline(); ok {
		r.timeouts = append(r.timeouts, time.Until(deadline))
	} else {
		r.timeouts = append(r.timeouts, 0)
	}
	if r.handler == nil {
		return nil, nil, nil
	}
	out, err := r.handler(copied)
	if err != nil {
		return []byte(out), nil, err
	}
	return []byte(out), nil, nil
}

func newTestADBInputController(t *testing.T, screen *screenState, runner *recordingADBRunner) *ADBInputController {
	t.Helper()
	t.Setenv("AIDEN_ADB_PATH", "/fake/adb")
	t.Setenv("AIDEN_ADB_SERIAL", "serial123")
	if screen == nil {
		screen = &screenState{}
	}
	return &ADBInputController{
		screen: screen,
		client: NewADBScreenClient(),
		runADB: runner.run,
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSliceMatrixEqual(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !stringSlicesEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestADBMouseClickUsesInputTapWithNormalizedCoordinates(t *testing.T) {
	screen := &screenState{}
	screen.UpdatePhoneScreenInfo(PhoneScreenInfo{WidthPixels: intPtr(1080), HeightPixels: intPtr(2400)})
	runner := &recordingADBRunner{}
	tool := &MouseClickTool{screen: screen, adb: newTestADBInputController(t, screen, runner)}

	out, err := tool.Call(context.Background(), `{"x":500,"y":250,"coord_space":"normalized"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "tap", "540", "600"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBTouchGestureSwipeUsesInputSwipe(t *testing.T) {
	screen := &screenState{}
	screen.UpdatePhoneScreenInfo(PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: screen, adb: newTestADBInputController(t, screen, runner)}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":100,"y":900},"end":{"x":900,"y":100},"duration_ms":321}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "swipe", "100", "900", "900", "100", "321"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBTouchGestureLongPressExtendsCommandTimeout(t *testing.T) {
	screen := &screenState{}
	screen.UpdatePhoneScreenInfo(PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: screen, adb: newTestADBInputController(t, screen, runner)}

	out, err := tool.Call(context.Background(), `{"type":"long_press","point":{"x":50,"y":50},"duration_ms":9000}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "swipe", "50", "50", "50", "50", "9000"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
	if len(runner.timeouts) != 1 {
		t.Fatalf("adb timeouts = %#v, want one timeout", runner.timeouts)
	}
	if runner.timeouts[0] < 11*time.Second {
		t.Fatalf("adb timeout = %v, want at least 11s for 9s gesture", runner.timeouts[0])
	}
}

func TestADBTouchGestureBackUsesKeyevent(t *testing.T) {
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: &screenState{}, adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(context.Background(), `{"type":"back"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "keyevent", "4"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBKeyboardTapUsesKeyCombinationForChords(t *testing.T) {
	runner := &recordingADBRunner{}
	tool := &KeyboardTapTool{adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(context.Background(), `{"keys":["ctrl","c"],"hold_ms":77}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "keycombination", "-t", "77", "KEYCODE_CTRL_LEFT", "KEYCODE_C"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBKeyboardTextUsesADBKeyboardBroadcastAndRestoresIME(t *testing.T) {
	origSleep := sleepMs
	sleepMs = func(int) {}
	defer func() { sleepMs = origSleep }()

	runner := &recordingADBRunner{handler: func(args []string) (string, error) {
		if strings.Join(args, " ") == "-s serial123 shell settings get secure default_input_method" {
			return "com.example/.Keyboard", nil
		}
		return "", nil
	}}
	tool := &KeyboardTextTool{adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(context.Background(), `{"text":"Hello!"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("Hello!"))
	want := [][]string{
		{"-s", "serial123", "shell", "settings", "get", "secure", "default_input_method"},
		{"-s", "serial123", "shell", "ime", "set", adbKeyboardIME},
		{"-s", "serial123", "shell", "am", "broadcast", "-a", "ADB_INPUT_B64", "--es", "msg", encoded},
		{"-s", "serial123", "shell", "ime", "set", "com.example/.Keyboard"},
	}
	if !stringSliceMatrixEqual(runner.commands, want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBKeyboardTextFallsBackToKeyEventsWhenADBKeyboardUnavailable(t *testing.T) {
	runner := &recordingADBRunner{handler: func(args []string) (string, error) {
		if strings.Join(args, " ") == "-s serial123 shell settings get secure default_input_method" {
			return "com.example/.Keyboard", nil
		}
		if strings.Join(args, " ") == "-s serial123 shell ime set "+adbKeyboardIME {
			return "Unknown input method", errors.New("exit status 1")
		}
		return "", nil
	}}
	tool := &KeyboardTextTool{adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(context.Background(), `{"text":"A z!"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := [][]string{
		{"-s", "serial123", "shell", "settings", "get", "secure", "default_input_method"},
		{"-s", "serial123", "shell", "ime", "set", adbKeyboardIME},
		{"-s", "serial123", "shell", "input", "keycombination", "-t", "50", "KEYCODE_SHIFT_LEFT", "KEYCODE_A"},
		{"-s", "serial123", "shell", "input", "keyevent", "KEYCODE_SPACE"},
		{"-s", "serial123", "shell", "input", "keyevent", "KEYCODE_Z"},
		{"-s", "serial123", "shell", "input", "keycombination", "-t", "50", "KEYCODE_SHIFT_LEFT", "KEYCODE_1"},
	}
	if !stringSliceMatrixEqual(runner.commands, want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestParseADBWMSizePrefersOverrideSize(t *testing.T) {
	size, ok := parseADBWMSize("Physical size: 1080x2400\nOverride size: 720x1600\n")
	if !ok {
		t.Fatal("parseADBWMSize ok = false")
	}
	if size.width != 720 || size.height != 1600 {
		t.Fatalf("size = %+v, want 720x1600", size)
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

	// Pixel coords are now relative to cropped image (608x1080).
	// x=304 in crop = center of active area (was x=960 in full frame).
	x, y, err := resolvePointerPosition(screen, 304, 540, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	wantX := scalePixelToAbsolute(304, 608)
	wantY := scalePixelToAbsolute(540, 1080)
	if x != wantX {
		t.Fatalf("x = %d, want %d", x, wantX)
	}
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionPixelUses720pActiveArea(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1280, 720, screenActiveArea{X: 320, Y: 0, Width: 640, Height: 720, Valid: true})

	// Pixel coords relative to cropped image (640x720).
	// x=320 in crop = center (was x=640 in full frame).
	x, y, err := resolvePointerPosition(screen, 320, 360, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	wantX := scalePixelToAbsolute(320, 640)
	wantY := scalePixelToAbsolute(360, 720)
	if x != wantX {
		t.Fatalf("x = %d, want %d", x, wantX)
	}
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionNormalizedUsesActiveArea(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	x, y, err := resolvePointerPosition(screen, 0, 500, "normalized", coordinateSpaceNormalized)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	if x != 0 {
		t.Fatalf("x = %d, want 0 at left edge of active_area", x)
	}
	wantY := scalePixelToAbsolute(float64(1079)/2, 1080)
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionTouchscreenNormalizedUsesFrameSpaceWithinActiveArea(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	x, y, err := resolvePointerPositionForSurface(screen, true, 627, 180, "normalized", coordinateSpaceNormalized)
	if err != nil {
		t.Fatalf("resolvePointerPositionForSurface returned error: %v", err)
	}

	wantX := scalePixelToAbsolute(float64(656)+(627.0/1000.0)*float64(608-1), 1920)
	wantY := scalePixelToAbsolute((180.0/1000.0)*float64(1080-1), 1080)
	if x != wantX {
		t.Fatalf("x = %d, want %d", x, wantX)
	}
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionTouchscreenPixelUsesFrameSpace(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	// Pixel coords relative to cropped image (608x1080).
	// x=304 in crop = center of active area → full frame x = 656+304 = 960.
	x, y, err := resolvePointerPositionForSurface(screen, true, 304, 540, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPositionForSurface returned error: %v", err)
	}

	wantX := scalePixelToAbsolute(656+304, 1920)
	wantY := scalePixelToAbsolute(540, 1080)
	if x != wantX {
		t.Fatalf("x = %d, want %d (touchscreen pixel should use full frame)", x, wantX)
	}
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionPixelRejectsBlackBar(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	// With pixel relative to cropped image, "black bar" doesn't exist.
	// But x=700 exceeds cropped width (608), so it should still error.
	_, _, err := resolvePointerPosition(screen, 700, 540, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for pixel coordinates outside cropped image bounds")
	}
	if !strings.Contains(err.Error(), "outside screenshot bounds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScalePixelToAbsoluteUsesActiveAreaYOffset(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1280, 720, screenActiveArea{X: 0, Y: 72, Width: 1280, Height: 576, Valid: true})

	// Pixel coords relative to cropped image (1280x576).
	// y=94 in crop (was y=166 in full frame, 166-72=94).
	x, y, err := resolvePointerPosition(screen, 919, 94, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("resolvePointerPosition returned error: %v", err)
	}
	expectedX := scalePixelToAbsolute(919, 1280)
	expectedY := scalePixelToAbsolute(94, 576)
	if x != expectedX {
		t.Fatalf("x = %d, want %d", x, expectedX)
	}
	if y != expectedY {
		t.Fatalf("y = %d, want %d", y, expectedY)
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
	if !strings.Contains(err.Error(), "outside screenshot bounds 431x947") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolvePointerPositionPixelRelativeToCroppedImage(t *testing.T) {
	screen := &screenState{}
	// Full frame 1920x1080, active area starts at x=712, width=496
	// LLM sees cropped 496x1080 image, passes coords relative to that
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 712, Y: 0, Width: 496, Height: 1080, Valid: true})

	// x=248 is center of cropped image, y=1030 is near bottom
	x, y, err := resolvePointerPosition(screen, 248, 1030, "pixel", coordinateSpaceAuto)
	if err != nil {
		t.Fatalf("pixel coords relative to cropped image should work: %v", err)
	}

	wantX := scalePixelToAbsolute(248, 496)
	wantY := scalePixelToAbsolute(1030, 1080)
	if x != wantX {
		t.Fatalf("x = %d, want %d", x, wantX)
	}
	if y != wantY {
		t.Fatalf("y = %d, want %d", y, wantY)
	}
}

func TestResolvePointerPositionPixelRejectsOutsideCroppedBounds(t *testing.T) {
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 712, Y: 0, Width: 496, Height: 1080, Valid: true})

	// x=500 exceeds cropped image width 496
	_, _, err := resolvePointerPosition(screen, 500, 540, "pixel", coordinateSpaceAuto)
	if err == nil {
		t.Fatal("expected error for x=500 outside cropped image width 496")
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

func TestTouchscreenTapUsesFrameSpaceForActiveArea(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screen := &screenState{}
	screen.UpdateActiveArea(1920, 1080, screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: screen}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":627,"y":180}}`)
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
	wantX := uint16(scalePixelToAbsolute(float64(656)+(627.0/1000.0)*float64(608-1), 1920))
	wantY := uint16(scalePixelToAbsolute((180.0/1000.0)*float64(1080-1), 1080))
	if reports[0].flags != 0x03 || reports[0].contactID != 1 || reports[0].x != wantX || reports[0].y != wantY {
		t.Fatalf("down report = %+v, want touch at (%d,%d)", reports[0], wantX, wantY)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i].flags != 0x00 || reports[i].contactID != 1 || reports[i].x != wantX || reports[i].y != wantY {
			t.Fatalf("up report %d = %+v, want release at (%d,%d)", i, reports[i], wantX, wantY)
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

func TestKeyboardTextDescriptionWarnsAgainstNonASCII(t *testing.T) {
	desc := (&KeyboardTextTool{}).Description()
	for _, want := range []string{
		"ASCII",
		"Do NOT pass non-ASCII",
		"enter_text_in_field",
		"Do not transliterate Chinese/CJK targets to pinyin",
		`{"text":"App Store"}`,
		"do not pass a bare string",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	for _, unexpected := range []string{
		"Type a string of text",
		"hello world",
		"use pinyin",
	} {
		if strings.Contains(desc, unexpected) {
			t.Fatalf("description should not contain misleading phrase %q:\n%s", unexpected, desc)
		}
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
		output: `{"ok":true,"stable":false,"elapsed_ms":3001,"screen_changed":true,"last_diff":18.5}`,
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
	if result.ScreenChanged == nil || !*result.ScreenChanged {
		t.Fatalf("ScreenChanged = %#v, want true", result.ScreenChanged)
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
	if len(waitStable.inputs) != 1 || waitStable.inputs[0] != `{"timeout_ms":3000,"stable_ms":500,"diff_threshold":6}` {
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
		output: `{"ok":true,"stable":true,"elapsed_ms":600,"screen_changed":false}`,
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
	if result.ScreenChanged == nil || *result.ScreenChanged {
		t.Fatalf("ScreenChanged = %#v, want false", result.ScreenChanged)
	}
}

func TestPostActionScreenshotToolSkipsScreenshotOnActionErrorOutput(t *testing.T) {
	action := &stubTool{name: "mouse_click", output: "error: invalid input"}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionScreenshotTool(action, screenshot, 0)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "invalid input" {
		t.Fatalf("Call output = %q, want structured error message", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeToolExecutionFailed || got.Message != out {
		t.Fatalf("ToolError = %+v, want tool_execution_failed with output message", got)
	}
	if len(screenshot.inputs) != 0 {
		t.Fatalf("screenshot should not be called on action error, got inputs %#v", screenshot.inputs)
	}
}

func TestPostActionScreenshotToolSkipsScreenshotOnStructuredActionError(t *testing.T) {
	toolErr := NewToolError(CodeInvalidArguments, "invalid touch gesture")
	action := &contextToolErrorStub{name: "touch_gesture", toolErr: toolErr}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionScreenshotTool(action, screenshot, 0)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != toolErr.Message {
		t.Fatalf("Call output = %q, want structured error message %q", out, toolErr.Message)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments {
		t.Fatalf("context ToolError = %+v, want invalid_arguments", got)
	}
	if len(screenshot.inputs) != 0 {
		t.Fatalf("screenshot should not be called on structured action error, got inputs %#v", screenshot.inputs)
	}
}

type contextToolErrorStub struct {
	name    string
	toolErr *ToolError
}

func (t *contextToolErrorStub) Name() string { return t.name }

func (t *contextToolErrorStub) Description() string { return "structured error stub" }

func (t *contextToolErrorStub) Call(ctx context.Context, input string) (string, error) {
	SetToolError(ctx, t.toolErr)
	return toolErrorString(t.toolErr), nil
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

func TestHIDDeviceWriteReopensWhenDeviceNodeRecreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidg0")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create test hid device: %v", err)
	}
	dev := NewHIDDevice(path)
	dev.refreshState = ""

	if err := dev.Write([]byte{1, 2, 3}); err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove test hid device: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("recreate test hid device: %v", err)
	}

	if err := dev.Write([]byte{4, 5, 6}); err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	dev.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recreated test hid device: %v", err)
	}
	if string(data) != string([]byte{4, 5, 6}) {
		t.Fatalf("recreated device data = %v, want only second report", data)
	}
}

func TestHIDDeviceWriteReopensAfterWatchdogRefreshState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hidg0")
	statePath := filepath.Join(dir, "aiden_usb_ecm_watchdog.state")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create test hid device: %v", err)
	}
	dev := NewHIDDevice(path)
	dev.refreshState = statePath

	if err := dev.Write([]byte{1, 2, 3}); err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("last_refresh_result=ok\n"), 0o600); err != nil {
		t.Fatalf("write watchdog state: %v", err)
	}
	refreshTime := dev.openedAt.Add(time.Second)
	if err := os.Chtimes(statePath, refreshTime, refreshTime); err != nil {
		t.Fatalf("set watchdog state mtime: %v", err)
	}

	if err := dev.Write([]byte{4, 5, 6}); err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	dev.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test hid device: %v", err)
	}
	if string(data) != string([]byte{4, 5, 6}) {
		t.Fatalf("device data = %v, want second report after reopen", data)
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

func TestMouseScrollToolRejectsOutOfRangeDelta(t *testing.T) {
	tool := &MouseScrollTool{}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"delta":128}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "delta must be between -127 and 127" {
		t.Fatalf("output = %q", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
		t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
	}
}

func TestMouseScrollToolRejectsTouchscreenPointerMode(t *testing.T) {
	tool := &MouseScrollTool{pc: testTouchscreenPointerController(nil, &pointerState{})}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"delta":-3}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	want := "mouse_scroll is unsupported when pointer_mode is touchscreen; use touch_gesture"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != want {
		t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
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

func TestKeyboardTapSupportsAndroidBackAlias(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

	out, err := tool.Call(context.Background(), `{"keys":["KEYCODE_BACK"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile keyboard path: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
	}
	data, err := os.ReadFile(androidPath)
	if err != nil {
		t.Fatalf("ReadFile android path: %v", err)
	}
	if len(data) != 4 {
		t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
	}
	if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap["android_back"] {
		t.Fatalf("android back usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap["android_back"])
	}
	if data[2] != 0 || data[3] != 0 {
		t.Fatalf("android back release = %v, want [0 0]", data[2:4])
	}
}

func TestKeyboardTapSupportsAndroidVolumeAlias(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

	out, err := tool.Call(context.Background(), `{"keys":["KEYCODE_VOLUME_UP"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile keyboard path: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
	}
	data, err := os.ReadFile(androidPath)
	if err != nil {
		t.Fatalf("ReadFile android path: %v", err)
	}
	if len(data) != 4 {
		t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
	}
	if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap["volume_up"] {
		t.Fatalf("android volume_up usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap["volume_up"])
	}
}

func TestKeyboardTapSupportsAndroidAppSwitchAlias(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

	out, err := tool.Call(context.Background(), `{"keys":["KEYCODE_APP_SWITCH"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile keyboard path: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
	}
	data, err := os.ReadFile(androidPath)
	if err != nil {
		t.Fatalf("ReadFile android path: %v", err)
	}
	if len(data) != 4 {
		t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
	}
	if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap["app_switch"] {
		t.Fatalf("android app_switch usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap["app_switch"])
	}
}

func TestKeyboardTapSupportsAdditionalAndroidKeycodeAliases(t *testing.T) {
	testCases := []struct {
		input  string
		mapKey string
	}{
		{input: "KEYCODE_SLEEP", mapKey: "sleep"},
		{input: "KEYCODE_MEDIA_PLAY_PAUSE", mapKey: "media_play_pause"},
		{input: "KEYCODE_MEDIA_STOP", mapKey: "media_stop"},
		{input: "KEYCODE_MEDIA_NEXT", mapKey: "media_next"},
		{input: "KEYCODE_MEDIA_PREVIOUS", mapKey: "media_previous"},
		{input: "KEYCODE_MEDIA_REWIND", mapKey: "media_rewind"},
		{input: "KEYCODE_MEDIA_FAST_FORWARD", mapKey: "media_fast_forward"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			androidDev, androidPath := newTestHIDDevice(t)
			tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

			out, err := tool.Call(context.Background(), fmt.Sprintf(`{"keys":["%s"]}`, tc.input))
			if err != nil {
				t.Fatalf("Call failed: %v", err)
			}
			if out != "ok" {
				t.Fatalf("unexpected output: %s", out)
			}

			dev.Close()
			androidDev.Close()
			if data, err := os.ReadFile(path); err != nil {
				t.Fatalf("ReadFile keyboard path: %v", err)
			} else if len(data) != 0 {
				t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
			}
			data, err := os.ReadFile(androidPath)
			if err != nil {
				t.Fatalf("ReadFile android path: %v", err)
			}
			if len(data) != 4 {
				t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
			}
			if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap[tc.mapKey] {
				t.Fatalf("usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap[tc.mapKey])
			}
		})
	}
}

func TestKeyboardTapSupportsAndroidSettingsUsageAlias(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

	out, err := tool.Call(context.Background(), `{"keys":["KEY_USAGE_SETTINGS"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile keyboard path: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
	}
	data, err := os.ReadFile(androidPath)
	if err != nil {
		t.Fatalf("ReadFile android path: %v", err)
	}
	if len(data) != 4 {
		t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
	}
	if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap["key_usage_settings"] {
		t.Fatalf("android settings usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap["key_usage_settings"])
	}
}

func TestKeyboardTapSupportsAndroidLanguageSwitchUsageAlias(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

	out, err := tool.Call(context.Background(), `{"keys":["KEY_USAGE_LANGUAGE_SWITCH"]}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("ReadFile keyboard path: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
	}
	data, err := os.ReadFile(androidPath)
	if err != nil {
		t.Fatalf("ReadFile android path: %v", err)
	}
	if len(data) != 4 {
		t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
	}
	if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap["key_usage_language_switch"] {
		t.Fatalf("android language_switch usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap["key_usage_language_switch"])
	}
}

func TestKeyboardTapSupportsAdditionalAndroidUsageAliases(t *testing.T) {
	testCases := []struct {
		input  string
		mapKey string
	}{
		{input: "KEY_USAGE_SCREENSHOT", mapKey: "key_usage_screenshot"},
		{input: "KEY_USAGE_WINDOW", mapKey: "key_usage_window"},
		{input: "KEY_USAGE_BRIGHTNESS_UP", mapKey: "key_usage_brightness_up"},
		{input: "KEY_USAGE_BRIGHTNESS_DOWN", mapKey: "key_usage_brightness_down"},
		{input: "KEY_USAGE_DICTATE", mapKey: "key_usage_dictate"},
		{input: "KEY_USAGE_EMOJI_PICKER", mapKey: "key_usage_emoji_picker"},
		{input: "KEY_USAGE_MEDIA_AUDIO_TRACK", mapKey: "key_usage_media_audio_track"},
		{input: "KEY_USAGE_PROFILE_SWITCH", mapKey: "key_usage_profile_switch"},
		{input: "KEY_USAGE_NEW", mapKey: "key_usage_new"},
		{input: "KEY_USAGE_CLOSE", mapKey: "key_usage_close"},
		{input: "KEY_USAGE_PRINT", mapKey: "key_usage_print"},
		{input: "KEY_USAGE_REFRESH", mapKey: "key_usage_refresh"},
		{input: "KEY_USAGE_FULLSCREEN", mapKey: "key_usage_fullscreen"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			androidDev, androidPath := newTestHIDDevice(t)
			tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "touchscreen"}

			out, err := tool.Call(context.Background(), fmt.Sprintf(`{"keys":["%s"]}`, tc.input))
			if err != nil {
				t.Fatalf("Call failed: %v", err)
			}
			if out != "ok" {
				t.Fatalf("unexpected output: %s", out)
			}

			dev.Close()
			androidDev.Close()
			if data, err := os.ReadFile(path); err != nil {
				t.Fatalf("ReadFile keyboard path: %v", err)
			} else if len(data) != 0 {
				t.Fatalf("standard keyboard path bytes = %v, want none for android extension key", data)
			}
			data, err := os.ReadFile(androidPath)
			if err != nil {
				t.Fatalf("ReadFile android path: %v", err)
			}
			if len(data) != 4 {
				t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
			}
			if got := uint16(data[0]) | uint16(data[1])<<8; got != androidExtensionUsageMap[tc.mapKey] {
				t.Fatalf("usage = 0x%04x, want 0x%04x", got, androidExtensionUsageMap[tc.mapKey])
			}
		})
	}
}

func TestKeyboardTapAbsolutePointerModeAllowsMediaKeySubset(t *testing.T) {
	testCases := []struct {
		input  string
		mapKey string
	}{
		{input: "KEYCODE_VOLUME_MUTE", mapKey: "volume_mute"},
		{input: "KEYCODE_VOLUME_UP", mapKey: "volume_up"},
		{input: "KEYCODE_VOLUME_DOWN", mapKey: "volume_down"},
		{input: "KEYCODE_MEDIA_PLAY_PAUSE", mapKey: "media_play_pause"},
		{input: "KEYCODE_MEDIA_STOP", mapKey: "media_stop"},
		{input: "KEYCODE_MEDIA_NEXT", mapKey: "media_next"},
		{input: "KEYCODE_MEDIA_PREVIOUS", mapKey: "media_previous"},
		{input: "KEYCODE_MEDIA_REWIND", mapKey: "media_rewind"},
		{input: "KEYCODE_MEDIA_FAST_FORWARD", mapKey: "media_fast_forward"},
		{input: "KEY_USAGE_SCREENSHOT", mapKey: "key_usage_screenshot"},
		{input: "KEY_USAGE_BRIGHTNESS_UP", mapKey: "key_usage_brightness_up"},
		{input: "KEY_USAGE_BRIGHTNESS_DOWN", mapKey: "key_usage_brightness_down"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			androidDev, androidPath := newTestHIDDevice(t)
			tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "absolute"}

			out, err := tool.Call(context.Background(), fmt.Sprintf(`{"keys":["%s"]}`, tc.input))
			if err != nil {
				t.Fatalf("Call failed: %v", err)
			}
			if out != "ok" {
				t.Fatalf("unexpected output: %s", out)
			}

			dev.Close()
			androidDev.Close()
			if data, err := os.ReadFile(path); err != nil {
				t.Fatalf("ReadFile keyboard path: %v", err)
			} else if len(data) != 0 {
				t.Fatalf("standard keyboard path bytes = %v, want none for absolute media key", data)
			}
			data, err := os.ReadFile(androidPath)
			if err != nil {
				t.Fatalf("ReadFile android path: %v", err)
			}
			if len(data) != 4 {
				t.Fatalf("report bytes = %d, want 4 (consumer usage + release)", len(data))
			}
			if got := uint16(data[0]) | uint16(data[1])<<8; got != absolutePointerModeExtensionReports[tc.mapKey] {
				t.Fatalf("report mask = 0x%04x, want 0x%04x", got, absolutePointerModeExtensionReports[tc.mapKey])
			}
		})
	}
}

func TestKeyboardTapAbsolutePointerModeRejectsAndroidNavigationKeys(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	androidDev, _ := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, androidDev: androidDev, pointerMode: "absolute"}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"keys":["KEYCODE_BACK"]}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, `hid.pointer_mode="touchscreen"`) || !strings.Contains(out, absolutePointerModeExtensionKeyList) {
		t.Fatalf("output = %q, want absolute pointer mode allow-list error", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
		t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
	}
}

func TestKeyboardTapRejectsUnsupportedAndroidKeycodeAliases(t *testing.T) {
	testCases := []struct {
		input      string
		messageSub string
	}{
		{input: "KEYCODE_WAKEUP", messageSub: "Generic Desktop/System Control HID path"},
		{input: "KEYCODE_SOFT_SLEEP", messageSub: "no verified standard Consumer Control usage"},
		{input: "KEYCODE_NOTIFICATION", messageSub: "notification center has no verified standard Consumer Control usage"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			ctx, _ := WithToolError(context.Background())
			tool := &KeyboardTapTool{}

			out, err := tool.Call(ctx, fmt.Sprintf(`{"keys":["%s"]}`, tc.input))
			if err != nil {
				t.Fatalf("Call returned error: %v", err)
			}
			if !strings.Contains(out, strings.ToLower(tc.input)) || !strings.Contains(out, tc.messageSub) {
				t.Fatalf("output = %q, want alias and reason substring %q", out, tc.messageSub)
			}
			if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
				t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
			}
		})
	}
}

func TestKeyboardTapRejectsAndroidExtensionWithoutAndroidDevice(t *testing.T) {
	tool := &KeyboardTapTool{pointerMode: "touchscreen"}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"keys":["KEYCODE_HOME"]}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "android extension keyboard device is not configured") || !strings.Contains(out, "hid.android_keyboard_device") {
		t.Fatalf("output = %q, want android extension device error", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeModuleUnavailable || got.Message != out {
		t.Fatalf("ToolError = %+v, want module_unavailable with output message", got)
	}
}

func TestKeyboardTapRejectsMixedAndroidExtensionChord(t *testing.T) {
	tool := &KeyboardTapTool{}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"keys":["ctrl","KEYCODE_BACK"]}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, `Android extension key "android_back" cannot be combined`) {
		t.Fatalf("output = %q, want mixed-chord error", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
		t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
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
	// The description keeps only the load-bearing quick_action disambiguation and swipe-direction rule.
	for _, want := range []string{"Prefer quick_action", "finger movement"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// Edge-alias coordinates (back x=1, home y=999) now live in the type ArgsSchema field.
	props, _ := (&TouchGestureTool{}).ArgsSchema()["properties"].(map[string]any)
	typeSchema, _ := props["type"].(map[string]any)
	typeDesc, _ := typeSchema["description"].(string)
	for _, want := range []string{"back", "home", "x=1", "y=999"} {
		if !strings.Contains(typeDesc, want) {
			t.Fatalf("type schema missing %q:\n%s", want, typeDesc)
		}
	}
}

func TestMouseClickDescriptionDocumentsTargetCenter(t *testing.T) {
	desc := (&MouseClickTool{}).Description()
	for _, want := range []string{"normalized", "latest screenshot", "visual center", "post-action screenshot"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestKeyboardTapDescriptionDocumentsQuickActionFallback(t *testing.T) {
	desc := (&KeyboardTapTool{}).Description()
	for _, want := range []string{"Prefer quick_action", "custom key input"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// Key mechanics (backspace/forward-delete, modifiers, key list) now live in the keys ArgsSchema field.
	keysDesc := keyboardTapKeysSchemaDescription(t)
	for _, want := range []string{"backspace", "forward-delete", "modifier", "modifier-only"} {
		if !strings.Contains(keysDesc, want) {
			t.Fatalf("keys schema missing %q:\n%s", want, keysDesc)
		}
	}
}

func TestKeyboardTapDescriptionReferencesAndroidGuidePage(t *testing.T) {
	keysDesc := keyboardTapKeysSchemaDescription(t)
	for _, want := range []string{"KEYCODE_*", "KEY_USAGE_*", "Android key guide", "single-key taps only", "hid.pointer_mode is absolute", "KEY_USAGE_SCREENSHOT"} {
		if !strings.Contains(keysDesc, want) {
			t.Fatalf("keys schema missing %q:\n%s", want, keysDesc)
		}
	}
}

func keyboardTapKeysSchemaDescription(t *testing.T) string {
	t.Helper()
	props, ok := (&KeyboardTapTool{}).ArgsSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatal("keyboard_tap schema missing properties")
	}
	keys, ok := props["keys"].(map[string]any)
	if !ok {
		t.Fatal("keyboard_tap schema missing keys property")
	}
	desc, _ := keys["description"].(string)
	return desc
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

func TestTouchGestureSwipeRejectsPointInsteadOfStartEnd(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","point":{"x":500,"y":500}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "swipe requires start and end, not point") {
		t.Fatalf("output = %q, want swipe start/end error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestTouchGestureAcceptsArrayPointFormat(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":[500,250]}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+touchReleaseReportCount)
	}
	if reports[0].x != 16384 || reports[0].y != 8192 {
		t.Fatalf("point = (%d,%d), want (16384,8192)", reports[0].x, reports[0].y)
	}
}
