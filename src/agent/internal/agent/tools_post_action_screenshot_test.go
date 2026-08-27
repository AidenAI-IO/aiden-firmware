package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStripScreenshotDataPreservesStableScreenStatus(t *testing.T) {
	content := `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ==","action_output":" completed ","screen_stable":false,"stable_wait_ms":0,"screen_changed":true,"last_diff":1.25,"gesture_marker":{"type":"tap","x":250,"y":750}}`

	stripped := stripScreenshotData(content)

	if strings.Contains(stripped, "ZmFrZQ==") || strings.Contains(stripped, `"data"`) {
		t.Fatalf("screenshot data was not stripped: %s", stripped)
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(stripped), &result); err != nil {
		t.Fatalf("decode stripped result: %v", err)
	}
	if result.Width != 320 || result.Height != 240 || result.Format != "jpeg" || result.Size != 4 {
		t.Fatalf("screenshot metadata = %#v", result)
	}
	if result.ActionOutput != "completed" {
		t.Fatalf("ActionOutput = %q, want completed", result.ActionOutput)
	}
	if result.ScreenStable == nil || *result.ScreenStable {
		t.Fatalf("ScreenStable = %#v, want false", result.ScreenStable)
	}
	if result.StableWaitMs == nil || *result.StableWaitMs != 0 {
		t.Fatalf("StableWaitMs = %#v, want 0", result.StableWaitMs)
	}
	if result.ScreenChanged == nil || !*result.ScreenChanged {
		t.Fatalf("ScreenChanged = %#v, want true", result.ScreenChanged)
	}
	if result.LastDiff == nil || *result.LastDiff != 1.25 {
		t.Fatalf("LastDiff = %#v, want 1.25", result.LastDiff)
	}
	if result.GestureMarker == nil || result.GestureMarker.Type != "tap" || result.GestureMarker.X != 250 || result.GestureMarker.Y != 750 {
		t.Fatalf("GestureMarker = %#v", result.GestureMarker)
	}
}

func TestParseTouchGesturePostMarker(t *testing.T) {
	for _, gestureType := range []string{"tap", "double_tap", "long_press"} {
		marker, ok := parseTouchGesturePostMarker(`{"type":"` + gestureType + `","point":{"x":125,"y":875}}`)
		if !ok || marker.Type != gestureType || marker.X != 125 || marker.Y != 875 {
			t.Fatalf("%s marker = %#v, %v", gestureType, marker, ok)
		}
	}
	for _, input := range []string{
		`{"type":"tap","point":["125","875"]}`,
		`{"type":"tap","point":{"x":"125","y":"875"}}`,
	} {
		marker, ok := parseTouchGesturePostMarker(input)
		if !ok || marker.Type != "tap" || marker.X != 125 || marker.Y != 875 {
			t.Fatalf("compatibility point marker for %s = %#v, %v", input, marker, ok)
		}
	}
	for _, input := range []string{
		`{"type":"swipe","point":{"x":125,"y":875}}`,
		`{"type":"home","point":{"x":125,"y":875}}`,
		`{"type":"tap"}`,
		`{"type":"tap","point":{"x":125}}`,
		`{"type":"tap","point":{"y":875}}`,
		`{"type":"tap","point":{"x":-1,"y":875}}`,
		`{"type":"tap","point":{"x":125,"y":1001}}`,
	} {
		if marker, ok := parseTouchGesturePostMarker(input); ok {
			t.Fatalf("unexpected marker for %s: %#v", input, marker)
		}
	}
}

func TestPostActionTouchGestureReturnsRawScreenshotWithMarkerMetadata(t *testing.T) {
	const rawData = "ZmFrZS1qcGVn"
	action := &stubTool{name: "touch_gesture", output: "ok"}
	screenshot := &stubTool{name: "screenshot", output: `{"width":320,"height":240,"format":"jpeg","size":9,"data":"` + rawData + `"}`}
	tool := newPostActionScreenshotTool(action, screenshot, 0)

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":250,"y":750}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Data != rawData {
		t.Fatalf("tool JSON screenshot data changed: %q", result.Data)
	}
	if result.GestureMarker == nil || result.GestureMarker.Type != "tap" || result.GestureMarker.X != 250 || result.GestureMarker.Y != 750 {
		t.Fatalf("GestureMarker = %#v", result.GestureMarker)
	}
}

func TestPostActionTouchGestureDoesNotMarkSwipe(t *testing.T) {
	action := &stubTool{name: "touch_gesture", output: "ok"}
	screenshot := &stubTool{name: "screenshot", output: `{"width":320,"height":240,"format":"jpeg","size":9,"data":"ZmFrZS1qcGVn"}`}
	tool := newPostActionScreenshotTool(action, screenshot, 0)

	out, err := tool.Call(context.Background(), `{"type":"swipe","point":{"x":250,"y":750}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.GestureMarker != nil {
		t.Fatalf("swipe GestureMarker = %#v, want nil", result.GestureMarker)
	}
}
