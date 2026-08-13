package agent

import (
	"aiden-agent/internal/agent/screen"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestResolvePointerPositionNormalized(t *testing.T) {
	x, y, err := resolvePointerPosition(nil, 500, 250)
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
		Screen: screen.PhoneScreenInfo{
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

func TestBuiltinToolSetWiresConfiguredKeyboardLayout(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{KeyboardLayout: keyboardLayoutAZERTY}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	keyboardText, ok := tools.textInputHW.keyboardText.(*KeyboardTextTool)
	if !ok {
		t.Fatalf("keyboardText = %T, want *KeyboardTextTool", tools.textInputHW.keyboardText)
	}
	if got := keyboardText.keyboardLayout; got != keyboardLayoutAZERTY {
		t.Fatalf("keyboard_text layout = %q, want %q", got, keyboardLayoutAZERTY)
	}
	keyboardTap, ok := tools.textInputHW.keyboardTap.(*KeyboardTapTool)
	if !ok {
		t.Fatalf("keyboardTap = %T, want *KeyboardTapTool", tools.textInputHW.keyboardTap)
	}
	if got := keyboardTap.keyboardLayout; got != keyboardLayoutAZERTY {
		t.Fatalf("keyboard_tap layout = %q, want %q", got, keyboardLayoutAZERTY)
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
		"wheel_nudge":   &WheelNudgeTool{},
	} {
		schema := tool.ArgsSchema()
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s missing schema properties: %#v", name, schema)
		}
	}
}

func TestWheelNudgeWritesLowInertiaDrag(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":650,"remaining_gap":3,"current_value":10,"target_value":13,"cycle_size":24,"cycle_start":0,"row_spacing":70,"value_step":1,"center_y":500}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	// gap=3 uses two measured rows: 2 * 70 = 140 normalized units.
	if !strings.Contains(out, "wheel_nudge direction=up") || !strings.Contains(out, "rows=2") || !strings.Contains(out, "physical_travel=140") {
		t.Fatalf("Call output = %q, want wheel_nudge summary", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+wheelNudgeDefaultSteps+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+wheelNudgeDefaultSteps+touchReleaseReportCount)
	}
	// center_y=500, travel=140, half=70: startY=570, endY=430.
	expectedX, expectedStartY := normalizedToAbsolutePoint(650, 570)
	_, expectedEndY := normalizedToAbsolutePoint(650, 430)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) || reports[0].buttons != 0x00 {
		t.Fatalf("pre-move = (%d,%d,%d), want (%d,%d,0)", reports[0].x, reports[0].y, reports[0].buttons, expectedX, expectedStartY)
	}
	if reports[1].x != uint16(expectedX) || reports[1].y != uint16(expectedStartY) || reports[1].buttons != 0x01 {
		t.Fatalf("press = (%d,%d,%d), want (%d,%d,1)", reports[1].x, reports[1].y, reports[1].buttons, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.x != uint16(expectedX) || finalMove.y != uint16(expectedEndY) || finalMove.buttons != 0x01 {
		t.Fatalf("final move = (%d,%d,%d), want (%d,%d,1)", finalMove.x, finalMove.y, finalMove.buttons, expectedX, expectedEndY)
	}
}

func TestMeasureWheelRowSpacingFindsSyntheticPickerRows(t *testing.T) {
	jpegData := syntheticWheelPickerJPEG(t, 500, 1000, 300, 300, 40)

	measurement, ok := measureWheelRowSpacingJPEG(jpegData, 600, 300)
	if !ok {
		t.Fatal("measureWheelRowSpacingJPEG() did not find synthetic picker rows")
	}
	if math.Abs(measurement.Normalized-40) > 2 {
		t.Fatalf("normalized spacing = %.2f, want about 40", measurement.Normalized)
	}
	if measurement.Confidence < 0.5 {
		t.Fatalf("confidence = %.2f, want at least 0.5", measurement.Confidence)
	}
}

func TestMeasureWheelRowSpacingRejectsUniformImage(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 500, 1000))
	for i := range img.Pix {
		img.Pix[i] = 24
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode uniform image: %v", err)
	}
	if measurement, ok := measureWheelRowSpacingJPEG(encoded.Bytes(), 600, 300); ok {
		t.Fatalf("uniform image produced measurement %+v", measurement)
	}
}

func TestSelectWheelRowSpacingPeakRejectsWeakEarlyHarmonic(t *testing.T) {
	correlations := []float64{0.10, 0.27, 0.10, 0.10, 0.30, 0.10, 0.10}
	if index, confidence, ok := selectWheelRowSpacingPeak(correlations); ok {
		t.Fatalf("selectWheelRowSpacingPeak() = index %d confidence %.2f, want low-confidence fallback", index, confidence)
	}
}

func TestWheelNudgeUsesMeasuredRowSpacingFromLatestScreenshot(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(500, 1000, screen.ScreenActiveArea{})
	screenState.UpdateScreenshot(syntheticWheelPickerJPEG(t, 500, 1000, 300, 300, 40), 500, 1000)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":4,"target_value":2,"cycle_size":60,"cycle_start":0,"row_spacing":61,"value_step":1,"center_y":300}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "row_spacing_source=image") {
		t.Fatalf("Call output = %q, want image measurement metadata", out)
	}
	if !strings.Contains(out, "physical_travel=80") {
		t.Fatalf("Call output = %q, want two measured 40-unit rows", out)
	}
}

func TestWheelNudgeUsesConfidentImageMotionProfileForLargeGap(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(500, 1000, screen.ScreenActiveArea{})
	jpegData := syntheticWheelPickerJPEG(t, 500, 1000, 300, 300, 40)
	screenState.UpdateScreenshot(jpegData, 500, 1000)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":57,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":35,"value_step":1,"center_y":300}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "rows=6") || !strings.Contains(out, "physical_travel=258") {
		t.Fatalf("Call output = %q, want six measured rows plus settling compensation / 258 units", out)
	}
	if !strings.Contains(out, "motion_profile=image_calibrated") {
		t.Fatalf("Call output = %q, want calibrated motion metadata", out)
	}
	if !strings.Contains(out, "settle_compensation_rows=0.45") {
		t.Fatalf("Call output = %q, want settling compensation metadata", out)
	}

	reports := readMouseReports(t, dev, path)
	// Keep touchdown at the original three-row boundary inside the picker,
	// then extend only the destination by the 0.45-row settling allowance.
	measurement, measured := measureWheelRowSpacingJPEG(jpegData, 600, 300)
	if !measured {
		t.Fatal("synthetic picker row spacing was not measurable")
	}
	plannedTravel := 6 * measurement.Normalized
	expectedX, expectedStartY := normalizedToAbsolutePoint(600, 300+plannedTravel/2)
	_, expectedEndY := normalizedToAbsolutePoint(600, 300+plannedTravel/2-(6+wheelNudgeMultiRowCompensation)*measurement.Normalized)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) {
		t.Fatalf("pre-move = (%d,%d), want compensated drag to start at (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.x != uint16(expectedX) || finalMove.y != uint16(expectedEndY) {
		t.Fatalf("final move = (%d,%d), want compensated drag to end at (%d,%d)", finalMove.x, finalMove.y, expectedX, expectedEndY)
	}
}

