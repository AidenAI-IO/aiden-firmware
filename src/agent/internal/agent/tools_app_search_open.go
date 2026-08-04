package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const appSearchOpenLaunchDelay = 1200 * time.Millisecond
const appSearchResultSettleDelay = 350 * time.Millisecond

func sleepWithContext(ctx context.Context, delay time.Duration) error {
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

type appSearchOpenTool struct {
	hw                   *textInputHardwareDeps
	vision               textInputVision
	platformFn           func() string
	findAppTapFn         func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn     func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	afterOpenFn          func() error
	searchTermFn         func(string) string
	entryTool            *EnterTextTool
	launchDelay          time.Duration
	sleep                func(context.Context, time.Duration) error
	iosKeyboardIsolation *iosKeyboardIsolationController
}

type appSearchOpenArgs struct {
	App      string `json:"app"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
}

func (t *appSearchOpenTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *appSearchOpenTool) Name() string { return toolSearchLaunchApp }

func (t *appSearchOpenTool) Description() string {
	return strings.TrimSpace(`Search for an app from the system search UI, tap the result, and confirm it opened. ` +
		`This is the visible-UI fallback used internally by open_app when Phone Bridge is unavailable. ` +
		`Input JSON: {"app":"WeChat"}. Returns ok:true when the target app is visibly opened. Observe the opened screen and complete any create/open/navigation step before calling a text-entry tool.`)
}

func (t *appSearchOpenTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"app":      stringArgSchema("App name to search for and open."),
		"name":     stringArgSchema("Alias for app."),
		"platform": stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
	}, "app")
}

func (t *appSearchOpenTool) Call(ctx context.Context, input string) (string, error) {
	var controller *iosKeyboardIsolationController
	if t != nil {
		controller = t.iosKeyboardIsolation
	}
	return withIOSKeyboardIsolationBatchCall(ctx, controller, func(batchCtx context.Context) (string, error) {
		return t.call(batchCtx, input)
	})
}

func (t *appSearchOpenTool) call(ctx context.Context, input string) (string, error) {
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
	if platform == "" && t.iosKeyboardIsolation != nil {
		platform = "ios"
	}
	result, err := runAppSearchOpenFlow(ctx, appSearchOpenFlowConfig{
		hw:               t.hw,
		vision:           t.vision,
		platform:         platform,
		searchTerm:       t.searchTerm(args.App),
		findAppTapFn:     t.findAppTapFn,
		confirmAppOpenFn: t.confirmAppOpenFn,
		afterOpenFn:      t.afterOpenFn,
		entryTool:        t.entryTool,
		launchDelay:      t.launchDelay,
		sleep:            t.sleep,
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
	entryTool        *EnterTextTool
	launchDelay      time.Duration
	sleep            func(context.Context, time.Duration) error
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
	if cfg.hw.quickAction == nil || cfg.hw.touchGesture == nil {
		return result, fmt.Errorf("app search open tools are not fully configured")
	}
	if cfg.entryTool == nil {
		return result, fmt.Errorf("app search entry tool is not configured")
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
	sleep := cfg.sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	if cfg.entryTool.engine != nil && cfg.sleep != nil && cfg.entryTool.engine.sleep == nil {
		cfg.entryTool.engine.sleep = cfg.sleep
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
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	searchTerms := appSearchFallbackTerms(searchTerm)
	for index, term := range searchTerms {
		if err := enterSearchQuery(ctx, cfg, term, index > 0); err != nil {
			result.Steps = append(steps, "search query entry failed")
			return result, err
		}
		steps = append(steps, fmt.Sprintf("searched %q", term))
		if err := sleep(ctx, appSearchResultSettleDelay); err != nil {
			result.Steps = append(steps, "wait for search results canceled")
			return result, err
		}
		steps = append(steps, "waited for search results to settle")
		foundForTerm := false
		for attempt := 1; attempt <= 2; attempt++ {
			findResult, calls, err := findSearchOpenAppResult(ctx, cfg, engine, term)
			result.VLMCalls += calls
			if err != nil {
				result.Steps = append(steps, "locate app failed")
				return result, err
			}
			if !findResult.Found {
				if attempt < 2 {
					steps = append(steps, fmt.Sprintf("app result not found for %q; rechecking", term))
					if err := sleep(ctx, 350*time.Millisecond); err != nil {
						result.Steps = append(steps, "app result recheck wait canceled")
						return result, err
					}
					continue
				}
				steps = append(steps, fmt.Sprintf("app result not found for %q", term))
				break
			}
			foundForTerm = true
			if err := tapSearchOpenResult(ctx, cfg.hw, findResult.TapPoint); err != nil {
				result.Steps = append(steps, "tap app result failed")
				return result, err
			}
			if label := strings.TrimSpace(findResult.Label); label != "" {
				steps = append(steps, fmt.Sprintf("tapped app result %q", label))
			} else {
				steps = append(steps, "tapped app result")
			}
			if err := sleep(ctx, launchDelay); err != nil {
				result.Steps = append(steps, "app launch wait canceled")
				return result, err
			}
			opened, calls, err := confirmSearchOpenApp(ctx, cfg, engine, term)
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
		}
		if foundForTerm {
			break
		}
	}
	result.Steps = steps
	if result.Reason == "" {
		result.Reason = "app did not open"
	}
	return result, nil
}

func enterSearchQuery(ctx context.Context, cfg appSearchOpenFlowConfig, term string, clearFirst bool) error {
	if clearFirst {
		engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
		if err := engine.clearField(ctx, cfg.platform); err != nil {
			return err
		}
	}
	input := map[string]any{
		"text":  term,
		"focus": map[string]any{"x": 500, "y": 120, "coord_space": "normalized"},
	}
	out, err := cfg.entryTool.Call(ctx, jsonString(input))
	if err != nil {
		return err
	}
	var result enterTextToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("parse search entry result: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("enter search query: %s", strings.TrimSpace(result.Suggestion))
	}
	return nil
}

func appSearchFallbackTerms(searchTerm string) []string {
	base := strings.TrimSpace(searchTerm)
	if base == "" {
		return nil
	}
	terms := []string{base}
	parts := strings.Fields(base)
	if len(parts) > 1 {
		terms = append(terms, parts[0])
	}
	runes := []rune(base)
	if len(runes) > 4 {
		terms = append(terms, string(runes[:4]))
	}
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		unique = append(unique, term)
	}
	sort.SliceStable(unique, func(i, j int) bool { return len([]rune(unique[i])) > len([]rune(unique[j])) })
	return unique
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
	raw, err := modelVision.visionJSON(ctx, "app_search", prompt, shot)
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
	raw, err := modelVision.visionJSON(ctx, "app_open_confirmation", prompt, shot)
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
