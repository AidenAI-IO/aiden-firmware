package agent

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
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
	tool := &TouchGestureTool{dev: dev, screen: &screenState{}}

	out, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":0.1,"y":0.9},"end":{"x":0.9,"y":0.1},"steps":3,"duration_ms":0}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call output = %q, want ok", out)
	}

	reports := readMouseReports(t, dev, path)
	if len(reports) != 5 {
		t.Fatalf("len(reports) = %d, want 5", len(reports))
	}
	if reports[0].buttons != 0x01 {
		t.Fatalf("press buttons = %d, want 1", reports[0].buttons)
	}
	if reports[0].x != 3277 || reports[0].y != 29490 {
		t.Fatalf("press point = (%d,%d), want (3277,29490)", reports[0].x, reports[0].y)
	}
	if reports[3].x != 29490 || reports[3].y != 3277 || reports[3].buttons != 0x01 {
		t.Fatalf("final move = (%d,%d,%d), want (29490,3277,1)", reports[3].x, reports[3].y, reports[3].buttons)
	}
	if reports[4].x != 29490 || reports[4].y != 3277 || reports[4].buttons != 0x00 {
		t.Fatalf("release = (%d,%d,%d), want (29490,3277,0)", reports[4].x, reports[4].y, reports[4].buttons)
	}
}

func TestMouseMoveAutoFallsBackToAbsoluteWithoutScreenDimensions(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &MouseMoveTool{dev: dev, screen: &screenState{}}

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

type mouseReport struct {
	buttons uint8
	x       uint16
	y       uint16
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
			buttons: data[i+1],
			x:       binary.LittleEndian.Uint16(data[i+2 : i+4]),
			y:       binary.LittleEndian.Uint16(data[i+4 : i+6]),
		})
	}
	return reports
}
