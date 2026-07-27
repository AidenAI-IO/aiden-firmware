package agent

import (
	"aiden-agent/internal/agent/screen"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strings"
	"testing"
)

func TestImageDiffUsesTwoLatestScreenshotObservations(t *testing.T) {
	state := &screen.ScreenState{}
	state.UpdateScreenshotWithID(101, solidImageDiffJPEG(t, color.Black), 40, 40)
	state.UpdateScreenshotWithID(102, solidImageDiffJPEG(t, color.White), 40, 40)

	out, err := (&ImageDiffTool{screen: state}).Call(context.Background(), `{"before_id":101,"after_id":102}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v; output=%s", err, out)
	}
	if !result.Changed || result.DiffRatio != 1 {
		t.Fatalf("result = %#v, want changed=true diff_ratio=1", result)
	}
}

func TestImageDiffReportsNoChangeForIdenticalObservations(t *testing.T) {
	state := &screen.ScreenState{}
	jpegData := solidImageDiffJPEG(t, color.RGBA{R: 30, G: 60, B: 90, A: 255})
	state.UpdateScreenshotWithID(201, jpegData, 40, 40)
	state.UpdateScreenshotWithID(202, jpegData, 40, 40)

	out, err := (&ImageDiffTool{screen: state}).Call(context.Background(), `{"before_id":201,"after_id":202,"region":{"x":100,"y":100,"w":800,"h":800}}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v; output=%s", err, out)
	}
	if result.Changed || result.DiffRatio != 0 || result.PrimaryAxis != "none" {
		t.Fatalf("result = %#v, want unchanged", result)
	}
}

func TestImageDiffRequiresTwoScreenshotObservations(t *testing.T) {
	state := &screen.ScreenState{}
	state.UpdateScreenshotWithID(301, solidImageDiffJPEG(t, color.Black), 40, 40)
	ctx, _ := WithToolError(context.Background())

	out, err := (&ImageDiffTool{screen: state}).Call(ctx, `{"before_id":300,"after_id":301}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "requires two screenshot observations") {
		t.Fatalf("output = %q", out)
	}
	if toolErr := ToolErrorFromContext(ctx); toolErr == nil || toolErr.Code != CodeInvalidArguments {
		t.Fatalf("tool error = %#v, want code %q", toolErr, CodeInvalidArguments)
	}
}

func TestImageDiffSchemaDoesNotExposeBase64Arguments(t *testing.T) {
	properties := (&ImageDiffTool{}).ArgsSchema()["properties"].(map[string]any)
	if _, ok := properties["before"]; ok {
		t.Fatalf("schema unexpectedly exposes before: %#v", properties)
	}
	if _, ok := properties["after"]; ok {
		t.Fatalf("schema unexpectedly exposes after: %#v", properties)
	}
	if properties["before_id"] == nil || properties["after_id"] == nil {
		t.Fatalf("schema missing screenshot IDs: %#v", properties)
	}
	if properties["region"] == nil {
		t.Fatalf("schema missing optional region: %#v", properties)
	}
}

func TestImageDiffRejectsScreenshotIDMismatch(t *testing.T) {
	state := &screen.ScreenState{}
	state.UpdateScreenshotWithID(401, solidImageDiffJPEG(t, color.Black), 40, 40)
	state.UpdateScreenshotWithID(402, solidImageDiffJPEG(t, color.White), 40, 40)
	ctx, _ := WithToolError(context.Background())

	out, err := (&ImageDiffTool{screen: state}).Call(ctx, `{"before_id":400,"after_id":402}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "latest pair is 401 -> 402") {
		t.Fatalf("output = %q", out)
	}
}

