package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultStableWaitTimeoutMs = 3500
	defaultStableDurationMs    = 500
	defaultDiffThreshold       = 2.0
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
	client   *FrameServiceClient
	defaults ScreenStableDefaults
}

type waitStableScreenResult struct {
	OK        bool     `json:"ok"`
	Stable    bool     `json:"stable"`
	ElapsedMs int64    `json:"elapsed_ms"`
	LastDiff  *float64 `json:"last_diff,omitempty"`
}

func NewWaitStableScreenTool(socketPath string, defaults ScreenStableDefaults) *WaitStableScreenTool {
	return &WaitStableScreenTool{
		client:   NewFrameServiceClient(socketPath),
		defaults: defaults,
	}
}

func (t *WaitStableScreenTool) Name() string { return "wait_for_stable_screen" }

func (t *WaitStableScreenTool) Description() string {
	resolved := t.defaults.Resolved()
	return fmt.Sprintf(
		`Wait until the connected display stops changing before taking a screenshot or judging UI result. `+
			`Input JSON: {"timeout_ms":%d,"stable_ms":%d,"diff_threshold":%g}. `+
			`Omitted fields use agent config defaults. `+
			`The screen is stable when consecutive frames stay below diff_threshold for stable_ms. `+
			`Returns {"ok":true,"stable":true/false,"elapsed_ms":N}. `+
			`stable=false means the wait timed out while the screen was still changing (for example video playback); that is not an error and you may continue operating.`,
		resolved.TimeoutMs,
		resolved.StableMs,
		resolved.DiffThreshold,
	)
}

func (t *WaitStableScreenTool) Call(ctx context.Context, input string) (string, error) {
	result, err := t.wait(ctx, input)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	out, _ := json.Marshal(result)
	return string(out), nil
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

	prevMeta, prevFrame, err := t.client.LatestFrame()
	if err != nil {
		return waitStableScreenResult{}, err
	}
	prevRGB, err := convertFrameToRGB(prevMeta, prevFrame)
	if err != nil {
		return waitStableScreenResult{}, err
	}
	prevSeq := prevMeta.Seq
	stableSince := time.Now()
	var lastDiff *float64

	for {
		now := time.Now()
		if now.Sub(stableSince) >= stableFor {
			return waitStableScreenResult{OK: true, Stable: true, ElapsedMs: now.Sub(start).Milliseconds(), LastDiff: lastDiff}, nil
		}
		if !now.Before(deadline) {
			stable := now.Sub(stableSince) >= stableFor
			return waitStableScreenResult{OK: true, Stable: stable, ElapsedMs: now.Sub(start).Milliseconds(), LastDiff: lastDiff}, nil
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

		meta, frame, err := t.client.LatestFrame()
		if err != nil {
			return waitStableScreenResult{}, err
		}
		if meta.Stale || meta.Seq <= prevSeq {
			continue
		}

		rgb, err := convertFrameToRGB(meta, frame)
		if err != nil {
			prevSeq = meta.Seq
			stableSince = time.Now()
			continue
		}

		diff := meanRGBAbsDiff(prevMeta, prevRGB, meta, rgb)
		lastDiff = &diff
		if diff > diffThreshold {
			stableSince = time.Now()
		}
		prevMeta = meta
		prevRGB = rgb
		prevSeq = meta.Seq
	}
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