func TestWheelNudgeDoesNotCompensateWhenPlannedRowsReachTarget(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(500, 1000, screen.ScreenActiveArea{})
	screenState.UpdateScreenshot(syntheticWheelPickerJPEG(t, 500, 1000, 300, 300, 40), 500, 1000)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":6,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":35,"value_step":1,"center_y":300}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "rows=3") || !strings.Contains(out, "physical_travel=120") {
		t.Fatalf("Call output = %q, want exact three-row travel without over-target compensation", out)
	}
	if strings.Contains(out, "settle_compensation_rows") {
		t.Fatalf("Call output = %q, exact-target drag must not add settling compensation", out)
	}
}

func TestWheelNudgeUsesConservativeProfileWhenImageMeasurementRejected(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(500, 1000, screen.ScreenActiveArea{})
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 500, 1000), 500, 1000)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":57,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":35,"value_step":1,"center_y":300}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "rows=5") || !strings.Contains(out, "physical_travel=175") {
		t.Fatalf("Call output = %q, want conservative five-row travel", out)
	}
	if strings.Contains(out, "motion_profile=image_calibrated") {
		t.Fatalf("Call output = %q, low-confidence image must not enable calibrated motion", out)
	}
}

func TestWheelNudgeMotionProfileBoundaries(t *testing.T) {
	tests := []struct {
		gap          int
		conservative int
		calibrated   int
	}{
		{gap: 1, conservative: 1, calibrated: 1},
		{gap: 2, conservative: 2, calibrated: 2},
		{gap: 3, conservative: 2, calibrated: 3},
		{gap: 4, conservative: 2, calibrated: 3},
		{gap: 5, conservative: 3, calibrated: 4},
		{gap: 8, conservative: 3, calibrated: 4},
		{gap: 9, conservative: 5, calibrated: 6},
		{gap: 12, conservative: 5, calibrated: 6},
	}
	for _, tt := range tests {
		if got := wheelNudgeRowsForGap(tt.gap); got != tt.conservative {
			t.Errorf("wheelNudgeRowsForGap(%d) = %d, want %d", tt.gap, got, tt.conservative)
		}
		if got := wheelNudgeRowsForConfidentGap(tt.gap); got != tt.calibrated {
			t.Errorf("wheelNudgeRowsForConfidentGap(%d) = %d, want %d", tt.gap, got, tt.calibrated)
		}
	}
}

func TestWheelNudgeTapDoesNotReportCalibratedDragProfile(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(500, 1000, screen.ScreenActiveArea{})
	screenState.UpdateScreenshot(syntheticWheelPickerJPEG(t, 500, 1000, 300, 300, 40), 500, 1000)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":8,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":40,"value_step":1,"center_y":300,"visible_target_y":340}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "interaction=tap") {
		t.Fatalf("Call output = %q, want adjacent-row tap", out)
	}
	if strings.Contains(out, "motion_profile=image_calibrated") {
		t.Fatalf("Call output = %q, tap must not report a drag profile", out)
	}
}

func TestWheelNudgeRequiresFreshScreenshotWhenConfigured(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{
		pc:                     testPointerController(dev, &pointerState{}),
		screen:                 &screen.ScreenState{},
		durationMs:             1,
		requireFreshScreenshot: true,
	}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":57,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":35,"value_step":1,"center_y":300}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "fresh screenshot") {
		t.Fatalf("Call output = %q, want fresh screenshot requirement", out)
	}
	if reports := readMouseReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no gesture without a fresh screenshot", len(reports))
	}
}

func TestWheelNudgeDwellsAtFinalCoordinateBeforeRelease(t *testing.T) {
	originalSleep := sleepMs
	var sleeps []int
	sleepMs = func(milliseconds int) {
		sleeps = append(sleeps, milliseconds)
	}
	t.Cleanup(func() { sleepMs = originalSleep })

	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}
	if _, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":600,"current_value":5,"target_value":9,"cycle_size":60,"cycle_start":0,"row_spacing":35,"value_step":1,"center_y":300}`); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !slices.Contains(sleeps, wheelNudgeEndpointHoldMs) {
		t.Fatalf("sleep calls = %v, want endpoint hold %dms", sleeps, wheelNudgeEndpointHoldMs)
	}
}

func syntheticWheelPickerJPEG(t *testing.T, width, height, columnX, centerY, spacing int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 20
	}
	for y := centerY - 20; y <= centerY+20; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, color.Gray{Y: 38})
		}
	}
	for row := -4; row <= 4; row++ {
		y := centerY + row*spacing
		brightness := uint8(max(70, 225-35*absInt(row)))
		drawSyntheticWheelDigits(img, columnX, y, brightness)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode synthetic picker: %v", err)
	}
	return encoded.Bytes()
}

func uniformWheelScreenshotJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	for index := range img.Pix {
		img.Pix[index] = 24
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode uniform wheel screenshot: %v", err)
	}
	return encoded.Bytes()
}

func drawSyntheticWheelDigits(img *image.Gray, centerX, centerY int, brightness uint8) {
	for _, digitX := range []int{centerX - 13, centerX + 7} {
		for y := centerY - 10; y <= centerY+10; y++ {
			for x := digitX; x <= digitX+3; x++ {
				img.SetGray(x, y, color.Gray{Y: brightness})
			}
		}
		for y := centerY - 10; y <= centerY-7; y++ {
			for x := digitX; x <= digitX+10; x++ {
				img.SetGray(x, y, color.Gray{Y: brightness})
			}
		}
		for y := centerY + 7; y <= centerY+10; y++ {
			for x := digitX; x <= digitX+10; x++ {
				img.SetGray(x, y, color.Gray{Y: brightness})
			}
		}
	}
}

func TestWheelNudgeUsesRowGapForMultiValueSteps(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"five-minute-picker","column_x":500,"remaining_gap":2,"current_value":0,"target_value":10,"cycle_size":60,"cycle_start":0,"row_spacing":42,"value_step":5,"center_y":500}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "direction=up") || !strings.Contains(out, "rows=2") || !strings.Contains(out, "physical_travel=84") {
		t.Fatalf("Call output = %q, want two-row movement for 0 -> 10 in five-value steps", out)
	}
}

func TestWheelNudgeRejectsTargetUnreachableByValueStep(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"five-minute-picker","column_x":500,"remaining_gap":1,"current_value":0,"target_value":3,"cycle_size":60,"cycle_start":0,"row_spacing":42,"value_step":5,"center_y":500}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "not reachable by value_step=5") {
		t.Fatalf("Call output = %q, want unreachable-step validation", out)
	}
	if reports := readMouseReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("unreachable target wrote %d HID reports", len(reports))
	}
}

func TestWheelNudgeTapsAdjacentVisibleTarget(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":650,"remaining_gap":1,"current_value":10,"target_value":11,"cycle_size":24,"cycle_start":0,"row_spacing":70,"value_step":1,"center_y":500,"visible_target_y":570}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "wheel_nudge interaction=tap") || !strings.Contains(out, "row_offset=1") {
		t.Fatalf("Call output = %q, want adjacent-row tap summary", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+touchReleaseReportCount)
	}
	expectedX, expectedY := normalizedToAbsolutePoint(650, 570)
	for index, report := range reports {
		if report.x != uint16(expectedX) || report.y != uint16(expectedY) {
			t.Fatalf("report[%d] = (%d,%d), want (%d,%d)", index, report.x, report.y, expectedX, expectedY)
		}
	}
	if reports[0].buttons != 0 || reports[1].buttons != 1 {
		t.Fatalf("tap reports buttons = %d,%d, want 0,1", reports[0].buttons, reports[1].buttons)
	}
}