func TestScreenshotSummaryExposesScreenshotID(t *testing.T) {
	summary, ok := compactScreenshotObservation("screenshot", `{"previous_screenshot_id":700,"screenshot_id":701,"width":40,"height":40,"format":"jpeg","size":10,"data":"eA=="}`)
	if !ok {
		t.Fatal("compactScreenshotObservation rejected screenshot result")
	}
	if !strings.Contains(summary, "previous_screenshot_id=700") || !strings.Contains(summary, "screenshot_id=701") {
		t.Fatalf("summary does not expose screenshot pair: %q", summary)
	}

	compact := stripScreenshotData(`{"previous_screenshot_id":700,"screenshot_id":701,"width":40,"height":40,"format":"jpeg","size":10,"data":"eA=="}`)
	if !strings.Contains(compact, `"previous_screenshot_id":700`) || !strings.Contains(compact, `"screenshot_id":701`) {
		t.Fatalf("compacted screenshot does not preserve screenshot pair: %q", compact)
	}
}

func TestImageDiffAcceptsAutomaticPostActionScreenshot(t *testing.T) {
	state := &screen.ScreenState{}
	beforeJPEG := solidImageDiffJPEG(t, color.Black)
	afterJPEG := solidImageDiffJPEG(t, color.White)
	state.UpdateScreenshotWithID(801, beforeJPEG, 40, 40)

	screenshot := &ScreenshotTool{
		client: &fakeScreenshotFrameClient{
			meta: frameMetadata{
				Seq:          802,
				Width:        40,
				Height:       40,
				SourceWidth:  40,
				SourceHeight: 40,
				CropWidth:    40,
				CropHeight:   40,
				PixelFormat:  "jpeg",
				Bytes:        uint64(len(afterJPEG)),
			},
			data: afterJPEG,
		},
		screen: state,
	}
	action := &stubTool{name: "touch_gesture", output: "ok"}
	wrapped := newPostActionScreenshotTool(action, screenshot, 0)

	postActionOutput, err := wrapped.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":500}}`)
	if err != nil {
		t.Fatalf("post-action tool returned error: %v", err)
	}
	var observation postActionScreenshotResult
	if err := json.Unmarshal([]byte(postActionOutput), &observation); err != nil {
		t.Fatalf("unmarshal post-action observation: %v", err)
	}
	if observation.PreviousScreenshotID != 801 || observation.ScreenshotID != 802 {
		t.Fatalf("post-action screenshot pair = %d -> %d, want 801 -> 802", observation.PreviousScreenshotID, observation.ScreenshotID)
	}

	diffOutput, err := (&ImageDiffTool{screen: state}).Call(context.Background(), `{"before_id":801,"after_id":802}`)
	if err != nil {
		t.Fatalf("image_diff returned error: %v", err)
	}
	var diff imageDiffResult
	if err := json.Unmarshal([]byte(diffOutput), &diff); err != nil {
		t.Fatalf("unmarshal image_diff result: %v", err)
	}
	if !diff.Changed || diff.DiffRatio != 1 {
		t.Fatalf("image_diff result = %#v, want changed=true diff_ratio=1", diff)
	}
}

func solidImageDiffJPEG(t *testing.T, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestImageDiffRegionRejectsInvalidValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 200)
	tests := []imageDiffRegion{
		{X: -100, Y: 0, W: 500, H: 500},
		{X: 0, Y: 1100, W: 500, H: 500},
		{X: 0, Y: 0, W: 0, H: 500},
		{X: 0, Y: 0, W: 500, H: -100},
	}

	for _, tt := range tests {
		_, err := tt.toPixelRect(full)
		if err == nil {
			t.Fatalf("toPixelRect(%+v) succeeded, want error", tt)
		}
	}
}

func TestImageDiffRegionClampsToImageBounds(t *testing.T) {
	full := image.Rect(10, 20, 110, 220)
	got, err := (&imageDiffRegion{X: 800, Y: 750, W: 500, H: 500}).toPixelRect(full)
	if err != nil {
		t.Fatalf("toPixelRect returned error: %v", err)
	}
	want := image.Rect(90, 170, 110, 220)
	if got != want {
		t.Fatalf("toPixelRect = %v, want %v", got, want)
	}
}

func TestImageDiffRegionRejectsNonFiniteValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 100)
	_, err := (&imageDiffRegion{X: 0, Y: 0, W: 500, H: math.NaN()}).toPixelRect(full)
	if err == nil {
		t.Fatal("expected error for non-finite region")
	}
	if !strings.Contains(err.Error(), "finite normalized") {
		t.Fatalf("unexpected error: %v", err)
	}
}
