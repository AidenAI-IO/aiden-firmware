package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	postActionScreenshotDelay              = time.Second
	postActionTinyChangeRetryDiffThreshold = 0.05
)

type postActionScreenshotResult struct {
	screenshotResult
	ActionOutput     string   `json:"action_output,omitempty"`
	ActionRetryCount int      `json:"action_retry_count,omitempty"`
	ScreenStable     *bool    `json:"screen_stable,omitempty"`
	StableWaitMs     *int64   `json:"stable_wait_ms,omitempty"`
	ScreenChanged    *bool    `json:"screen_changed,omitempty"`
	LastDiff         *float64 `json:"last_diff,omitempty"`
}

type postActionScreenshotTool struct {
	inner                    langtools.Tool
	waitStable               langtools.Tool
	screenshot               langtools.Tool
	delay                    time.Duration
	waitInput                string
	retryTinyChangeOnce      bool
	retryTinyChangeThreshold float64
	retryMu                  sync.Mutex
	retryUsed                bool
}

type stableScreenWaiter interface {
	wait(context.Context, string) (waitStableScreenResult, error)
}

type postActionTinyChangeRetryable interface {
	ShouldRetryPostActionTinyChange(input string) bool
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

func newPostActionStableScreenshotToolWithTinyChangeRetry(inner langtools.Tool, waitStable langtools.Tool, screenshot langtools.Tool, delay time.Duration, defaults ScreenStableDefaults) langtools.Tool {
	tool := newPostActionStableScreenshotTool(inner, waitStable, screenshot, delay, defaults).(*postActionScreenshotTool)
	tool.retryTinyChangeOnce = true
	tool.retryTinyChangeThreshold = postActionTinyChangeRetryDiffThreshold
	return tool
}

func (t *postActionScreenshotTool) Name() string {
	return t.inner.Name()
}

func (t *postActionScreenshotTool) Description() string {
	if t.waitStable != nil {
		return t.inner.Description() + " On successful execution, waits for the screen to become stable (or until the configured timeout) and returns a post-action screenshot observation. screen_changed=false means no visible screen change was observed during the wait window; when the action was expected to change the UI, do not assume success and inspect the screenshot before answering or repeating. screen_stable=false means the screen was still changing (for example during video playback) but the screenshot was still captured."
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
	actionOutput, handledOutput, handled, err := t.callAction(ctx, input)
	if err != nil {
		return "", err
	}
	if handled {
		return handledOutput, nil
	}

	var waitResult waitStableScreenResult
	retryCount := 0
	if t.waitStable != nil {
		waitResult, handledOutput, handled, err = t.waitAfterAction(ctx, actionOutput)
		if err != nil {
			return "", err
		}
		if handled {
			return handledOutput, nil
		}

		if t.shouldRetryTinyChange(input, waitResult) {
			retryCount = 1
			handledOutput, handled, err = t.refreshMappingBeforeRetry(ctx, actionOutput)
			if err != nil {
				return "", err
			}
			if handled {
				return handledOutput, nil
			}
			actionOutput, handledOutput, handled, err = t.callAction(ctx, input)
			if err != nil {
				return "", err
			}
			if handled {
				return handledOutput, nil
			}
			waitResult, handledOutput, handled, err = t.waitAfterAction(ctx, actionOutput)
			if err != nil {
				return "", err
			}
			if handled {
				return handledOutput, nil
			}
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
		ActionRetryCount: retryCount,
	}
	if t.waitStable != nil {
		stable := waitResult.Stable
		elapsed := waitResult.ElapsedMs
		payload.ScreenStable = &stable
		payload.StableWaitMs = &elapsed
		payload.ScreenChanged = waitResult.ScreenChanged
		payload.LastDiff = waitResult.LastDiff
	}

	out, _ := json.Marshal(payload)
	return string(out), nil
}

func (t *postActionScreenshotTool) callAction(ctx context.Context, input string) (string, string, bool, error) {
	actionOutput, err := t.inner.Call(ctx, input)
	if err != nil {
		return "", "", false, err
	}
	if te := ToolErrorFromContext(ctx); te != nil {
		return "", toolErrorString(te), true, nil
	}
	if legacyToolOutputLooksLikeError(actionOutput) {
		trimmed := strings.TrimSpace(actionOutput)
		te := NewToolError(CodeToolExecutionFailed, strings.TrimSpace(trimmed[len("error:"):]))
		SetToolError(ctx, te)
		return "", toolErrorString(te), true, nil
	}
	return actionOutput, "", false, nil
}

func (t *postActionScreenshotTool) refreshMappingBeforeRetry(ctx context.Context, actionOutput string) (string, bool, error) {
	screenshotOutput, err := t.screenshot.Call(ctx, "{}")
	if err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but pre-retry screenshot failed: %v", t.inner.Name(), actionOutput, err), true, nil
	}
	if te := ToolErrorFromContext(ctx); te != nil {
		return wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but pre-retry screenshot failed: %s", t.inner.Name(), actionOutput, te.Message), true, nil
	}
	if legacyToolOutputLooksLikeError(screenshotOutput) {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but pre-retry screenshot failed: %s", t.inner.Name(), actionOutput, screenshotOutput), true, nil
	}
	var result screenshotResult
	if err := json.Unmarshal([]byte(screenshotOutput), &result); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but pre-retry screenshot was invalid: %v", t.inner.Name(), actionOutput, err), true, nil
	}
	return "", false, nil
}

func (t *postActionScreenshotTool) waitAfterAction(ctx context.Context, actionOutput string) (waitStableScreenResult, string, bool, error) {
	var waitResult waitStableScreenResult
	if waiter, ok := t.waitStable.(stableScreenWaiter); ok {
		var err error
		waitResult, err = waiter.wait(ctx, t.waitInput)
		if err != nil {
			return waitStableScreenResult{}, toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %v", t.inner.Name(), actionOutput, err), true, nil
		}
	} else {
		waitOutput, err := t.waitStable.Call(ctx, t.waitInput)
		if err != nil {
			return waitStableScreenResult{}, "", false, err
		}
		if te := ToolErrorFromContext(ctx); te != nil {
			return waitStableScreenResult{}, wrapPostActionSubtoolError(ctx, te, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, te.Message), true, nil
		}
		if legacyToolOutputLooksLikeError(waitOutput) {
			return waitStableScreenResult{}, toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), true, nil
		}
		if err := json.Unmarshal([]byte(waitOutput), &waitResult); err != nil {
			return waitStableScreenResult{}, toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait returned invalid JSON: %v", t.inner.Name(), actionOutput, err), true, nil
		}
		if !waitResult.OK {
			return waitStableScreenResult{}, toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed: %s", t.inner.Name(), actionOutput, waitOutput), true, nil
		}
	}
	if !waitResult.OK {
		return waitStableScreenResult{}, toolErrorResultf(ctx, CodeToolExecutionFailed, "%s completed with output %q, but stable-screen wait failed", t.inner.Name(), actionOutput), true, nil
	}
	return waitResult, "", false, nil
}