func TestWheelNudgeAdjacentTargetWithoutVisibleCoordinateUsesMicroDrag(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":650,"remaining_gap":1,"current_value":10,"target_value":11,"cycle_size":24,"cycle_start":0,"row_spacing":70,"value_step":1,"center_y":500}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if strings.Contains(out, "interaction=tap") || !strings.Contains(out, "rows=1") {
		t.Fatalf("Call output = %q, want one-row micro drag without visible target evidence", out)
	}
}

func TestWheelNudgeNonAdjacentTargetIgnoresVisibleCoordinateAndUsesBoundedDrag(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":188,"remaining_gap":6,"current_value":15,"target_value":9,"cycle_size":24,"cycle_start":0,"row_spacing":46,"value_step":1,"center_y":271,"visible_target_y":167}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if strings.Contains(out, "visible_target_y is only valid") || strings.Contains(out, "interaction=tap") {
		t.Fatalf("Call output = %q, want stale non-adjacent tap hint ignored", out)
	}
	if !strings.Contains(out, "wheel_nudge direction=down") || !strings.Contains(out, "rows=3") {
		t.Fatalf("Call output = %q, want bounded three-row drag", out)
	}
}

func TestWheelNudgeRejectsUnverifiedVisibleTargetCoordinate(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":650,"remaining_gap":1,"current_value":10,"target_value":11,"cycle_size":24,"cycle_start":0,"row_spacing":70,"value_step":1,"center_y":500,"visible_target_y":800}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "does not match the observed adjacent row") {
		t.Fatalf("Call output = %q, want visible target validation error", out)
	}
	if reports := readMouseReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("invalid visible target wrote %d HID reports", len(reports))
	}
}

func TestWheelNudgeRejectsAdjacentTargetOutsideTightRowCenterTolerance(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"alarm-create","column_x":193,"remaining_gap":1,"current_value":10,"target_value":9,"cycle_size":24,"cycle_start":0,"row_spacing":43,"value_step":1,"center_y":240,"visible_target_y":187}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "does not match the observed adjacent row") {
		t.Fatalf("Call output = %q, want inaccurate adjacent target rejected", out)
	}
	if reports := readMouseReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("inaccurate adjacent target wrote %d HID reports", len(reports))
	}
}

func TestTouchGestureRejectsDistinctInputsResolvingToSameHIDPoint(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"type":"drag","start":{"x":500,"y":500},"end":{"x":500.001,"y":500.001}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "same HID point") {
		t.Fatalf("Call output = %q, want resolved zero-distance rejection", out)
	}
}

func TestTouchGestureTouchscreenPrimesMappingBeforeNormalizedInput(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	primeCalls := 0
	tool := &TouchGestureTool{
		pc:     testTouchscreenPointerController(dev, &pointerState{}),
		screen: screenState,
		primeScreenMapping: func(context.Context) error {
			primeCalls++
			screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 0, Width: 497, Height: 1080, Valid: true})
			return nil
		},
	}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":931,"y":83}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}
	if primeCalls != 1 {
		t.Fatalf("prime calls = %d, want 1", primeCalls)
	}

	reports := readTouchscreenReports(t, dev, path)
	if len(reports) != 1+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 1+touchReleaseReportCount)
	}
	expectedX := scalePixelToAbsolute(711+(931.0/1000.0)*496, 1920)
	expectedY := scalePixelToAbsolute((83.0/1000.0)*1079, 1080)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedY) {
		t.Fatalf("first report = (%d,%d), want (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedY)
	}
	fallbackX, fallbackY := normalizedToAbsolutePoint(931, 83)
	if reports[0].x == uint16(fallbackX) && reports[0].y == uint16(fallbackY) {
		t.Fatalf("first report used fallback coordinates (%d,%d)", reports[0].x, reports[0].y)
	}
}

func TestTouchGestureTouchscreenDoesNotWriteWhenMappingPrimeFails(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{
		pc:     testTouchscreenPointerController(dev, &pointerState{}),
		screen: &screen.ScreenState{},
		primeScreenMapping: func(context.Context) error {
			return errors.New("frame service recovering")
		},
	}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":931,"y":83}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "touchscreen mapping unavailable") {
		t.Fatalf("Call output = %q, want mapping error", out)
	}
	if reports := readTouchscreenReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestTouchGestureTouchscreenKeepsFreshFullFrameMappingWhenPrimeWouldFail(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{})
	primeCalls := 0
	tool := &TouchGestureTool{
		pc:     testTouchscreenPointerController(dev, &pointerState{}),
		screen: screenState,
		primeScreenMapping: func(context.Context) error {
			primeCalls++
			return errors.New("frame service unavailable")
		},
	}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":931,"y":83}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}
	if primeCalls != 0 {
		t.Fatalf("prime calls = %d, want 0", primeCalls)
	}

	reports := readTouchscreenReports(t, dev, path)
	if len(reports) != 1+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 1+touchReleaseReportCount)
	}
	expectedX := scalePixelToAbsolute((931.0/1000.0)*1919, 1920)
	expectedY := scalePixelToAbsolute((83.0/1000.0)*1079, 1080)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedY) {
		t.Fatalf("first report = (%d,%d), want (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedY)
	}
}

func TestWheelNudgeLargeSupportsDown(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":500,"center_y":460,"current_value":16,"target_value":0,"cycle_size":0,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "wheel_nudge direction=down") || !strings.Contains(out, "rows=5") {
		t.Fatalf("Call output = %q, want large wheel_nudge summary", out)
	}
	if !strings.Contains(out, "physical_travel=210") || !strings.Contains(out, "duration_ms=2") {
		t.Fatalf("Call output = %q, want five-row slow drag", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2+wheelNudgeDefaultSteps+touchReleaseReportCount {
		t.Fatalf("len(reports) = %d, want %d", len(reports), 2+wheelNudgeDefaultSteps+touchReleaseReportCount)
	}
	// Large drags start near the highlighted row so they cannot begin at a
	// screen edge and trigger an iOS system gesture.
	expectedX, expectedStartY := normalizedToAbsolutePoint(500, 355)
	_, expectedEndY := normalizedToAbsolutePoint(500, 565)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) || reports[0].buttons != 0x00 {
		t.Fatalf("pre-move = (%d,%d,%d), want (%d,%d,0)", reports[0].x, reports[0].y, reports[0].buttons, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.x != uint16(expectedX) || finalMove.y != uint16(expectedEndY) || finalMove.buttons != 0x01 {
		t.Fatalf("final move = (%d,%d,%d), want (%d,%d,1)", finalMove.x, finalMove.y, finalMove.buttons, expectedX, expectedEndY)
	}
}

func TestWheelNudgeReportsEffectiveTravelAfterEdgeClamping(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"edge-picker","column_x":500,"current_value":0,"target_value":12,"cycle_size":0,"cycle_start":0,"row_spacing":300,"value_step":1,"center_y":990}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "physical_travel=1000") {
		t.Fatalf("Call output = %q, want clamped effective travel", out)
	}

	reports := readMouseReports(t, dev, path)
	expectedX, expectedStartY := normalizedToAbsolutePoint(500, 1000)
	_, expectedEndY := normalizedToAbsolutePoint(500, 0)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) {
		t.Fatalf("pre-move = (%d,%d), want (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.y != uint16(expectedEndY) {
		t.Fatalf("final y = %d, want %d", finalMove.y, expectedEndY)
	}
}

