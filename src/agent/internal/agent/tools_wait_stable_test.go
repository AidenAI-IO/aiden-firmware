package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestScreenStableDefaultsResolved(t *testing.T) {
	t.Parallel()

	defaults := ScreenStableDefaults{}.Resolved()
	if defaults.TimeoutMs != defaultStableWaitTimeoutMs {
		t.Fatalf("TimeoutMs = %d, want %d", defaults.TimeoutMs, defaultStableWaitTimeoutMs)
	}
	if defaults.StableMs != defaultStableDurationMs {
		t.Fatalf("StableMs = %d, want %d", defaults.StableMs, defaultStableDurationMs)
	}

	custom := ScreenStableDefaults{TimeoutMs: 8000, StableMs: 9000}.Resolved()
	if custom.TimeoutMs != 8000 {
		t.Fatalf("TimeoutMs = %d, want 8000", custom.TimeoutMs)
	}
	if custom.StableMs != 8000 {
		t.Fatalf("StableMs = %d, want clamped to 8000", custom.StableMs)
	}
}

func TestScreenStableDefaultsInputJSON(t *testing.T) {
	t.Parallel()

	got := ScreenStableDefaults{TimeoutMs: 4500, StableMs: 600, DiffThreshold: 2.5}.InputJSON()
	want := `{"timeout_ms":4500,"stable_ms":600,"diff_threshold":2.5}`
	if got != want {
		t.Fatalf("InputJSON() = %q, want %q", got, want)
	}
}

func TestWaitStableScreenToolReturnsScreenshotObservationJSON(t *testing.T) {
	rawFrame := []byte{128, 128, 128, 128, 128, 128}
	jpegData, err := encodeJPEG([]byte{
		255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 255,
	}, 2, 2, screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("encodeJPEG() error = %v", err)
	}
	screen := &screenState{}
	client := &fakeWaitStableFrameClient{
		rawFrames: []fakeWaitStableFrame{
			{meta: frameMetadata{Seq: 1, Width: 2, Height: 2, PixelFormat: "nv12"}, data: rawFrame},
			{meta: frameMetadata{Seq: 2, Width: 2, Height: 2, PixelFormat: "nv12"}, data: rawFrame},
		},
		jpegData: jpegData,
	}
	tool := &WaitStableScreenTool{
		client:   client,
		defaults: ScreenStableDefaults{TimeoutMs: 50, StableMs: 1, DiffThreshold: 2},
		screen:   screen,
	}

	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("Call() returned error output: %s", out)
	}

	var result waitStableScreenObservationResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid wait screenshot JSON: %v", err)
	}
	if !result.OK || !result.Stable {
		t.Fatalf("stable result = ok:%v stable:%v, want true/true", result.OK, result.Stable)
	}
	if result.ScreenStable == nil || !*result.ScreenStable {
		t.Fatalf("ScreenStable = %#v, want true", result.ScreenStable)
	}
	if result.StableWaitMs == nil || *result.StableWaitMs != result.ElapsedMs {
		t.Fatalf("StableWaitMs = %#v, elapsed_ms=%d", result.StableWaitMs, result.ElapsedMs)
	}
	if result.Width != 2 || result.Height != 2 || result.Format != "jpeg" || result.Size != len(jpegData) {
		t.Fatalf("unexpected screenshot metadata: %#v", result.screenshotResult)
	}
	if result.Data != base64.StdEncoding.EncodeToString(jpegData) {
		t.Fatalf("unexpected screenshot data: %q", result.Data)
	}
	if client.jpegCalls != 1 {
		t.Fatalf("jpegCalls = %d, want 1", client.jpegCalls)
	}
	if width, height, _, ok := screen.DimensionsWithAge(); !ok || width != 2 || height != 2 {
		t.Fatalf("screen dimensions = %dx%d ok=%v, want 2x2 true", width, height, ok)
	}
	visual, ok := any(tool).(visualObservationTool)
	if !ok || !visual.ReturnsVisualObservation() {
		t.Fatalf("wait_for_stable_screen must be a visual observation tool")
	}
}

func TestPostActionScreenshotToolUsesInternalStableWaitWithoutWaitScreenshot(t *testing.T) {
	action := &stubTool{name: "touch_gesture", output: "ok"}
	waitStable := &fakeInternalWaitTool{
		result: waitStableScreenResult{OK: true, Stable: true, ElapsedMs: 12},
	}
	screenshot := &stubTool{
		name:   "screenshot",
		output: `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ=="}`,
	}
	tool := newPostActionStableScreenshotTool(action, waitStable, screenshot, 0, ScreenStableDefaults{TimeoutMs: 50, StableMs: 1, DiffThreshold: 2})

	out, err := tool.Call(context.Background(), `{"type":"tap"}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("Call() returned error output: %s", out)
	}
	if waitStable.callCount != 0 {
		t.Fatalf("wait tool Call count = %d, want 0", waitStable.callCount)
	}
	if waitStable.waitCount != 1 {
		t.Fatalf("wait count = %d, want 1", waitStable.waitCount)
	}
	if len(screenshot.inputs) != 1 {
		t.Fatalf("post-action screenshot inputs = %#v, want one capture", screenshot.inputs)
	}
}

type fakeWaitStableFrame struct {
	meta frameMetadata
	data []byte
}

type fakeWaitStableFrameClient struct {
	rawFrames []fakeWaitStableFrame
	rawCalls  int
	jpegData  []byte
	jpegCalls int
}

func (c *fakeWaitStableFrameClient) LatestFrame() (*frameMetadata, []byte, error) {
	if len(c.rawFrames) == 0 {
		return nil, nil, fmt.Errorf("no raw frames")
	}
	index := c.rawCalls
	if index >= len(c.rawFrames) {
		index = len(c.rawFrames) - 1
	}
	c.rawCalls++
	frame := c.rawFrames[index]
	meta := frame.meta
	return &meta, append([]byte(nil), frame.data...), nil
}

func (c *fakeWaitStableFrameClient) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, error) {
	if format != "jpeg" {
		return nil, nil, fmt.Errorf("unexpected format %q", format)
	}
	if quality != screenshotJPEGQuality {
		return nil, nil, fmt.Errorf("quality = %d, want %d", quality, screenshotJPEGQuality)
	}
	c.jpegCalls++
	meta := &frameMetadata{
		Seq:         99,
		Width:       2,
		Height:      2,
		PixelFormat: "jpeg",
		Bytes:       uint64(len(c.jpegData)),
	}
	return meta, append([]byte(nil), c.jpegData...), nil
}

type fakeInternalWaitTool struct {
	result    waitStableScreenResult
	err       error
	callCount int
	waitCount int
}

func (t *fakeInternalWaitTool) Name() string { return "wait_for_stable_screen" }

func (t *fakeInternalWaitTool) Description() string { return "fake wait" }

func (t *fakeInternalWaitTool) Call(context.Context, string) (string, error) {
	t.callCount++
	return "error: Call should not be used by post-action wrapper", nil
}

func (t *fakeInternalWaitTool) wait(context.Context, string) (waitStableScreenResult, error) {
	t.waitCount++
	return t.result, t.err
}
