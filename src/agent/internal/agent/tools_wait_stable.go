package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultStableWaitTimeoutMs = 2200
	defaultStableDurationMs    = 250
	defaultDiffThreshold       = 6.0
	stableWaitPollInterval     = 200 * time.Millisecond
)

type ScreenStableDefaults struct {
	TimeoutMs     int
	StableMs      int
	DiffThreshold float64
}

func (d ScreenStableDefaults) Resolved() ScreenStableDefaults {
	timeout := d.TimeoutMs
	if timeout <= 0 {
		timeout = defaultStableWaitTimeoutMs
	}
	stable := d.StableMs
	if stable <= 0 {
		stable = defaultStableDurationMs
	}
	if stable > timeout {
		stable = timeout
	}
	diffThreshold := d.DiffThreshold
	if diffThreshold <= 0 {
		diffThreshold = defaultDiffThreshold
	}
	return ScreenStableDefaults{
		TimeoutMs:     timeout,
		StableMs:      stable,
		DiffThreshold: diffThreshold,
	}
}

func (d ScreenStableDefaults) InputJSON() string {
	resolved := d.Resolved()
	return fmt.Sprintf(
		`{"timeout_ms":%d,"stable_ms":%d,"diff_threshold":%g}`,
		resolved.TimeoutMs,
		resolved.StableMs,
		resolved.DiffThreshold,
	)
}

type WaitStableScreenTool struct {
	client   waitStableFrameClient
	defaults ScreenStableDefaults
	screen   *screenState
}

type waitStableFrameClient interface {
	LatestFrame() (*frameMetadata, []byte, screenCaptureInfo, error)
	LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, screenCaptureInfo, error)
}

type waitStableScreenResult struct {
	OK            bool     `json:"ok"`
	Stable        bool     `json:"stable"`
	ElapsedMs     int64    `json:"elapsed_ms"`
	ScreenChanged *bool    `json:"screen_changed,omitempty"`
	LastDiff      *float64 `json:"last_diff,omitempty"`
}

type waitStableScreenObservationResult struct {
	screenshotResult
	OK            bool     `json:"ok"`
	Stable        bool     `json:"stable"`
	ElapsedMs     int64    `json:"elapsed_ms"`
	ScreenStable  *bool    `json:"screen_stable,omitempty"`
	StableWaitMs  *int64   `json:"stable_wait_ms,omitempty"`
	ScreenChanged *bool    `json:"screen_changed,omitempty"`
	LastDiff      *float64 `json:"last_diff,omitempty"`
}

func NewWaitStableScreenTool(socketPath string, defaults ScreenStableDefaults, screens ...*screenState) *WaitStableScreenTool {
	var screen *screenState
	if len(screens) > 0 {
		screen = screens[0]
	}
	return &WaitStableScreenTool{
		client:   NewScreenCaptureClient(socketPath),
		defaults: defaults,
		screen:   screen,
	}
}

func (t *WaitStableScreenTool) Name() string { return "wait_for_stable_screen" }

func (t *WaitStableScreenTool) ReturnsVisualObservation() bool { return true }

func (t *WaitStableScreenTool) Description() string {
	resolved := t.defaults.Resolved()
	return fmt.Sprintf(
		`Wait until the connected display stops changing before judging UI result. `+
			`Use only while operating a visible target UI, after a UI action or known UI transition that may animate, navigate, or load; do not call for text-only reasoning, arithmetic, comparison, or memory lookup. `+
			`Input JSON: {"timeout_ms":%d,"stable_ms":%d,"diff_threshold":%g}. `+
			`Omitted fields use agent config defaults. `+
			`The screen is stable when consecutive frames stay below diff_threshold for stable_ms. `+
			`Returns {"ok":true,"stable":true/false,"elapsed_ms":N,"screen_changed":true/false,...} plus a screenshot observation with width, height, format, size, and base64 JPEG data. `+
			`screen_changed=false means no visible frame change was observed during the wait window. `+
			`stable=false means the wait timed out while the screen was still changing (for example video playback); that is not an error and the screenshot is still captured as a best-effort observation.`,
		resolved.TimeoutMs,
		resolved.StableMs,
		resolved.DiffThreshold,
	)
}

func (t *WaitStableScreenTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"timeout_ms":     minIntegerArgSchema("Maximum time to wait for stability in milliseconds.", 1),
		"stable_ms":      minIntegerArgSchema("Required continuous stable duration in milliseconds.", 1),
		"diff_threshold": numberArgSchema("Maximum frame difference considered stable."),
	})
}

func (t *WaitStableScreenTool) Call(ctx context.Context, input string) (string, error) {
	result, err := t.wait(ctx, input)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
	}
	screenshot, err := t.captureScreenshot()
	if err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "stable-screen wait completed with stable=%v elapsed_ms=%d, but screenshot failed: %v", result.Stable, result.ElapsedMs, err), nil
	}
	stable := result.Stable
	elapsed := result.ElapsedMs
	out, _ := json.Marshal(waitStableScreenObservationResult{
		screenshotResult: screenshot,
		OK:               result.OK,
		Stable:           result.Stable,
		ElapsedMs:        result.ElapsedMs,
		ScreenStable:     &stable,
		StableWaitMs:     &elapsed,
		ScreenChanged:    result.ScreenChanged,
		LastDiff:         result.LastDiff,
	})
	return string(out), nil
}