func TestWheelNudgeUsesNormalizedColumnAndCenter(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: screenState, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":195,"current_value":10,"target_value":13,"cycle_size":24,"cycle_start":0,"row_spacing":38,"value_step":1,"center_y":273}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "wheel_nudge direction=up") || !strings.Contains(out, "physical_travel=76") {
		t.Fatalf("Call output = %q, want wheel_nudge summary", out)
	}

	travel := 2.0 * 38.0
	startY := 273.0 + travel/2
	endY := startY - travel
	expectedX, expectedStartY, err := normalizedToAbsolutePointForSurface(screenState, false, 195, startY)
	if err != nil {
		t.Fatalf("resolve normalized wheel start: %v", err)
	}
	_, expectedEndY, err := normalizedToAbsolutePointForSurface(screenState, false, 195, endY)
	if err != nil {
		t.Fatalf("resolve normalized wheel end: %v", err)
	}
	reports := readMouseReports(t, dev, path)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) {
		t.Fatalf("pre-move = (%d,%d), want (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.x != uint16(expectedX) || finalMove.y != uint16(expectedEndY) {
		t.Fatalf("final move = (%d,%d), want (%d,%d)", finalMove.x, finalMove.y, expectedX, expectedEndY)
	}
}

func TestWheelNudgeDerivesBoundedTravelFromGapAndRowSpacing(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":500,"center_y":500,"current_value":8,"target_value":13,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "rows=3") || !strings.Contains(out, "physical_travel=126") {
		t.Fatalf("Call output = %q, want three measured rows / 126 units", out)
	}

	reports := readMouseReports(t, dev, path)
	startY := 500.0 + 63.0
	endY := startY - 126.0
	expectedX, expectedStartY := normalizedToAbsolutePoint(500, startY)
	_, expectedEndY := normalizedToAbsolutePoint(500, endY)
	if reports[0].x != uint16(expectedX) || reports[0].y != uint16(expectedStartY) {
		t.Fatalf("pre-move = (%d,%d), want (%d,%d)", reports[0].x, reports[0].y, expectedX, expectedStartY)
	}
	finalMove := reports[1+wheelNudgeDefaultSteps]
	if finalMove.y != uint16(expectedEndY) {
		t.Fatalf("final y = %d, want %d", finalMove.y, expectedEndY)
	}
}

func TestWheelNudgeIgnoresLegacyRemainingGapAndDerivesShortestPath(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"minute-picker","column_x":500,"center_y":500,"remaining_gap":46,"current_value":47,"target_value":1,"cycle_size":60,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "direction=up") || !strings.Contains(out, "rows=5") {
		t.Fatalf("Call output = %q, want runtime-derived 14-row shortest path with a five-row coarse drag", out)
	}
}

func TestWheelNudgeFirstMicroProbeUsesExactlyOneMeasuredRow(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

	out, err := tool.Call(context.Background(), `{"picker_id":"test-picker","column_x":500,"center_y":500,"current_value":2,"target_value":12,"cycle_size":60,"cycle_start":0,"row_spacing":42}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "rows=1") || !strings.Contains(out, "physical_travel=42") {
		t.Fatalf("Call output = %q, want one measured-row probe", out)
	}
}

func TestWheelNudgeSchemaDerivesTravelFromGap(t *testing.T) {
	schema := (&WheelNudgeTool{}).ArgsSchema()
	props := schema["properties"].(map[string]any)
	want := map[string]bool{
		"picker_id": true, "column_x": true,
		"current_value": true, "target_value": true,
		"cycle_size": true, "cycle_start": true,
		"row_spacing": true, "value_step": true, "visible_target_y": true,
		"center_y": true,
	}
	if len(props) != len(want) {
		t.Fatalf("wheel_nudge schema properties = %#v, want only portable contract %#v", props, want)
	}
	for name := range want {
		if _, ok := props[name]; !ok {
			t.Fatalf("wheel_nudge schema missing %q: %#v", name, props)
		}
	}
	for _, removed := range []string{"distance", "duration_ms"} {
		if _, ok := props[removed]; ok {
			t.Fatalf("wheel_nudge schema must derive %s internally: %#v", removed, props)
		}
	}
}

func TestTouchGestureSchemaDoesNotExposeWheelMetadata(t *testing.T) {
	schema := (&TouchGestureTool{}).ArgsSchema()
	props := schema["properties"].(map[string]any)
	if _, ok := props["wheel"]; ok {
		t.Fatalf("touch_gesture must remain generic and must not expose wheel metadata: %#v", props)
	}
}

func TestWheelNudgeDescriptionDefinesAdaptiveTravelAndConservativeInput(t *testing.T) {
	description := (&WheelNudgeTool{}).Description()
	for _, want := range []string{
		"tap or slow drag",
		"9+",
		"3-4",
		"5-8",
		"confident runtime image measurement",
		"conservative limits remain",
		"final requested value",
		"never substitute an intermediate visible value",
		"normalized 0-1000 coordinates",
		"screenshot height",
		"Use wheel_nudge directly from the latest screenshot",
		"Do not tap the selected row",
		"do not use keyboard_text for picker values",
		"derives the shortest row gap",
		"measures the row spacing",
		"overrides the caller estimate",
		"low-confidence images keep the caller estimate",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("wheel_nudge description = %q, want %q", description, want)
		}
	}
	if strings.Contains(description, "coord_space") {
		t.Fatalf("wheel_nudge description must not expose coord_space: %q", description)
	}
}

func TestWheelNudgeRejectsInputsThatWouldBypassGestureGuard(t *testing.T) {
	invalidInputs := map[string]string{
		`{"picker_id":"alarm-create","column_x":400,"current_value":15,"target_value":7,"cycle_size":24,"cycle_start":0,"row_spacing":40,"value_step":1}`: "center_y is required",
		`{"column_x":350,"remaining_gap":1,"current_value":1,"target_value":2,"cycle_size":0,"cycle_start":0,"row_spacing":42,"value_step":1}`:            "picker_id is required",
		`{"column_x":350,"remaining_gap":1,"duration_ms":0}`:    `unknown field "duration_ms"`,
		`{"column_x":350,"remaining_gap":1,"distance":"micro"}`: `unknown field "distance"`,
		`{"column_x":"350"}`:                           "cannot unmarshal string",
		`{"column_x":350,"direction":"up"}`:            `unknown field "direction"`,
		`{"column_x":350,"increasing_direction":"up"}`: `unknown field "increasing_direction"`,
		`{"column_x":350} {"column_x":650}`:            "expected exactly one JSON object",
	}
	for input, wantError := range invalidInputs {
		t.Run(input, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			tool := &WheelNudgeTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}, durationMs: 1}

			out, err := tool.Call(context.Background(), input)
			if err != nil {
				t.Fatalf("Call returned error: %v", err)
			}
			if !strings.Contains(out, wantError) {
				t.Fatalf("Call output = %q, want %q", out, wantError)
			}
			if reports := readMouseReports(t, dev, path); len(reports) != 0 {
				t.Fatalf("invalid input wrote %d HID reports", len(reports))
			}
		})
	}
}

func TestWheelNudgeSchemaRequiresMeasuredCenterY(t *testing.T) {
	schema := (&WheelNudgeTool{}).ArgsSchema()
	required, _ := schema["required"].([]string)
	if !slices.Contains(required, "center_y") {
		t.Fatalf("wheel_nudge schema required = %v, want center_y", required)
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
	for _, want := range []string{
		"backspace is backward-delete",
		"delete is forward-delete",
		"For semantic deletion, use quick_action delete_backward/delete_forward",
	} {
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
	ctxErrs  []error
	handler  func(args []string) (string, error)
}

func (r *recordingADBRunner) run(ctx context.Context, _ string, args ...string) ([]byte, []byte, error) {
	copied := append([]string(nil), args...)
	r.commands = append(r.commands, copied)
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
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

func newTestADBInputController(t *testing.T, screenState *screen.ScreenState, runner *recordingADBRunner) *ADBInputController {
	t.Helper()
	t.Setenv("AIDEN_ADB_PATH", "/fake/adb")
	t.Setenv("AIDEN_ADB_SERIAL", "serial123")
	if screenState == nil {
		screenState = &screen.ScreenState{}
	}
	return &ADBInputController{
		screen: screenState,
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
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1080), HeightPixels: intPtr(2400)})
	runner := &recordingADBRunner{}
	tool := &MouseClickTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}

	out, err := tool.Call(context.Background(), `{"x":500,"y":250}`)
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

func TestADBMouseClickRejectsOutOfRangeCoordinates(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1080), HeightPixels: intPtr(2400)})
	runner := &recordingADBRunner{}
	tool := &MouseClickTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}

	out, err := tool.Call(context.Background(), `{"x":1500,"y":500}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "normalized 0-1000 scale") {
		t.Fatalf("Call output = %q, want normalized coordinate error", out)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("adb commands = %#v, want no command for out-of-range normalized coordinates", runner.commands)
	}
}

