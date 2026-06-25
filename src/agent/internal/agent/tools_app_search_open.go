package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const appSearchOpenLaunchDelay = 1200 * time.Millisecond

type appSearchOpenTool struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	platformFn       func() string
	findAppTapFn     func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	afterOpenFn      func() error
	searchTermFn     func(string) string
	launchDelay      time.Duration
}

type appSearchOpenArgs struct {
	App      string `json:"app"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
}

func (t *appSearchOpenTool) Name() string { return "search_launch_app" }

func (t *appSearchOpenTool) Description() string {
	return strings.TrimSpace(`Search for an app from the system search UI, tap the result, and confirm it opened. ` +
		`Use this when the fastest path is visible app search instead of bridge-based direct launch. ` +
		`Input JSON: {"app":"WeChat"}. Returns ok:true only when the target app is visibly opened.`)
}

func (t *appSearchOpenTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"app":      stringArgSchema("App name to search for and open."),
		"name":     stringArgSchema("Alias for app."),
		"platform": stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
	}, "app")
}

func (t *appSearchOpenTool) Call(ctx context.Context, input string) (string, error) {
	if t == nil || t.hw == nil || t.vision == nil {
		return "error: search_launch_app is not fully configured", nil
	}
	var args appSearchOpenArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if strings.TrimSpace(args.App) == "" && strings.TrimSpace(args.Name) != "" {
		args.App = args.Name
	}
	args.App = strings.TrimSpace(args.App)
	if args.App == "" {
		return jsonString(map[string]any{"ok": false, "error": "app is required"}), nil
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if t.platformFn != nil {
		if override := strings.ToLower(strings.TrimSpace(t.platformFn())); override != "" {
			platform = override
		}
	}
	result, err := runAppSearchOpenFlow(ctx, appSearchOpenFlowConfig{
		hw:               t.hw,
		vision:           t.vision,
		platform:         platform,
		searchTerm:       t.searchTerm(args.App),
		findAppTapFn:     t.findAppTapFn,
		confirmAppOpenFn: t.confirmAppOpenFn,
		afterOpenFn:      t.afterOpenFn,
		launchDelay:      t.launchDelay,
	})
	if err != nil {
		return jsonString(map[string]any{"ok": false, "error": err.Error(), "target": args.App, "steps": result.Steps, "vlm_calls": result.VLMCalls}), nil
	}
	output := map[string]any{
		"ok":        result.Opened,
		"target":    args.App,
		"steps":     result.Steps,
		"vlm_calls": result.VLMCalls,
	}
	if result.Reason != "" {
		output["reason"] = result.Reason
	}
	if !result.Opened {
		output["ok"] = false
	}
	return jsonString(output), nil
}

func (t *appSearchOpenTool) searchTerm(app string) string {
	if t != nil && t.searchTermFn != nil {
		if term := strings.TrimSpace(t.searchTermFn(app)); term != "" {
			return term
		}
	}
	return app
}

type appSearchOpenFlowConfig struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	platform         string
	searchTerm       string
	findAppTapFn     func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	afterOpenFn      func() error
	launchDelay      time.Duration
}

type appSearchOpenFlowResult struct {
	Opened   bool
	Reason   string
	VLMCalls int
	Steps    []string
}

func runAppSearchOpenFlow(ctx context.Context, cfg appSearchOpenFlowConfig) (appSearchOpenFlowResult, error) {
	result := appSearchOpenFlowResult{}
	if cfg.hw == nil || cfg.vision == nil {
		return result, fmt.Errorf("app search open flow is not fully configured")
	}
	if cfg.hw.quickAction == nil || cfg.hw.keyboardText == nil || cfg.hw.touchGesture == nil {
		return result, fmt.Errorf("app search open tools are not fully configured")
	}
	searchTerm := strings.TrimSpace(cfg.searchTerm)
	if searchTerm == "" {
		return result, fmt.Errorf("search term is required")
	}
	platform := strings.ToLower(strings.TrimSpace(cfg.platform))
	if platform == "" {
		platform = "android"
	}
	launchDelay := cfg.launchDelay
	if launchDelay <= 0 {
		launchDelay = appSearchOpenLaunchDelay
	}
	steps := make([]string, 0, 12)
	callQuickAction := func(action string) error {
		out, err := cfg.hw.quickAction.Call(ctx, jsonString(map[string]any{"action": action, "platform": platform}))
		if err != nil {
			return err
		}
		return interpretTextInputToolOutput(out)
	}
	if err := callQuickAction("spotlight_search"); err != nil {
		return result, err
	}
	steps = append(steps, "opened system search")
	out, err := cfg.hw.keyboardText.Call(ctx, jsonString(map[string]string{"text": searchTerm}))
	if err != nil {
		return result, err
	}
	if err := interpretTextInputToolOutput(out); err != nil {
		return result, err
	}
	steps = append(steps, fmt.Sprintf("searched %q", searchTerm))
	engine := newTextInputEngine(*cfg.hw, cfg.vision)
	for attempt := 1; attempt <= 2; attempt++ {
		findResult, calls, err := findSearchOpenAppResult(ctx, cfg, engine, searchTerm)
		result.VLMCalls += calls
		if err != nil {
			result.Steps = append(steps, "locate app failed")
			return result, err
		}
		if !findResult.Found {
			steps = append(steps, "app result not found")
			continue
		}
		if err := tapSearchOpenResult(ctx, cfg.hw, findResult.TapPoint); err != nil {
			result.Steps = append(steps, "tap app result failed")
			return result, err
		}
		if label := strings.TrimSpace(findResult.Label); label != "" {
			steps = append(steps, fmt.Sprintf("tapped app result %q", label))
		} else {
			steps = append(steps, "tapped app result")
		}
		time.Sleep(launchDelay)
		opened, calls, err := confirmSearchOpenApp(ctx, cfg, engine, searchTerm)
		result.VLMCalls += calls
		if err != nil {
			result.Steps = append(steps, "confirm app open failed")
			return result, err
		}
		if opened.Opened {
			steps = append(steps, "app open confirmed")
			if cfg.afterOpenFn != nil {
				if err := cfg.afterOpenFn(); err != nil {
					result.Steps = append(steps, "after-open hook failed")
					return result, err
				}
			}
			result.Opened = true
			result.Reason = strings.TrimSpace(opened.Reason)
			result.Steps = steps
			return result, nil
		}
		result.Reason = strings.TrimSpace(opened.Reason)
		steps = append(steps, "app did not open; retrying")
		_ = attempt
	}
	result.Steps = steps
	if result.Reason == "" {
		result.Reason = "app did not open"
	}
	return result, nil
}

func findSearchOpenAppResult(ctx context.Context, cfg appSearchOpenFlowConfig, engine *textInputEngine, searchTerm string) (bridgeSearchResult, int, error) {
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return bridgeSearchResult{}, 0, err
	}
	if cfg.findAppTapFn != nil {
		result, err := cfg.findAppTapFn(ctx, shot, searchTerm)
		if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
			result.TapPoint.CoordSpace = "normalized"
		}
		return result, 0, err
	}
	modelVision, ok := cfg.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return bridgeSearchResult{}, 0, fmt.Errorf("app search vision is not configured")
	}
	prompt := strings.TrimSpace(fmt.Sprintf(`Analyze this device screenshot of a system search results page.
Find the visible search result row that launches the requested app for query %q.
Return JSON only:
{
  "found": true,
  "tap_point": {"x": 500, "y": 220, "coord_space": "normalized"},
  "label": "App"
}

Rules:
- Return found=true only when the app result is clearly visible and tappable.
- tap_point must be inside the visible app result row, using normalized 0-1000 coordinates.
- Prefer the actual app result row, not the keyboard, search field, or suggestion chip.
- If not visible, return {"found": false, "tap_point": {"x": 0, "y": 0, "coord_space": "normalized"}}.`, searchTerm))
	raw, err := modelVision.visionJSON(ctx, prompt, shot)
	if err != nil {
		return bridgeSearchResult{}, 1, err
	}
	var result bridgeSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return bridgeSearchResult{}, 1, fmt.Errorf("parse app search result: %w", err)
	}
	if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
		result.TapPoint.CoordSpace = "normalized"
	}
	return result, 1, nil
}

func confirmSearchOpenApp(ctx context.Context, cfg appSearchOpenFlowConfig, engine *textInputEngine, searchTerm string) (bridgeAppOpenResult, int, error) {
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return bridgeAppOpenResult{}, 0, err
	}
	if cfg.confirmAppOpenFn != nil {
		result, err := cfg.confirmAppOpenFn(ctx, shot, searchTerm)
		return result, 0, err
	}
	modelVision, ok := cfg.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return bridgeAppOpenResult{}, 0, fmt.Errorf("app open confirmation vision is not configured")
	}
	prompt := strings.TrimSpace(fmt.Sprintf(`Analyze this device screenshot immediately after tapping the app search result for query %q.
Decide whether the screen has opened the requested app instead of remaining on the search results page.
Return JSON only:
{
  "opened": true,
  "reason": "app screen is visible"
}

Rules:
- opened=true only when the screenshot clearly shows the target app screen or a loading transition into that app.
- opened=false if the screenshot still looks like the system search page, launcher, keyboard search results, or any unrelated app.
- Keep reason short and concrete.`, searchTerm))
	raw, err := modelVision.visionJSON(ctx, prompt, shot)
	if err != nil {
		return bridgeAppOpenResult{}, 1, err
	}
	var result bridgeAppOpenResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return bridgeAppOpenResult{}, 1, fmt.Errorf("parse app open confirmation: %w", err)
	}
	return result, 1, nil
}

func tapSearchOpenResult(ctx context.Context, hw *textInputHardwareDeps, point focusPointArgs) error {
	out, err := hw.touchGesture.Call(ctx, jsonString(map[string]any{
		"type":        "tap",
		"point":       map[string]any{"x": point.X, "y": point.Y},
		"coord_space": firstNonEmptyString([]string{strings.TrimSpace(point.CoordSpace), "normalized"}),
	}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}
