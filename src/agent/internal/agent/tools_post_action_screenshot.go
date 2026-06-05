package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const postActionScreenshotDelay = time.Second

type postActionScreenshotResult struct {
	screenshotResult
	ActionOutput string   `json:"action_output,omitempty"`
	ScreenStable *bool    `json:"screen_stable,omitempty"`
	StableWaitMs *int64   `json:"stable_wait_ms,omitempty"`
	LastDiff     *float64 `json:"last_diff,omitempty"`
}

type postActionScreenshotTool struct {
	inner      langtools.Tool
	waitStable langtools.Tool
	screenshot langtools.Tool
	delay      time.Duration
	waitInput  string
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
	}
}

func (t *postActionScreenshotTool) Name() string {
	return t.inner.Name()
}

func (t *postActionScreenshotTool) Description() string {
	if t.waitStable != nil {
		return t.inner.Description() + " On successful execution, waits for the screen to become stable (or until the configured timeout) and returns a post-action screenshot observation. screen_stable=false means the screen was still changing (for example during video playback) but the screenshot was still captured."
	}
	return fmt.Sprintf(
		"%s On successful execution, waits %s and returns a post-action screenshot observation.",
		t.inner.Description(),
		t.delay,
	)
}

func (t *postActionScreenshotTool) ReturnsVisualObservation() bool {
	return true
}

func (t *postActionScreenshotTool) Call(ctx context.Context, input string) (string, error) {
	actionOutput, err := t.inner.Call(ctx, input)
	if err != nil {
		return "", err
	}
	if toolOutputLooksLikeError(actionOutput) {
		return actionOutput, nil
	}

	var waitResult waitStableScreenResult
	if t.waitStable != nil {
		waitOutput, err := t.waitStable.Call(ctx, t.waitInput)
		if err != nil {
			return "", err
		}
		if toolOutputLooksLikeError(waitOutput) {
			return fmt.Sprintf("error: %s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
		}
		if err := json.Unmarshal([]byte(waitOutput), &waitResult); err != nil {
			return fmt.Sprintf("error: %s completed with output %q, but stable-screen wait returned invalid JSON: %v", t.inner.Name(), actionOutput, err), nil
		}
		if !waitResult.OK {
			return fmt.Sprintf("error: %s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
		}
	} else if err := waitForPostActionScreenshot(ctx, t.delay); err != nil {
		return "", err
	}

	screenshotOutput, err := t.screenshot.Call(ctx, "{}")
	if err != nil {
		return fmt.Sprintf("error: %s completed with output %q, but post-action screenshot failed: %v", t.inner.Name(), actionOutput, err), nil
	}

	var result screenshotResult
	if err := json.Unmarshal([]byte(screenshotOutput), &result); err != nil {
		return fmt.Sprintf("error: %s completed with output %q, but post-action screenshot was invalid: %v", t.inner.Name(), actionOutput, err), nil
	}

	payload := postActionScreenshotResult{
		screenshotResult: result,
		ActionOutput:     actionOutput,
	}
	if t.waitStable != nil {
		stable := waitResult.Stable
		elapsed := waitResult.ElapsedMs
		payload.ScreenStable = &stable
		payload.StableWaitMs = &elapsed
		payload.LastDiff = waitResult.LastDiff
	}

	out, _ := json.Marshal(payload)
	return string(out), nil
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
