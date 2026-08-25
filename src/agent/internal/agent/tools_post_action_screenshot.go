package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log"
	"math"
	"strings"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const postActionScreenshotDelay = time.Second
const postActionCompletedDetail = "action_completed"

type postActionScreenshotResult struct {
	screenshotResult
	ActionOutput  string                      `json:"action_output,omitempty"`
	ScreenStable  *bool                       `json:"screen_stable,omitempty"`
	StableWaitMs  *int64                      `json:"stable_wait_ms,omitempty"`
	ScreenChanged *bool                       `json:"screen_changed,omitempty"`
	LastDiff      *float64                    `json:"last_diff,omitempty"`
	GestureMarker *touchGesturePostMarkerInfo `json:"gesture_marker,omitempty"`
}

type touchGesturePostMarkerInfo struct {
	Type string  `json:"type"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// stripScreenshotData removes the base64 payload while retaining metadata used
// by session memory, logs, and tool consumers.
func stripScreenshotData(content string) string {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(content), &result); err != nil || result.Data == "" {
		return content
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	compact := map[string]interface{}{
		"width":  result.Width,
		"height": result.Height,
		"format": format,
		"size":   result.Size,
	}
	if strings.TrimSpace(result.ActionOutput) != "" {
		compact["action_output"] = strings.TrimSpace(result.ActionOutput)
	}
	if result.ScreenStable != nil {
		compact["screen_stable"] = *result.ScreenStable
	}
	if result.StableWaitMs != nil {
		compact["stable_wait_ms"] = *result.StableWaitMs
	}
	if result.ScreenChanged != nil {
		compact["screen_changed"] = *result.ScreenChanged
	}
	if result.LastDiff != nil {
		compact["last_diff"] = *result.LastDiff
	}
	data, err := json.Marshal(compact)
	if err != nil {
		return content
	}
	return string(data)
}

type postActionScreenshotTool struct {
	inner                 langtools.Tool
	waitStable            langtools.Tool
	screenshot            langtools.Tool
	delay                 time.Duration
	waitInput             string
	defaults              ScreenStableDefaults
	markTouchGesturePoint bool
}

type postActionScreenshotOption func(*postActionScreenshotTool)

func withTouchGesturePostMarker(enabled bool) postActionScreenshotOption {
	return func(tool *postActionScreenshotTool) {
		tool.markTouchGesturePoint = enabled
	}
}

type stableScreenWaiter interface {
	wait(context.Context, string) (waitStableScreenResult, error)
}

func newPostActionScreenshotTool(inner langtools.Tool, screenshot langtools.Tool, delay time.Duration) langtools.Tool {
	return newPostActionStableScreenshotTool(inner, nil, screenshot, delay, ScreenStableDefaults{})
}

func newPostActionStableScreenshotTool(inner langtools.Tool, waitStable langtools.Tool, screenshot langtools.Tool, delay time.Duration, defaults ScreenStableDefaults, options ...postActionScreenshotOption) langtools.Tool {
	tool := &postActionScreenshotTool{
		inner:      inner,
		waitStable: waitStable,
		screenshot: screenshot,
		delay:      delay,
		waitInput:  defaults.InputJSON(),
		defaults:   defaults,
	}
	for _, option := range options {
		if option != nil {
			option(tool)
		}
	}
	return tool
}

func (t *postActionScreenshotTool) Name() string {
	return t.inner.Name()
}

func (t *postActionScreenshotTool) Description() string {
	markerDescription := ""
	if t.markTouchGesturePoint {
		markerDescription = " For tap, double_tap, and long_press, the returned screenshot is annotated with red and white concentric hollow rings centered at the requested coordinate. Judge only whether the rings' shared center lies inside the intended visible target; the ring boundaries do not indicate the touch area or target overlap. The marker is requested-coordinate feedback, not independently measured physical touch feedback."
	}
	if t.waitStable != nil {
		return t.inner.Description() + " On successful execution, captures a pre-action baseline, waits for the screen to become stable (or until the configured timeout), and returns the final screenshot observation. screen_changed compares the pre-action baseline with the final stable screenshot using meaningful structural change detection; it ignores the top status area and minor image noise. When screen_changed=false and the action was expected to change the UI, do not assume success and inspect the screenshot before answering or repeating. screen_stable=false means the screen was still changing (for example during video playback) but the screenshot was still captured." + markerDescription
	}
	return fmt.Sprintf(
		"%s Captures a pre-action baseline and, on successful execution, waits %s before returning the final screenshot observation. screen_changed reports meaningful structural change between those screenshots while ignoring the top status area and minor image noise.%s",
		t.inner.Description(),
		t.delay,
		markerDescription,
	)
}

func (t *postActionScreenshotTool) ReturnsVisualObservation() bool {
	return true
}

func (t *postActionScreenshotTool) ArgsSchema() map[string]any {
	structured, ok := t.inner.(structuredInputTool)
	if !ok {
		return nil
	}
	return structured.ArgsSchema()
}

func (t *postActionScreenshotTool) Call(ctx context.Context, input string) (string, error) {
	touchscreenRCALogf("post_action start inner=%q input_len=%d", t.inner.Name(), len(input))
	baselineImage := t.captureBaseline(ctx)
	actionOutput, err := t.inner.Call(ctx, input)
	if err != nil {
		touchscreenRCALogf("post_action inner error inner=%q err_type=%T", t.inner.Name(), err)
		return "", err
	}
	touchscreenRCALogf("post_action inner completed inner=%q output_len=%d", t.inner.Name(), len(actionOutput))
	if te := ToolErrorFromContext(ctx); te != nil {
		touchscreenRCALogf("post_action inner tool_error inner=%q code=%q category=%q", t.inner.Name(), te.Code, te.Category)
		return toolErrorString(te), nil
	}
	if legacyToolOutputLooksLikeError(actionOutput) {
		trimmed := strings.TrimSpace(actionOutput)
		te := NewToolError(CodeToolExecutionFailed, strings.TrimSpace(trimmed[len("error:"):]))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	var waitResult waitStableScreenResult
	if t.waitStable != nil {
		touchscreenRCALogf("post_action wait_stable start inner=%q", t.inner.Name())
		if waiter, ok := t.waitStable.(stableScreenWaiter); ok {
			waitResult, err = waiter.wait(ctx, t.waitInput)
			if err != nil {
				touchscreenRCALogf("post_action wait_stable error inner=%q err_type=%T", t.inner.Name(), err)
				return postActionErrorResultf(ctx, postActionErrorCode(err), "%s completed with output %q, but stable-screen wait failed: %v", t.inner.Name(), actionOutput, err), nil
			}
		} else {
			waitOutput, err := t.waitStable.Call(ctx, t.waitInput)
			if err != nil {
				touchscreenRCALogf("post_action wait_stable call error inner=%q err_type=%T", t.inner.Name(), err)
				return postActionErrorResultf(ctx, postActionErrorCode(err), "%s completed with output %q, but stable-screen wait failed: %v", t.inner.Name(), actionOutput, err), nil
			}
			if te := ToolErrorFromContext(ctx); te != nil {
				return wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, te.Message), nil
			}
			if legacyToolOutputLooksLikeError(waitOutput) {
				return postActionErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
			}
			if err := json.Unmarshal([]byte(waitOutput), &waitResult); err != nil {
				return postActionErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait returned invalid JSON: %v", t.inner.Name(), actionOutput, err), nil
			}
			if !waitResult.OK {
				return postActionErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
			}
		}
		resolved := t.defaults.Resolved()
		log.Printf("[INFO] stable-screen: tool=%s timeout_ms=%d elapsed_ms=%d stable=%v", t.inner.Name(), resolved.TimeoutMs, waitResult.ElapsedMs, waitResult.Stable)
		if !waitResult.OK {
			return postActionErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed", t.inner.Name(), actionOutput), nil
		}
		touchscreenRCALogf(
			"post_action wait_stable completed inner=%q stable=%v elapsed_ms=%d screen_changed=%v last_diff=%s",
			t.inner.Name(),
			waitResult.Stable,
			waitResult.ElapsedMs,
			waitResult.ScreenChanged != nil && *waitResult.ScreenChanged,
			formatTouchscreenRCAFloatPtr(waitResult.LastDiff),
		)
	} else if err := waitForPostActionScreenshot(ctx, t.delay); err != nil {
		return postActionErrorResultf(ctx, postActionErrorCode(err), "%s completed with output %q, but post-action delay failed: %v", t.inner.Name(), actionOutput, err), nil
	}

	touchscreenRCALogf("post_action screenshot start inner=%q", t.inner.Name())
	screenshotOutput, err := t.screenshot.Call(ctx, "{}")
	if err != nil {
		touchscreenRCALogf("post_action screenshot error inner=%q err_type=%T", t.inner.Name(), err)
		return postActionErrorResultf(ctx, postActionErrorCode(err), "%s completed with output %q, but post-action screenshot failed: %v", t.inner.Name(), actionOutput, err), nil
	}
	if te := ToolErrorFromContext(ctx); te != nil {
		return wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but post-action screenshot failed: %s", t.inner.Name(), actionOutput, te.Message), nil
	}

	var result screenshotResult
	if err := json.Unmarshal([]byte(screenshotOutput), &result); err != nil {
		return postActionErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but post-action screenshot was invalid: %v", t.inner.Name(), actionOutput, err), nil
	}
	touchscreenRCALogf("post_action screenshot completed inner=%q display=%dx%d capture_backend=%q size=%d", t.inner.Name(), result.Width, result.Height, result.CaptureBackend, result.Size)

	payload := postActionScreenshotResult{
		screenshotResult: result,
		ActionOutput:     actionOutput,
	}
	if finalImage, _ := extractScreenshotImage(screenshotOutput); baselineImage != nil && finalImage != nil {
		if changed, comparable := screenshotProgressChanged(baselineImage, finalImage); comparable {
			payload.ScreenChanged = &changed
			touchscreenRCALogf("post_action progress_compare inner=%q screen_changed=%v", t.inner.Name(), changed)
		}
	}
	if t.waitStable != nil {
		stable := waitResult.Stable
		elapsed := waitResult.ElapsedMs
		payload.ScreenStable = &stable
		payload.StableWaitMs = &elapsed
		payload.LastDiff = waitResult.LastDiff
	}
	if t.markTouchGesturePoint {
		if marker, ok := parseTouchGesturePostMarker(input); ok {
			payload.GestureMarker = &marker
		}
	}

	out, _ := json.Marshal(payload)
	return string(out), nil
}

func (t *postActionScreenshotTool) captureBaseline(ctx context.Context) image.Image {
	baselineCtx, _ := WithToolError(ctx)
	touchscreenRCALogf("post_action baseline start inner=%q", t.inner.Name())
	output, err := t.screenshot.Call(baselineCtx, "{}")
	if err != nil {
		touchscreenRCALogf("post_action baseline unavailable inner=%q err_type=%T", t.inner.Name(), err)
		return nil
	}
	if te := ToolErrorFromContext(baselineCtx); te != nil {
		touchscreenRCALogf("post_action baseline unavailable inner=%q code=%q category=%q", t.inner.Name(), te.Code, te.Category)
		return nil
	}
	img, _ := extractScreenshotImage(output)
	if img == nil {
		touchscreenRCALogf("post_action baseline unavailable inner=%q reason=invalid_screenshot", t.inner.Name())
		return nil
	}
	touchscreenRCALogf("post_action baseline completed inner=%q", t.inner.Name())
	return img
}

func parseTouchGesturePostMarker(input string) (touchGesturePostMarkerInfo, bool) {
	var args struct {
		Type  string `json:"type"`
		Point *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"point"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil || args.Point == nil {
		return touchGesturePostMarkerInfo{}, false
	}
	gestureType := strings.ToLower(strings.TrimSpace(args.Type))
	switch gestureType {
	case "tap", "double_tap", "long_press":
	default:
		return touchGesturePostMarkerInfo{}, false
	}
	if !finiteNormalizedCoordinate(args.Point.X) || !finiteNormalizedCoordinate(args.Point.Y) {
		return touchGesturePostMarkerInfo{}, false
	}
	return touchGesturePostMarkerInfo{Type: gestureType, X: args.Point.X, Y: args.Point.Y}, true
}

func finiteNormalizedCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1000
}

func drawTouchGesturePostMarker(jpegData []byte, marker touchGesturePostMarkerInfo) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot jpeg: %w", err)
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid screenshot dimensions %dx%d", width, height)
	}

	marked := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(marked, marked.Bounds(), img, bounds.Min, draw.Src)
	x := int(math.Round(marker.X / 1000 * float64(max(width-1, 0))))
	y := int(math.Round(marker.Y / 1000 * float64(max(height-1, 0))))
	drawTouchMarker(marked, x, y)

	var output bytes.Buffer
	if err := jpeg.Encode(&output, marked, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("encode marked screenshot jpeg: %w", err)
	}
	return output.Bytes(), nil
}

func drawTouchMarker(img *image.RGBA, x, y int) {
	if img == nil {
		return
	}
	minDimension := min(img.Bounds().Dx(), img.Bounds().Dy())
	radius := max(10, min(24, minDimension/28))
	outer := radius + 3
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red := color.RGBA{R: 255, G: 32, B: 32, A: 255}

	drawMarkerRing(img, x, y, outer, 3, white)
	drawMarkerRing(img, x, y, radius, 3, red)
}

func drawMarkerRing(img *image.RGBA, centerX, centerY, radius, thickness int, c color.Color) {
	outerSquared := radius * radius
	innerRadius := max(radius-thickness, 0)
	innerSquared := innerRadius * innerRadius
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			dx := x - centerX
			dy := y - centerY
			distanceSquared := dx*dx + dy*dy
			if distanceSquared <= outerSquared && distanceSquared >= innerSquared {
				setMarkerPixel(img, x, y, c)
			}
		}
	}
}

func setMarkerPixel(img *image.RGBA, x, y int, c color.Color) {
	if image.Pt(x, y).In(img.Bounds()) {
		img.Set(x, y, c)
	}
}

func wrapPostActionSubtoolError(ctx context.Context, te *ToolError, format string, args ...any) string {
	details := make(map[string]any, len(te.Details)+1)
	for key, value := range te.Details {
		details[key] = value
	}
	details[postActionCompletedDetail] = true
	wrapped := NewToolErrorWithDetails(te.Code, fmt.Sprintf(format, args...), details)
	SetToolError(ctx, wrapped)
	return toolErrorString(wrapped)
}

func postActionErrorResultf(ctx context.Context, code string, format string, args ...any) string {
	toolErr := NewToolErrorWithDetails(code, fmt.Sprintf(format, args...), map[string]any{postActionCompletedDetail: true})
	SetToolError(ctx, toolErr)
	return toolErrorString(toolErr)
}

func postActionErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeDeadlineExceeded
	default:
		return CodeToolExecutionFailed
	}
}

func waitForPostActionScreenshot(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *postActionScreenshotTool) SetDeviceTypeFunc(fn func() string) {
	type deviceTypeConfigurable interface {
		SetDeviceTypeFunc(func() string)
	}
	if tool, ok := t.inner.(deviceTypeConfigurable); ok {
		tool.SetDeviceTypeFunc(fn)
	}
}