func TestPointerResolversRejectNonFiniteCoordinates(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, _, err := resolvePointerPositionForSurface(nil, false, value, 500); err == nil || !strings.Contains(err.Error(), "finite") {
			t.Fatalf("resolvePointerPositionForSurface(%v, 500) error = %v, want finite-coordinate error", value, err)
		}
		controller := &ADBInputController{}
		if _, err := controller.ResolvePosition(context.Background(), value, 500); err == nil || !strings.Contains(err.Error(), "finite") {
			t.Fatalf("ResolvePosition(%v, 500) error = %v, want finite-coordinate error", value, err)
		}
	}
}

func TestDirectionalSwipeCoordinatesClampDerivedEdgeEndpoints(t *testing.T) {
	distance := 1000.0
	anchor := 1000.0
	startX, startY, endX, endY, err := directionalSwipeNormalizedCoordinates(
		"swipe_left",
		&distance,
		&anchor,
		directionalSwipeSettings{},
	)
	if err != nil {
		t.Fatalf("directionalSwipeNormalizedCoordinates() error = %v", err)
	}
	if startX != 1000 || startY != 1000 || endX != 500 || endY != 1000 {
		t.Fatalf("coordinates = (%.0f,%.0f)->(%.0f,%.0f), want (1000,1000)->(500,1000)", startX, startY, endX, endY)
	}
}

func TestADBTouchGestureSwipeUsesInputSwipe(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}

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

func TestADBTouchGestureTapAcceptsLegacyTopLevelCoordinates(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1080), HeightPixels: intPtr(2400)})
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}

	out, err := tool.Call(context.Background(), `{"type":"tap","x":"500","y":"250"}`)
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

func TestADBTouchGestureSwipeAndDragRejectSameResolvedPoint(t *testing.T) {
	for _, gestureType := range []string{"swipe", "drag"} {
		t.Run(gestureType, func(t *testing.T) {
			screenState := &screen.ScreenState{}
			screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
			runner := &recordingADBRunner{}
			tool := &TouchGestureTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}
			ctx, _ := WithToolError(context.Background())

			out, err := tool.Call(ctx, fmt.Sprintf(`{"type":%q,"start":{"x":500,"y":500},"end":{"x":500,"y":500}}`, gestureType))
			if err != nil {
				t.Fatalf("Call returned error: %v", err)
			}
			if !strings.Contains(out, gestureType+" start and end resolve to the same point") {
				t.Fatalf("Call output = %q, want same-point error", out)
			}
			if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
				t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("adb commands = %#v, want no swipe command for identical points", runner.commands)
			}
		})
	}
}

func TestADBTouchGestureLongPressExtendsCommandTimeout(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}

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
	tool := &TouchGestureTool{screen: &screen.ScreenState{}, adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(context.Background(), `{"type":"back"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	want := []string{"-s", "serial123", "shell", "input", "keyevent", "KEYCODE_BACK"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBTouchGestureBackDoesNotPrimeTouchscreenMapping(t *testing.T) {
	runner := &recordingADBRunner{}
	tool := &TouchGestureTool{
		screen: &screen.ScreenState{},
		adb:    newTestADBInputController(t, nil, runner),
		primeScreenMapping: func(context.Context) error {
			return errors.New("mapping should not run for adb")
		},
	}

	out, err := tool.Call(context.Background(), `{"type":"back"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}
	want := []string{"-s", "serial123", "shell", "input", "keyevent", "KEYCODE_BACK"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
		t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
	}
}

func TestADBKeyboardTapAndroidAliasesAlignWithBridge(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{name: "home", keys: []string{"home"}, want: "KEYCODE_HOME"},
		{name: "keycode home", keys: []string{"KEYCODE_HOME"}, want: "KEYCODE_HOME"},
		{name: "escape as android back", keys: []string{"escape"}, want: "KEYCODE_BACK"},
		{name: "return", keys: []string{"return"}, want: "KEYCODE_ENTER"},
		{name: "delete backward", keys: []string{"delete_backward"}, want: "KEYCODE_DEL"},
		{name: "app switch", keys: []string{"keycode_app_switch"}, want: "KEYCODE_APP_SWITCH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingADBRunner{}
			tool := &KeyboardTapTool{adb: newTestADBInputController(t, nil, runner)}

			payload, err := json.Marshal(map[string]any{"keys": tt.keys})
			if err != nil {
				t.Fatal(err)
			}
			out, err := tool.Call(context.Background(), string(payload))
			if err != nil {
				t.Fatalf("Call returned error: %v", err)
			}
			if out != "ok" {
				t.Fatalf("Call output = %q, want ok", out)
			}
			want := []string{"-s", "serial123", "shell", "input", "keyevent", tt.want}
			if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
				t.Fatalf("adb commands = %#v, want %#v", runner.commands, want)
			}
		})
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

