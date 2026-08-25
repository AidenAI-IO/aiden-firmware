package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if result.GestureMarker != nil {
		compact["gesture_marker"] = result.GestureMarker
	}
	data, err := json.Marshal(compact)
	if err != nil {
		return content
	}
	return string(data)
}

type postActionScreenshotTool struct {
	inner      langtools.Tool
	waitStable langtools.Tool
	screenshot langtools.Tool
	delay      time.Duration
	waitInput  string
	defaults   ScreenStableDefaults
}

type stableScreenWaiter interface {
	wait(context.Context, string) (waitStableScreenResult, error)
}

func newPostActionScreenshotTool(inner langtools.Tool, screenshot langtools.Tool, delay time.Duration) langtools.Tool {
	return newPostActionStableScreenshotTool(inner, nil, screenshot, delay, ScreenStableDefaults{})
}

func newPostActionStableScreenshotTool(inner langtools.Tool, waitStable langtools.Tool, screenshot langtools.Tool, delay time.Duration, defaults ScreenStableDefaults) langtools.Tool {
	return &postActionScreenshotTool{
		inner:      inner,
		waitStable: waitStable,
		screenshot: screenshot,
		delay:      delay,
		waitInput:  defaults.InputJSON(),
		defaults:   defaults,
	}
}

func (t *postActionScreenshotTool) Name() string {
	return t.inner.Name()
}

func (t *postActionScreenshotTool) Description() string {
	markerDescription := ""
	if t.inner.Name() == "touch_gesture" {
		markerDescription = " For tap, double_tap, and long_press, the Agent-facing screenshot attachment is annotated with red and white concentric hollow rings centered at the requested coordinate. The raw JSON data remains the original unannotated JPEG for direct callers and image comparisons. The marker is requested-coordinate feedback, not independently measured physical touch feedback."
	}
	if t.waitStable != nil {
		return t.inner.Description() + " On successful execution, waits for the screen to become stable (or until the configured timeout) and returns a post-action screenshot observation. screen_changed=false means no visible screen change was observed during the wait window; when the action was expected to change the UI, do not assume success and inspect the screenshot before answering or repeating. screen_stable=false means the screen was still changing (for example during video playback) but the screenshot was still captured." + markerDescription
	}
	return fmt.Sprintf(
		"%s On successful execution, waits %s and returns a post-action screenshot observation.%s",
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
	if t.waitStable != nil {
		stable := waitResult.Stable
		elapsed := waitResult.ElapsedMs
		payload.ScreenStable = &stable
		payload.StableWaitMs = &elapsed
		payload.ScreenChanged = waitResult.ScreenChanged
		payload.LastDiff = waitResult.LastDiff
	}
	if t.inner.Name() == "touch_gesture" {
		if marker, ok := parseTouchGesturePostMarker(input); ok {
			payload.GestureMarker = &marker
		}
	}

	out, _ := json.Marshal(payload)
	return string(out), nil
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
	marker := touchGesturePostMarkerInfo{
		Type: strings.ToLower(strings.TrimSpace(args.Type)),
		X:    args.Point.X,
		Y:    args.Point.Y,
	}
	if !validTouchGesturePostMarker(marker) {
		return touchGesturePostMarkerInfo{}, false
	}
	return marker, true
}

func validTouchGesturePostMarker(marker touchGesturePostMarkerInfo) bool {
	switch marker.Type {
	case "tap", "double_tap", "long_press":
	default:
		return false
	}
	return finiteNormalizedCoordinate(marker.X) && finiteNormalizedCoordinate(marker.Y)
}

func finiteNormalizedCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1000
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
