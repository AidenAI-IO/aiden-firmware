package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func (t *postActionScreenshotTool) ArgsSchema() map[string]any {
	structured, ok := t.inner.(structuredInputTool)
	if !ok {
		return nil
	}
	return structured.ArgsSchema()
}

func (t *postActionScreenshotTool) Call(ctx context.Context, input string) (string, error) {
	actionOutput, err := t.inner.Call(ctx, input)
	if err != nil {
		return "", err
	}
	if te := ToolErrorFromContext(ctx); te != nil {
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
		if waiter, ok := t.waitStable.(stableScreenWaiter); ok {
			waitResult, err = waiter.wait(ctx, t.waitInput)
			if err != nil {
				return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %v", t.inner.Name(), actionOutput, err), nil
			}
		} else {
			waitOutput, err := t.waitStable.Call(ctx, t.waitInput)
			if err != nil {
				return "", err
			}
			if te := ToolErrorFromContext(ctx); te != nil {
				return wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, te.Message), nil
			}
			if legacyToolOutputLooksLikeError(waitOutput) {
				return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
			}
			if err := json.Unmarshal([]byte(waitOutput), &waitResult); err != nil {
				return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait returned invalid JSON: %v", t.inner.Name(), actionOutput, err), nil
			}
			if !waitResult.OK {
				return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), nil
			}
		}
		if !waitResult.OK {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed", t.inner.Name(), actionOutput), nil
		}
	} else if err := waitForPostActionScreenshot(ctx, t.delay); err != nil {
		return "", err
	}

	screenshotOutput, err := t.screenshot.Call(ctx, "{}")
	if err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but post-action screenshot failed: %v", t.inner.Name(), actionOutput, err), nil
	}
	if te := ToolErrorFromContext(ctx); te != nil {
		return wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but post-action screenshot failed: %s", t.inner.Name(), actionOutput, te.Message), nil
	}

	var result screenshotResult
	if err := json.Unmarshal([]byte(screenshotOutput), &result); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but post-action screenshot was invalid: %v", t.inner.Name(), actionOutput, err), nil
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

func wrapPostActionSubtoolError(ctx context.Context, te *ToolError, format string, args ...any) string {
	wrapped := NewToolErrorWithDetails(te.Code, fmt.Sprintf(format, args...), te.Details)
	SetToolError(ctx, wrapped)
	return toolErrorString(wrapped)
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

// SetPlatformFn delegates to the wrapped EnterTextInFieldTool if present.
// This allows runtime configuration of platform precedence (bridge > config > LLM).
func (t *postActionScreenshotTool) SetPlatformFn(fn func() string) {
	if tool, ok := t.inner.(*EnterTextInFieldTool); ok {
		tool.SetPlatformFn(fn)
	}
}