func (t *postActionScreenshotTool) shouldRetryTinyChange(input string, waitResult waitStableScreenResult) bool {
	if !t.retryTinyChangeOnce || !postActionWaitLooksLikeTinyChange(waitResult, t.retryTinyChangeThreshold) {
		return false
	}
	retryable, ok := t.inner.(postActionTinyChangeRetryable)
	if !ok || !retryable.ShouldRetryPostActionTinyChange(input) {
		return false
	}

	t.retryMu.Lock()
	defer t.retryMu.Unlock()
	if t.retryUsed {
		return false
	}
	t.retryUsed = true
	return true
}

func postActionWaitLooksLikeTinyChange(waitResult waitStableScreenResult, threshold float64) bool {
	if threshold <= 0 {
		threshold = postActionTinyChangeRetryDiffThreshold
	}
	if waitResult.LastDiff != nil {
		return *waitResult.LastDiff >= 0 && *waitResult.LastDiff < threshold
	}
	return waitResult.ScreenChanged != nil && !*waitResult.ScreenChanged
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

// SetPlatformFn delegates to wrapped text-entry tools if present.
// This allows runtime configuration of platform precedence (bridge > config > LLM).
func (t *postActionScreenshotTool) SetPlatformFn(fn func() string) {
	type platformConfigurable interface {
		SetPlatformFn(func() string)
	}
	if tool, ok := t.inner.(platformConfigurable); ok {
		tool.SetPlatformFn(fn)
	}
}