func TestADBKeyboardTextRestoresIMEAfterCallerContextCanceled(t *testing.T) {
	origSleep := sleepMs
	sleepMs = func(int) {}
	defer func() { sleepMs = origSleep }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &recordingADBRunner{handler: func(args []string) (string, error) {
		switch strings.Join(args, " ") {
		case "-s serial123 shell settings get secure default_input_method":
			return "com.example/.Keyboard", nil
		case "-s serial123 shell am broadcast -a ADB_INPUT_B64 --es msg " + base64.StdEncoding.EncodeToString([]byte("Hello")):
			cancel()
		}
		return "", nil
	}}
	tool := &KeyboardTextTool{adb: newTestADBInputController(t, nil, runner)}

	out, err := tool.Call(ctx, `{"text":"Hello"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	wantRestore := []string{"-s", "serial123", "shell", "ime", "set", "com.example/.Keyboard"}
	if len(runner.commands) != 4 || !stringSlicesEqual(runner.commands[3], wantRestore) {
		t.Fatalf("adb commands = %#v, want final restore command %#v", runner.commands, wantRestore)
	}
	if runner.ctxErrs[3] != nil {
		t.Fatalf("restore ctx err = %v, want independent uncanceled context", runner.ctxErrs[3])
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

func TestADBMouseMoveRejectsUnsupportedAfterCoordinateValidation(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1080), HeightPixels: intPtr(2400)})
	runner := &recordingADBRunner{}
	tool := &MouseMoveTool{screen: screenState, adb: newTestADBInputController(t, screenState, runner)}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"x":500,"y":250}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "adb mouse_move is unsupported") {
		t.Fatalf("Call output = %q, want unsupported adb mouse_move error", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeModuleUnavailable || got.Message != out {
		t.Fatalf("ToolError = %+v, want module_unavailable with output message", got)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("adb commands = %#v, want no command for unsupported mouse_move", runner.commands)
	}
}

func TestADBMouseScrollUsesSwipeApproximation(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{WidthPixels: intPtr(1001), HeightPixels: intPtr(1001)})
	runner := &recordingADBRunner{}
	tool := &MouseScrollTool{adb: newTestADBInputController(t, screenState, runner)}

	out, err := tool.Call(context.Background(), `{"delta":-3}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}
	want := []string{"-s", "serial123", "shell", "input", "swipe", "500", "750", "500", "250", "650"}
	if len(runner.commands) != 1 || !stringSlicesEqual(runner.commands[0], want) {
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

func TestResolvePointerPositionNormalizedUsesActiveArea(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	x, y, err := resolvePointerPosition(screenState, 0, 500)
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
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})

	x, y, err := resolvePointerPositionForSurface(screenState, true, 627, 180)
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

func TestModelFacingDeviceGuidanceUsesNormalizedCoordinatesOnly(t *testing.T) {
	assertNoCoordinateSpaceGuidance := func(name, content string) {
		t.Helper()
		lower := strings.ToLower(content)
		if strings.Contains(lower, "coord_space") || strings.Contains(lower, "coordinate space") {
			t.Fatalf("%s still advertises coordinate-space selection: %q", name, content)
		}
	}

	descriptions := map[string]string{
		"mouse_click":   (&MouseClickTool{}).Description(),
		"mouse_move":    (&MouseMoveTool{}).Description(),
		"touch_gesture": (&TouchGestureTool{}).Description(),
		"wheel_nudge":   (&WheelNudgeTool{}).Description(),
		"screenshot":    (&ScreenshotTool{}).Description(),
	}
	for name, description := range descriptions {
		assertNoCoordinateSpaceGuidance(name+" description", description)
	}

	skillPath := filepath.Join("..", "..", "config", "skills", "device-operator", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read device-operator SKILL.md: %v", err)
	}
	content := string(data)
	assertNoCoordinateSpaceGuidance("device-operator SKILL.md", content)
	if !strings.Contains(content, "0-1000") {
		t.Fatalf("device-operator SKILL.md must document the normalized 0-1000 scale")
	}
}

func TestNormalizedCoordinatesPreserveNearlyFullAxisOffsetForAbsolutePointer(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	x, y, err := resolvePointerPositionForSurface(screenState, false, 500, 220)
	if err != nil {
		t.Fatalf("resolvePointerPositionForSurface returned error: %v", err)
	}
	localX := 0.5 * float64(498-1)
	localY := 0.22 * float64(1052-1)
	wantX := scalePixelToAbsolute(localX, 498)
	wantY := scalePixelToAbsolute(28+localY, 1080)
	if x != wantX || y != wantY {
		t.Fatalf("normalized point = (%d,%d), want (%d,%d)", x, y, wantX, wantY)
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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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

func TestMouseMoveRejectsCoordinatesOutsideNormalizedRange(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseMoveTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"x":2000,"y":3000}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "normalized 0-1000 scale") {
		t.Fatalf("Call output = %q, want normalized range error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want 0", len(reports))
	}
}

func TestMouseClickAcceptsStringCoordinates(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseClickTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"x":"500","y":"250"}`)
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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true})
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: screenState}

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
	tool := &TouchGestureTool{pc: testTouchscreenPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
		"enter_text",
		"Do not transliterate Chinese/CJK targets to pinyin",
		"English/Latin keyboard",
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

func TestKeyboardTextDescriptionRejectsNumericPickerInput(t *testing.T) {
	desc := (&KeyboardTextTool{}).Description()
	for _, want := range []string{
		"Do not use keyboard_text for picker/wheel values",
		"use wheel_nudge",
		"verify each returned screenshot",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing conservative picker guidance %q:\n%s", want, desc)
		}
	}
}

func TestDeviceOperatorSkillUsesWheelOnlyForPickers(t *testing.T) {
	skillPath := filepath.Join("..", "..", "config", "skills", "device-operator", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Skipf("device-operator SKILL.md not readable from test cwd: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"use `wheel_nudge` directly from the latest screenshot",
		"Do not tap the selected row to probe for keyboard/edit mode",
		"do not use `enter_text` for picker values",
		"do not use `enter_text` or `keyboard_tap` to change picker values",
		"issue one bounded `wheel_nudge`, then read the returned screenshot",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("device-operator SKILL.md missing conservative picker guidance %q", want)
		}
	}
}

func TestTouchGestureDescriptionReservesPickerForWheelNudge(t *testing.T) {
	desc := (&TouchGestureTool{}).Description()
	for _, want := range []string{
		"Do not tap picker rows to probe for keyboard/edit mode",
		"do not drag picker columns with this tool",
		"use wheel_nudge for the entire picker interaction",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("touch_gesture description missing wheel ownership guidance %q:\n%s", want, desc)
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

func TestKeyboardTextUsesConfiguredAZERTYLayout(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTextTool{dev: dev, keyboardLayout: keyboardLayoutAZERTY}

	out, err := tool.Call(context.Background(), `{"text":"shape"}`)
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
	wantUsages := []byte{0x16, 0x0b, 0x14, 0x13, 0x08}
	if len(data) != len(wantUsages)*16 {
		t.Fatalf("report bytes = %d, want %d", len(data), len(wantUsages)*16)
	}
	for i, want := range wantUsages {
		press := data[i*16 : i*16+8]
		if press[0] != 0 || press[2] != want {
			t.Fatalf("press report %d = %v, want modifier 0 and usage 0x%02x", i, press, want)
		}
	}
}

func TestKeyboardTextUsesConfiguredQWERTZLayout(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTextTool{dev: dev, keyboardLayout: keyboardLayoutQWERTZ}

	out, err := tool.Call(context.Background(), `{"text":"yz"}`)
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
	wantUsages := []byte{0x1d, 0x1c}
	if len(data) != len(wantUsages)*16 {
		t.Fatalf("report bytes = %d, want %d", len(data), len(wantUsages)*16)
	}
	for i, want := range wantUsages {
		press := data[i*16 : i*16+8]
		if press[0] != 0 || press[2] != want {
			t.Fatalf("press report %d = %v, want modifier 0 and usage 0x%02x", i, press, want)
		}
	}
}

func TestKeyboardTextRejectsQWERTZDeadKeysWithoutPartialTyping(t *testing.T) {
	for _, deadKey := range []string{"^", "`"} {
		t.Run(deadKey, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			tool := &KeyboardTextTool{dev: dev, keyboardLayout: keyboardLayoutQWERTZ}

			out, err := tool.Call(context.Background(), fmt.Sprintf(`{"text":"a%sb"}`, deadKey))
			if err != nil {
				t.Fatalf("Call returned error: %v", err)
			}
			if !strings.Contains(out, fmt.Sprintf(`unsupported characters: %q`, deadKey)) {
				t.Fatalf("unexpected output: %q", out)
			}

			dev.Close()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if len(data) != 0 {
				t.Fatalf("dead key must not type a partial prefix, got %d bytes", len(data))
			}
		})
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