func (t *WaitStableScreenTool) captureScreenshot() (screenshotResult, error) {
	meta, jpegData, captureInfo, err := t.client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		return screenshotResult{}, err
	}
	if meta.Stale {
		return screenshotResult{}, fmt.Errorf("frame service: STALE_FRAME")
	}
	if meta.PixelFormat != "jpeg" {
		return screenshotResult{}, fmt.Errorf("expected jpeg format, got %s", meta.PixelFormat)
	}
	active := screenActiveArea{}
	sourceWidth := int(meta.Width)
	sourceHeight := int(meta.Height)
	alreadyCropped := false
	if fullWidth, fullHeight, sourceActive, ok := frameMetadataSourceActiveArea(meta); ok {
		sourceWidth = fullWidth
		sourceHeight = fullHeight
		active = sourceActive
		alreadyCropped = true
	} else {
		active = detectScreenshotActiveAreaForScreen(t.screen, jpegData, int(meta.Width), int(meta.Height))
	}
	if t.screen != nil {
		t.screen.UpdateActiveArea(sourceWidth, sourceHeight, active)
	}
	displayWidth := int(meta.Width)
	displayHeight := int(meta.Height)
	displayData := jpegData
	if !alreadyCropped && active.Valid && (active.X != 0 || active.Y != 0 || active.Width != displayWidth || active.Height != displayHeight) {
		croppedData, croppedWidth, croppedHeight, err := cropJPEGToActiveArea(jpegData, active, screenshotJPEGQuality)
		if err != nil {
			return screenshotResult{}, fmt.Errorf("crop screenshot to active area: %w", err)
		}
		displayWidth = croppedWidth
		displayHeight = croppedHeight
		displayData = croppedData
	}
	result := screenshotResult{
		Width:  displayWidth,
		Height: displayHeight,
		Format: "jpeg",
		Size:   len(displayData),
		Data:   base64.StdEncoding.EncodeToString(displayData),
	}
	applyScreenCaptureInfo(&result, captureInfo)
	return result, nil
}

func (t *WaitStableScreenTool) wait(ctx context.Context, input string) (waitStableScreenResult, error) {
	var args struct {
		TimeoutMs     int     `json:"timeout_ms"`
		StableMs      int     `json:"stable_ms"`
		DiffThreshold float64 `json:"diff_threshold"`
	}
	if trimmed := strings.TrimSpace(input); trimmed != "" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return waitStableScreenResult{}, fmt.Errorf("invalid input: %w", err)
		}
	}

	resolvedDefaults := t.defaults.Resolved()
	timeout := time.Duration(resolvedDefaults.TimeoutMs) * time.Millisecond
	if args.TimeoutMs > 0 {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
	}
	stableFor := time.Duration(resolvedDefaults.StableMs) * time.Millisecond
	if args.StableMs > 0 {
		stableFor = time.Duration(args.StableMs) * time.Millisecond
	}
	if stableFor > timeout {
		stableFor = timeout
	}
	diffThreshold := args.DiffThreshold
	if diffThreshold <= 0 {
		diffThreshold = resolvedDefaults.DiffThreshold
	}

	start := time.Now()
	deadline := start.Add(timeout)

	prevMeta, prevFrame, _, err := t.client.LatestFrame()
	if err != nil {
		return waitStableScreenResult{}, err
	}
	prevRGB, err := convertFrameToRGB(prevMeta, prevFrame)
	if err != nil {
		return waitStableScreenResult{}, err
	}
	prevSeq := prevMeta.Seq
	stableSince := time.Now()
	screenChanged := false
	var lastDiff *float64

	for {
		now := time.Now()
		if now.Sub(stableSince) >= stableFor {
			return waitStableScreenResult{
				OK:            true,
				Stable:        true,
				ElapsedMs:     now.Sub(start).Milliseconds(),
				ScreenChanged: waitStableBoolPtr(screenChanged),
				LastDiff:      lastDiff,
			}, nil
		}
		if !now.Before(deadline) {
			stable := now.Sub(stableSince) >= stableFor
			return waitStableScreenResult{
				OK:            true,
				Stable:        stable,
				ElapsedMs:     now.Sub(start).Milliseconds(),
				ScreenChanged: waitStableBoolPtr(screenChanged),
				LastDiff:      lastDiff,
			}, nil
		}

		wait := stableWaitPollInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait > 0 {
			if err := sleepContext(ctx, wait); err != nil {
				return waitStableScreenResult{}, err
			}
		}

		meta, frame, _, err := t.client.LatestFrame()
		if err != nil {
			return waitStableScreenResult{}, err
		}
		if meta.Stale || meta.Seq <= prevSeq {
			continue
		}

		rgb, err := convertFrameToRGB(meta, frame)
		if err != nil {
			screenChanged = true
			prevSeq = meta.Seq
			stableSince = time.Now()
			continue
		}

		diff := meanRGBAbsDiff(prevMeta, prevRGB, meta, rgb)
		lastDiff = &diff
		if diff > 0 {
			screenChanged = true
		}
		if diff > diffThreshold {
			stableSince = time.Now()
		}
		prevMeta = meta
		prevRGB = rgb
		prevSeq = meta.Seq
	}
}

func waitStableBoolPtr(value bool) *bool {
	return &value
}

func meanRGBAbsDiff(aMeta *frameMetadata, a []byte, bMeta *frameMetadata, b []byte) float64 {
	if aMeta == nil || bMeta == nil || aMeta.Width != bMeta.Width || aMeta.Height != bMeta.Height || len(a) != len(b) || len(a) == 0 {
		return 256.0
	}

	var sum uint64
	for i := range a {
		if a[i] > b[i] {
			sum += uint64(a[i] - b[i])
		} else {
			sum += uint64(b[i] - a[i])
		}
	}
	return float64(sum) / float64(len(a))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
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
