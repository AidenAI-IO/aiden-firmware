package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"
	"testing"
)

func TestTerminationPolicyRepeatWithoutProgressTerminates(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	observation := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	for i := 0; i < 2; i++ {
		if decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, observation, false); decision.Stop {
			t.Fatalf("iteration %d: unexpected stop: %#v", i+1, decision)
		}
	}
	decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, observation, false)
	if !decision.Stop || decision.Reason != StopReasonLoopDetected {
		t.Fatalf("expected loop termination, got %#v", decision)
	}
}

func TestTerminationPolicyRestrictsActionToolsAndRecoversAfterProgress(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	sameScreen := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	for i := 0; i < 2; i++ {
		if decision := policy.AfterToolCall("touch_gesture", `{"gesture":"swipe_up"}`, sameScreen, false); decision.Stop {
			t.Fatalf("iteration %d: unexpected stop: %#v", i+1, decision)
		}
	}
	if policy.tier < TierRestrictTools {
		t.Fatalf("tier = %d, want restrict tier", policy.tier)
	}
	if _, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`); allowed {
		t.Fatal("expected action tool blocked at restrict tier")
	}
	if _, allowed := policy.BeforeToolCall("quick_action", `{"action":"app_drawer"}`); !allowed {
		t.Fatal("a different action tool should remain available as a strategy change")
	}

	changedScreen := terminationPolicyScreenshotObservation(t, 200, 400, image.Rect(20, 100, 120, 200))
	progress := policy.AfterToolCall("screenshot", `{}`, changedScreen, false)
	if progress.Stop || policy.tier != TierNone || policy.stallScore != 0 {
		t.Fatalf("progress should clear restriction, decision=%#v score=%d tier=%d", progress, policy.stallScore, policy.tier)
	}
	if _, allowed := policy.BeforeToolCall("touch_gesture", `{"gesture":"swipe_up"}`); !allowed {
		t.Fatal("action tool should be allowed after progress")
	}
}

func TestTerminationPolicyIgnoresTopStatusBarChanges(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	before := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	after := terminationPolicyScreenshotObservation(t, 200, 400, image.Rect(0, 0, 200, 32))

	policy.AfterToolCall("screenshot", `{}`, before, false)
	policy.stallScore = 1
	policy.refreshTier()
	decision := policy.AfterToolCall("screenshot", `{}`, after, false)
	if decision.Stop {
		t.Fatalf("top-only change should not immediately stop the run: %#v", decision)
	}
	if policy.stallScore == 0 || policy.tier == TierNone {
		t.Fatalf("top-only change was incorrectly treated as progress: score=%d tier=%d", policy.stallScore, policy.tier)
	}
}

func TestTerminationPolicyIgnoresSubOnePercentPixelChanges(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	before := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	after := terminationPolicyScreenshotObservation(t, 200, 400, image.Rect(20, 100, 30, 110))

	policy.AfterToolCall("screenshot", `{}`, before, false)
	policy.stallScore = 1
	policy.refreshTier()
	decision := policy.AfterToolCall("screenshot", `{}`, after, false)
	if decision.Stop {
		t.Fatalf("sub-threshold change should not immediately stop the run: %#v", decision)
	}
	if policy.stallScore == 0 || policy.tier == TierNone {
		t.Fatalf("sub-threshold change was incorrectly treated as progress: score=%d tier=%d", policy.stallScore, policy.tier)
	}
}

func TestTerminationPolicyAcceptsLocalizedPixelChanges(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	before := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	after := terminationPolicyScreenshotObservation(t, 200, 400, image.Rect(20, 100, 80, 160))

	policy.AfterToolCall("screenshot", `{}`, before, false)
	policy.stallScore = 4
	policy.refreshTier()
	decision := policy.AfterToolCall("screenshot", `{}`, after, false)
	if decision.Stop || policy.stallScore != 0 || policy.tier != TierNone {
		t.Fatalf("localized structural change should count as progress: decision=%#v score=%d tier=%d", decision, policy.stallScore, policy.tier)
	}
}

func TestTerminationPolicyAcceptsLargePageChanges(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	before := terminationPolicyScreenshotObservation(t, 200, 400, image.Rectangle{})
	after := terminationPolicyScreenshotObservation(t, 200, 400, image.Rect(20, 100, 120, 200))

	policy.AfterToolCall("screenshot", `{}`, before, false)
	policy.stallScore = 4
	policy.refreshTier()
	decision := policy.AfterToolCall("screenshot", `{}`, after, false)
	if decision.Stop || policy.stallScore != 0 || policy.tier != TierNone {
		t.Fatalf("large page change should count as progress: decision=%#v score=%d tier=%d", decision, policy.stallScore, policy.tier)
	}
}

func TestComputeImageDiffUsesStrictOnePercentThreshold(t *testing.T) {
	before := image.NewRGBA(image.Rect(0, 0, 100, 100))
	after := image.NewRGBA(before.Bounds())
	draw.Draw(after, after.Bounds(), before, before.Bounds().Min, draw.Src)
	changed := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for x := 0; x < 100; x++ {
		after.Set(x, 0, changed)
	}

	result, err := computeImageDiff(before, after, before.Bounds())
	if err != nil {
		t.Fatalf("computeImageDiff exact threshold: %v", err)
	}
	if result.Changed {
		t.Fatalf("exactly 1%% changed pixels should remain unchanged: %#v", result)
	}

	after.Set(0, 1, changed)
	result, err = computeImageDiff(before, after, before.Bounds())
	if err != nil {
		t.Fatalf("computeImageDiff above threshold: %v", err)
	}
	if !result.Changed {
		t.Fatalf("more than 1%% changed pixels should count as changed: %#v", result)
	}
}

func TestScreenshotProgressIgnoresDistributedPixelNoise(t *testing.T) {
	before := image.NewRGBA(image.Rect(0, 0, 400, 800))
	after := image.NewRGBA(before.Bounds())
	changed := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	bounds := screenshotProgressBounds(before.Bounds())
	for row := 0; row < screenshotProgressGridRows; row++ {
		y := bounds.Min.Y + bounds.Dy()*row/screenshotProgressGridRows
		for column := 0; column < screenshotProgressGridColumns; column++ {
			x := bounds.Min.X + bounds.Dx()*column/screenshotProgressGridColumns
			after.Set(x, y, changed)
		}
	}

	result, err := computeImageDiff(before, after, bounds)
	if err != nil {
		t.Fatalf("compute raw image diff: %v", err)
	}
	if !result.Changed {
		t.Fatalf("fixture should exceed raw image_diff 1%% threshold: %#v", result)
	}
	if progress, comparable := screenshotProgressChanged(before, after); !comparable || progress {
		t.Fatalf("distributed one-pixel noise should not count as structural progress: progress=%v comparable=%v", progress, comparable)
	}
}

func TestScreenshotProgressUsesStrictOnePercentChangedBlockThreshold(t *testing.T) {
	before := image.NewRGBA(image.Rect(0, 0, 400, 800))
	after := image.NewRGBA(before.Bounds())
	changed := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	bounds := screenshotProgressBounds(before.Bounds())
	for index := 0; index < 32; index++ {
		fillScreenshotProgressGridCell(after, bounds, index, changed)
	}

	if progress, comparable := screenshotProgressChanged(before, after); !comparable || progress {
		t.Fatalf("exactly 1%% changed blocks should not count as progress: progress=%v comparable=%v", progress, comparable)
	}

	fillScreenshotProgressGridCell(after, bounds, 32, changed)
	if progress, comparable := screenshotProgressChanged(before, after); !comparable || !progress {
		t.Fatalf("more than 1%% changed blocks should count as progress: progress=%v comparable=%v", progress, comparable)
	}
}

func TestTerminationPolicyParseFailureLimit(t *testing.T) {
	policy := NewTerminationPolicy(TerminationPolicyConfig{ParseFailureLimit: 2})
	if decision := policy.RecordParseFailure(); decision.Stop {
		t.Fatalf("first parse failure should not stop: %#v", decision)
	}
	decision := policy.RecordParseFailure()
	if !decision.Stop || decision.Reason != StopReasonParseFailure {
		t.Fatalf("second parse failure should stop: %#v", decision)
	}
}

func TestTerminationPolicySameResultRequiresSameSignature(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	for _, input := range []string{`{"q":"a"}`, `{"q":"b"}`, `{"q":"c"}`} {
		decision := policy.AfterToolCall("web_search", input, `{"ok":true,"value":"same"}`, false)
		if decision.Stop || policy.sameResultStreak != 1 {
			t.Fatalf("input %s should not count as a repeated action: %#v, streak=%d", input, decision, policy.sameResultStreak)
		}
	}
}

func TestTerminationPolicyExternalCancel(t *testing.T) {
	policy := NewTerminationPolicy(DefaultTerminationPolicyConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decision := policy.CheckBeforeIteration(ctx, 1, 10)
	if !decision.Stop || decision.Reason != StopReasonExternal || !strings.Contains(decision.Message, "canceled") {
		t.Fatalf("expected external stop, got %#v", decision)
	}
}

func terminationPolicyScreenshotObservation(t *testing.T, width, height int, changed image.Rectangle) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 32, G: 48, B: 64, A: 255}}, image.Point{}, draw.Src)
	if !changed.Empty() {
		draw.Draw(img, changed.Intersect(img.Bounds()), &image.Uniform{C: color.RGBA{R: 240, G: 224, B: 208, A: 255}}, image.Point{}, draw.Src)
	}

	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode test screenshot: %v", err)
	}
	payload := struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Format string `json:"format"`
		Size   int    `json:"size"`
		Data   string `json:"data"`
	}{
		Width:  width,
		Height: height,
		Format: "jpeg",
		Size:   jpegData.Len(),
		Data:   base64.StdEncoding.EncodeToString(jpegData.Bytes()),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal test screenshot observation: %v", err)
	}
	return string(encoded)
}

func fillScreenshotProgressGridCell(img draw.Image, bounds image.Rectangle, index int, fill color.Color) {
	row := index / screenshotProgressGridColumns
	column := index % screenshotProgressGridColumns
	x0 := bounds.Min.X + bounds.Dx()*column/screenshotProgressGridColumns
	x1 := bounds.Min.X + bounds.Dx()*(column+1)/screenshotProgressGridColumns
	y0 := bounds.Min.Y + bounds.Dy()*row/screenshotProgressGridRows
	y1 := bounds.Min.Y + bounds.Dy()*(row+1)/screenshotProgressGridRows
	draw.Draw(img, image.Rect(x0, y0, x1, y1), &image.Uniform{C: fill}, image.Point{}, draw.Src)
}