func TestPostActionScreenshotFailureMarksActionAsCompleted(t *testing.T) {
	action := &stubTool{name: "wheel_nudge", output: "ok: wheel_nudge rows=2"}
	screenshot := &stubTool{name: "screenshot", err: errors.New("capture unavailable")}
	tool := newPostActionScreenshotTool(action, screenshot, 0)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	toolErr := ToolErrorFromContext(ctx)
	if toolErr == nil || toolErr.Message != out {
		t.Fatalf("ToolError = %#v, output=%q", toolErr, out)
	}
	if completed, _ := toolErr.Details[postActionCompletedDetail].(bool); !completed {
		t.Fatalf("post-action error details = %#v, want action_completed=true", toolErr.Details)
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
	moveTool := &MouseMoveTool{pc: testPointerController(dev, state), screen: &screen.ScreenState{}}
	scrollTool := &MouseScrollTool{pc: testPointerController(dev, state)}

	if out, err := moveTool.Call(context.Background(), `{"x":200,"y":300}`); err != nil || out != "ok" {
		t.Fatalf("move output=%q err=%v", out, err)
	}
	if out, err := scrollTool.Call(context.Background(), `{"delta":-3}`); err != nil || out != "ok" {
		t.Fatalf("scroll output=%q err=%v", out, err)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	wantX, wantY := normalizedToAbsolutePoint(200, 300)
	if reports[1].buttons != 0 || int(reports[1].x) != wantX || int(reports[1].y) != wantY || reports[1].wheel != -3 {
		t.Fatalf("scroll report = (%d,%d,%d,%d), want (0,%d,%d,-3)", reports[1].buttons, reports[1].x, reports[1].y, reports[1].wheel, wantX, wantY)
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

func TestKeyboardTapUsesConfiguredAZERTYLayout(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &KeyboardTapTool{dev: dev, keyboardLayout: keyboardLayoutAZERTY}

	out, err := tool.Call(context.Background(), `{"keys":["a"],"hold_ms":1}`)
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
		t.Fatalf("report bytes = %d, want 16", len(data))
	}
	if data[0] != 0 || data[2] != 0x14 {
		t.Fatalf("press report = %v, want modifier 0 and AZERTY a usage 0x14", data[:8])
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

func TestKeyboardTapSupportsHIDBackedAndroidKeycodeAliases(t *testing.T) {
	testCases := []struct {
		input  string
		mapKey string
	}{
		{input: "KEYCODE_SCREENSHOT", mapKey: "screenshot"},
		{input: "KEYCODE_SETTINGS", mapKey: "settings"},
		{input: "KEYCODE_WINDOW", mapKey: "window"},
		{input: "KEYCODE_BRIGHTNESS_UP", mapKey: "brightness_up"},
		{input: "KEYCODE_BRIGHTNESS_DOWN", mapKey: "brightness_down"},
		{input: "KEYCODE_DICTATE", mapKey: "dictate"},
		{input: "KEYCODE_EMOJI_PICKER", mapKey: "emoji_picker"},
		{input: "KEYCODE_MEDIA_AUDIO_TRACK", mapKey: "media_audio_track"},
		{input: "KEYCODE_PROFILE_SWITCH", mapKey: "profile_switch"},
		{input: "KEYCODE_NEW", mapKey: "new"},
		{input: "KEYCODE_CLOSE", mapKey: "close"},
		{input: "KEYCODE_PRINT", mapKey: "print"},
		{input: "KEYCODE_LANGUAGE_SWITCH", mapKey: "language_switch"},
		{input: "KEYCODE_REFRESH", mapKey: "refresh"},
		{input: "KEYCODE_FULLSCREEN", mapKey: "fullscreen"},
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

func TestKeyboardTapKeepsLegacyHIDUsageAliases(t *testing.T) {
	testCases := []struct {
		input  string
		mapKey string
	}{
		{input: "KEY_USAGE_SCREENSHOT", mapKey: "key_usage_screenshot"},
		{input: "KEY_USAGE_SETTINGS", mapKey: "key_usage_settings"},
		{input: "KEY_USAGE_LANGUAGE_SWITCH", mapKey: "key_usage_language_switch"},
		{input: "KEY_USAGE_PRINT", mapKey: "key_usage_print"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			resolved, err := resolveKeyboardTapKeys([]string{tc.input})
			if err != nil {
				t.Fatalf("resolveKeyboardTapKeys failed: %v", err)
			}
			if resolved.AndroidExtensionKey != tc.mapKey {
				t.Fatalf("AndroidExtensionKey = %q, want %q", resolved.AndroidExtensionKey, tc.mapKey)
			}
			if resolved.AndroidUsage != androidExtensionUsageMap[tc.mapKey] {
				t.Fatalf("AndroidUsage = 0x%04x, want 0x%04x", resolved.AndroidUsage, androidExtensionUsageMap[tc.mapKey])
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
		{input: "KEYCODE_SCREENSHOT", mapKey: "screenshot"},
		{input: "KEYCODE_BRIGHTNESS_UP", mapKey: "brightness_up"},
		{input: "KEYCODE_BRIGHTNESS_DOWN", mapKey: "brightness_down"},
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
	tool := &MouseClickTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"x":500,"y":500}`)
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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	for _, want := range []string{"Prefer quick_action", `quick_action with {"action":"home"} first`, `touch_gesture {"type":"home"} only as a fallback`, "finger movement"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// Edge-alias coordinates (back x=1, home y=999) now live in the type ArgsSchema field.
	props, _ := (&TouchGestureTool{}).ArgsSchema()["properties"].(map[string]any)
	typeSchema, _ := props["type"].(map[string]any)
	typeDesc, _ := typeSchema["description"].(string)
	for _, want := range []string{"back", "home", "x=1", "y=999", `quick_action {"action":"home"}`, "fallback alternatives"} {
		if !strings.Contains(typeDesc, want) {
			t.Fatalf("type schema missing %q:\n%s", want, typeDesc)
		}
	}
}

func TestTouchGestureSchemaRequiresNamedCoordinateObjectsAndShowsCompleteExamples(t *testing.T) {
	schema := (&TouchGestureTool{}).ArgsSchema()
	description, _ := schema["description"].(string)
	for _, want := range []string{"Strict JSON object", "point", "start", "end", "both x and y"} {
		if !strings.Contains(description, want) {
			t.Fatalf("root schema description missing %q:\n%s", want, description)
		}
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	for _, name := range []string{"point", "start", "end"} {
		coordinate, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("%s schema = %#v", name, properties[name])
		}
		if coordinate["type"] != "object" || coordinate["additionalProperties"] != false {
			t.Fatalf("%s schema is not a strict object: %#v", name, coordinate)
		}
		required, ok := coordinate["required"].([]string)
		if !ok || !slices.Equal(required, []string{"x", "y"}) {
			t.Fatalf("%s required = %#v, want x and y", name, coordinate["required"])
		}
		desc, _ := coordinate["description"].(string)
		for _, want := range []string{`named keys "x" and "y"`, "do not use an array", "bare value"} {
			if !strings.Contains(desc, want) {
				t.Fatalf("%s description missing %q:\n%s", name, want, desc)
			}
		}
	}

	examples, ok := schema["examples"].([]map[string]any)
	if !ok || len(examples) != 3 {
		t.Fatalf("schema examples = %#v, want three complete examples", schema["examples"])
	}
	encoded, err := json.Marshal(examples)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("schema examples are not valid JSON: encoded=%s err=%v", encoded, err)
	}
	for _, want := range []string{
		`{"point":{"x":500,"y":500},"type":"tap"}`,
		`"start":{"x":500,"y":800}`,
		`"end":{"x":500,"y":200}`,
		`{"strength":"medium","type":"swipe_up"}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("schema examples missing %q: %s", want, encoded)
		}
	}

	typeSchema, _ := properties["type"].(map[string]any)
	typeDesc, _ := typeSchema["description"].(string)
	for _, want := range []string{
		"tap, double_tap, and long_press require point",
		"swipe and drag require start and end",
		"Directional swipe types accept optional strength, distance, and anchor",
	} {
		if !strings.Contains(typeDesc, want) {
			t.Fatalf("type schema missing %q:\n%s", want, typeDesc)
		}
	}
}

func TestTouchGestureSchemaFiltersEdgeGesturesForDesktopPlatforms(t *testing.T) {
	desktopTool := &TouchGestureTool{}
	desktopTool.SetDeviceTypeFunc(func() string { return "windows" })
	desktopTypes := stringEnumPropertyValues(t, desktopTool.ArgsSchema(), "type")
	for _, want := range []string{"tap", "swipe", "swipe_up"} {
		if _, ok := desktopTypes[want]; !ok {
			t.Fatalf("desktop touch_gesture schema missing %q: %v", want, desktopTypes)
		}
	}
	for _, notWant := range []string{"back", "home"} {
		if _, ok := desktopTypes[notWant]; ok {
			t.Fatalf("desktop touch_gesture schema exposed edge gesture %q: %v", notWant, desktopTypes)
		}
	}

	androidTool := &TouchGestureTool{}
	androidTool.SetDeviceTypeFunc(func() string { return "Android" })
	androidTypes := stringEnumPropertyValues(t, androidTool.ArgsSchema(), "type")
	for _, want := range []string{"back", "home"} {
		if _, ok := androidTypes[want]; !ok {
			t.Fatalf("Android touch_gesture schema missing edge gesture %q: %v", want, androidTypes)
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

func TestKeyboardTapDescriptionRoutesSemanticShortcutsToQuickAction(t *testing.T) {
	desc := (&KeyboardTapTool{}).Description()
	for _, want := range []string{"cataloged semantic actions", "MUST use quick_action", "copy", "paste", "select_all", "delete_backward", "delete_forward", "exact physical chords explicitly requested", "app-specific shortcuts not represented", "current run explicitly reports", "Do not infer unavailability", "Never replay an active quick_action binding"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// Key mechanics (backspace/forward-delete, modifiers, key list) now live in the keys ArgsSchema field.
	keysDesc := keyboardTapKeysSchemaDescription(t)
	for _, want := range []string{"backspace", "forward-delete", "semantic deletion", "quick_action delete_backward/delete_forward", "modifier", "modifier-only", "Cataloged semantic actions MUST use quick_action", "explicitly requested physical input", "uncataloged app-specific shortcuts", "current run explicitly reports", "Do not infer unavailability", "replay an active binding"} {
		if !strings.Contains(keysDesc, want) {
			t.Fatalf("keys schema missing %q:\n%s", want, keysDesc)
		}
	}
}

func TestKeyboardTapSchemaListsExtensionKeysForPlatform(t *testing.T) {
	androidTool := &KeyboardTapTool{}
	androidTool.SetDeviceTypeFunc(func() string { return "Android" })
	androidDesc := keyboardTapKeysSchemaDescriptionForTool(t, androidTool)
	for _, want := range []string{"KEYCODE_*", "Android device_type", "single-key Android"} {
		if !strings.Contains(androidDesc, want) {
			t.Fatalf("Android keys schema missing %q:\n%s", want, androidDesc)
		}
	}

	desktopTool := &KeyboardTapTool{}
	desktopTool.SetDeviceTypeFunc(func() string { return "windows" })
	desktopDesc := keyboardTapKeysSchemaDescriptionForTool(t, desktopTool)
	for _, want := range []string{"non-Android device_type", "KEYCODE_SCREENSHOT", "KEYCODE_BRIGHTNESS_UP", "KEYCODE_MEDIA_PLAY_PAUSE"} {
		if !strings.Contains(desktopDesc, want) {
			t.Fatalf("desktop keys schema missing absolute extension key %q:\n%s", want, desktopDesc)
		}
	}
	for _, notWant := range []string{"KEYCODE_HOME", "KEYCODE_BACK", "KEYCODE_APP_SWITCH"} {
		if strings.Contains(desktopDesc, notWant) {
			t.Fatalf("desktop keys schema exposed Android-only key %q:\n%s", notWant, desktopDesc)
		}
	}
}

func keyboardTapKeysSchemaDescription(t *testing.T) string {
	t.Helper()
	return keyboardTapKeysSchemaDescriptionForTool(t, &KeyboardTapTool{})
}

func keyboardTapKeysSchemaDescriptionForTool(t *testing.T, tool *KeyboardTapTool) string {
	t.Helper()
	props, ok := tool.ArgsSchema()["properties"].(map[string]any)
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

func TestTouchGestureRejectsRetiredCoordSpaceField(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"type":"back","coord_space":"normalized"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(out, `unknown field "coord_space"`) {
		t.Fatalf("output = %q, want unknown coord_space field error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestTouchGestureRejectsRetiredCoordSpaceFieldInPoints(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "point",
			input: `{"type":"tap","point":{"x":500,"y":500,"coord_space":"pixel"}}`,
		},
		{
			name:  "start",
			input: `{"type":"swipe","start":{"x":500,"y":800,"coord_space":"pixel"},"end":{"x":500,"y":200}}`,
		},
		{
			name:  "end",
			input: `{"type":"swipe","start":{"x":500,"y":800},"end":{"x":500,"y":200,"coord_space":"pixel"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev, path := newTestHIDDevice(t)
			tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

			out, err := tool.Call(context.Background(), tt.input)
			if err != nil {
				t.Fatalf("Call error: %v", err)
			}
			if !strings.Contains(out, `unknown field "coord_space"`) {
				t.Fatalf("output = %q, want unknown coord_space field error", out)
			}
			if reports := readMouseReports(t, dev, path); len(reports) != 0 {
				t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
			}
		})
	}
}

func TestMouseToolsRejectRetiredCoordSpaceField(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tools := []struct {
		name string
		call func(context.Context, string) (string, error)
	}{
		{name: "mouse_click", call: (&MouseClickTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}).Call},
		{name: "mouse_move", call: (&MouseMoveTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}).Call},
	}
	for _, tool := range tools {
		out, err := tool.call(context.Background(), `{"x":500,"y":500,"coord_space":"normalized"}`)
		if err != nil {
			t.Fatalf("%s Call error: %v", tool.name, err)
		}
		if !strings.Contains(out, `unknown field "coord_space"`) {
			t.Fatalf("%s output = %q, want unknown coord_space field error", tool.name, out)
		}
	}
	if reports := readMouseReports(t, dev, path); len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes for rejected input", len(reports))
	}
}

func TestTouchGestureDragKeepsZeroHoldBeforeMs(t *testing.T) {
	dev, w := newTimedHIDDevice()
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
	screen := &screen.ScreenState{}

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

func TestTouchGestureSwipeRejectsPointInsteadOfStartEnd(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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

func TestTouchGestureRejectsZeroDistanceDrag(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

	out, err := tool.Call(context.Background(), `{"type":"drag","start":{"x":313,"y":513},"end":{"x":313,"y":513},"steps":20}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "drag requires distinct start and end points") {
		t.Fatalf("output = %q, want zero-distance drag error", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 0 {
		t.Fatalf("len(reports) = %d, want no HID writes", len(reports))
	}
}

func TestTouchGestureAcceptsArrayPointFormat(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &TouchGestureTool{pc: testPointerController(dev, &pointerState{}), screen: &screen.ScreenState{}}

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
