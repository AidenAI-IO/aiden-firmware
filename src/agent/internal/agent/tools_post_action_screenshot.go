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
	ActionOutput string `json:"action_output,omitempty"`
}

type postActionScreenshotTool struct {
	inner      langtools.Tool
	waitStable langtools.Tool
	screenshot langtools.Tool
	delay      time.Duration
}

func newPostActionScreenshotTool(inner langtools.Tool, screenshot langtools.Tool, delay time.Duration) langtools.Tool {
	return newPostActionStableScreenshotTool(inner, nil, screenshot, delay)
}

func newPostActionStableScreenshotTool(inner langtools.Tool, waitStable langtools.Tool, screenshot langtools.Tool, delay time.Duration) langtools.Tool {
	return &postActionScreenshotTool{
		inner:      inner,
		waitStable: waitStable,
		screenshot: screenshot,
		delay:      delay,
	}
}

func (t *postActionScreenshotTool) Name() string {
	return t.inner.Name()
}

func (t *postActionScreenshotTool) Description() string {
	if t.waitStable != nil {
		return t.inner.Description() + " On successful execution, waits for the screen to become stable and returns a post-action screenshot observation."
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

	if t.waitStable != nil {
		waitOutput, err := t.waitStable.Call(ctx, `{"timeout_ms":3000,"stable_ms":500,"diff_threshold":5}`)
		if err != nil {
			return "", err
		}
		if toolOutputLooksLikeError(waitOutput) {
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

	out, _ := json.Marshal(postActionScreenshotResult{
		screenshotResult: result,
		ActionOutput:     actionOutput,
	})
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
