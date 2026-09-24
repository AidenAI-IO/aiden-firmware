package agent

import (
	"aiden-agent/internal/logging"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	deviceTypeFn         func() string
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
	App  string `json:"app"`
	Name string `json:"name"`
}

func (t *appSearchOpenTool) SetDeviceTypeFunc(fn func() string) {
	if t != nil {
		t.deviceTypeFn = fn
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
		"app":  stringArgSchema("App name to search for and open."),
		"name": stringArgSchema("Alias for app."),
	}, "app")
}

func (t *appSearchOpenTool) Call(ctx context.Context, input string) (string, error) {
	var controller *iosKeyboardIsolationController
	if t != nil {
		controller = t.iosKeyboardIsolation
	}
	started := time.Now()
	output, err := withIOSKeyboardIsolationBatchCall(ctx, controller, func(batchCtx context.Context) (string, error) {
		return t.call(batchCtx, input)
	})
	logging.Infof("agent", "app_search", "phase=returned duration_ms=%d error=%v output=%q", time.Since(started).Milliseconds(), err, truncateForLog(output, 4096))
	return output, err
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
	platform := ""
	if t.deviceTypeFn != nil {
		if derived := textInputPlatformFromDeviceType(t.deviceTypeFn()); derived != "" {
			platform = derived
		}
	}
	if platform == "" && t.iosKeyboardIsolation != nil {
		platform = "ios"
	}
	logging.Infof("agent", "app_search", "open start target=%q platform=%q", args.App, platform)
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
		logging.Infof("agent", "app_search", "open finished target=%q ok=false error=%q steps=%v vlm_calls=%d", args.App, err.Error(), result.Steps, result.VLMCalls)
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
	logging.Infof("agent", "app_search", "open finished target=%q ok=%t reason=%q steps=%v vlm_calls=%d", args.App, result.Opened, result.Reason, result.Steps, result.VLMCalls)
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
		out, err := callTextInputTool(ctx, cfg.hw.quickAction, jsonString(map[string]any{"action": action}))
		if err != nil {
			return err
		}
		return interpretTextInputToolOutput(out)
	}
	if err := callQuickAction("spotlight_search"); err != nil {
		logging.Infof("agent", "app_search", "phase=spotlight_search error=%q", err.Error())
		return result, err
	}
	logging.Infof("agent", "app_search", "phase=spotlight_search complete")
	steps = append(steps, "opened system search")
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	searchTerms := appSearchFallbackTerms(searchTerm)
	for index, term := range searchTerms {
		logging.Infof("agent", "app_search", "phase=query_start term=%q attempt=%d", term, index+1)
		localInput, err := enterSearchQueryWithRoute(ctx, cfg, term, index > 0)
		if err != nil {
			logging.Infof("agent", "app_search", "phase=query_entry term=%q route_local=%t error=%q", term, localInput, err.Error())
			var unverified *searchQueryUnverifiedError
			if !localInput || !errors.As(err, &unverified) || ctx.Err() != nil {
				result.Steps = append(steps, "search query entry failed")
				return result, err
			}
			// A failed text verification does not mean the app result is absent.
			// Keep the keyboard batch alive so pending composition can be handled
			// before a pointer action or the batch's deferred HID restoration.
			logging.Infof("agent", "app_search", "phase=query_recovery term=%q action=inspect_before_hid_restore", term)
			steps = append(steps, "search input unverified; inspecting query and app results")
		}
		if localInput && err != nil {
			// Restoring the normal iOS HID profile before the result tap can
			// dismiss uncommitted IME text. Finish the composition while
			// keyboard-only isolation is still active.
			if err := commitPendingSearchQuery(ctx, cfg, term); err != nil {
				logging.Infof("agent", "app_search", "phase=query_commit term=%q error=%q", term, err.Error())
				result.Steps = append(steps, "search query composition commit failed")
				return result, err
			}
			logging.Infof("agent", "app_search", "phase=query_commit term=%q complete", term)
		} else if localInput {
			// RunSegmented already verified that each IME part was committed
			// with no pending composition. No input or profile switch intervenes
			// here, so another vision request can only duplicate that decision.
			logging.Infof("agent", "app_search", "phase=query_commit term=%q source=enter_text confirmed=true", term)
		}
		logging.Infof("agent", "app_search", "phase=query_entry term=%q route_local=%t complete", term, localInput)
		steps = append(steps, fmt.Sprintf("searched %q", term))
		if err := sleep(ctx, appSearchResultSettleDelay); err != nil {
			result.Steps = append(steps, "wait for search results canceled")
			return result, err
		}
		steps = append(steps, "waited for search results to settle")
		foundForTerm := false
		for attempt := 1; attempt <= 2; attempt++ {
			logging.Infof("agent", "app_search", "phase=result_lookup term=%q attempt=%d", term, attempt)
			findResult, calls, err := findSearchOpenAppResult(ctx, cfg, engine, term)
			result.VLMCalls += calls
			if err != nil {
				logging.Infof("agent", "app_search", "phase=result_lookup term=%q attempt=%d error=%q", term, attempt, err.Error())
				result.Steps = append(steps, "locate app failed")
				return result, err
			}
			if !findResult.Found {
				logging.Infof("agent", "app_search", "phase=result_lookup term=%q attempt=%d found=false", term, attempt)
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
			logging.Infof("agent", "app_search", "phase=result_lookup term=%q attempt=%d found=true label=%q tap_point=%v", term, attempt, findResult.Label, findResult.TapPoint)
			if findResult.TapPoint == nil {
				result.Steps = append(steps, "app result is missing tap point")
				return result, fmt.Errorf("app search result is missing tap_point")
			}
			logging.Infof("agent", "app_search", "phase=result_tap term=%q point=(%.0f,%.0f) start", term, findResult.TapPoint.X, findResult.TapPoint.Y)
			if err := tapSearchOpenResult(ctx, cfg.hw, *findResult.TapPoint); err != nil {
				logging.Infof("agent", "app_search", "phase=result_tap term=%q point=(%.0f,%.0f) error=%q", term, findResult.TapPoint.X, findResult.TapPoint.Y, err.Error())
				result.Steps = append(steps, "tap app result failed")
				return result, err
			}
			logging.Infof("agent", "app_search", "phase=result_tap term=%q point=(%.0f,%.0f) complete", term, findResult.TapPoint.X, findResult.TapPoint.Y)
			if label := strings.TrimSpace(findResult.Label); label != "" {
				steps = append(steps, fmt.Sprintf("tapped app result %q", label))
			} else {
				steps = append(steps, "tapped app result")
			}
			logging.Infof("agent", "app_search", "phase=launch_wait term=%q delay_ms=%d", term, launchDelay.Milliseconds())
			if err := sleep(ctx, launchDelay); err != nil {
				logging.Infof("agent", "app_search", "phase=launch_wait term=%q error=%q", term, err.Error())
				result.Steps = append(steps, "app launch wait canceled")
				return result, err
			}
			opened, calls, err := confirmSearchOpenApp(ctx, cfg, engine, term)
			result.VLMCalls += calls
			if err != nil {
				logging.Infof("agent", "app_search", "phase=open_confirm term=%q opened=unknown error=%q", term, err.Error())
				result.Steps = append(steps, "confirm app open failed")
				return result, err
			}
			logging.Infof("agent", "app_search", "phase=open_confirm term=%q opened=%t reason=%q", term, opened.Opened, opened.Reason)
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
				logging.Infof("agent", "app_search", "phase=complete term=%q opened=true", term)
				return result, nil
			}
			result.Reason = strings.TrimSpace(opened.Reason)
			logging.Infof("agent", "app_search", "phase=retry term=%q attempt=%d remaining=%d reason=%q", term, attempt, 2-attempt, result.Reason)
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
	_, err := enterSearchQueryWithRoute(ctx, cfg, term, clearFirst)
	return err
}

type searchQueryUnverifiedError struct {
	suggestion string
}

func (e *searchQueryUnverifiedError) Error() string {
	return "enter search query: " + e.suggestion
}

func enterSearchQueryWithRoute(ctx context.Context, cfg appSearchOpenFlowConfig, term string, clearFirst bool) (localInput bool, err error) {
	if clearFirst {
		engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
		if err := engine.clearField(ctx, cfg.platform); err != nil {
			return false, err
		}
	}
	attempted, err := pasteSearchQuery(ctx, cfg, term)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if attempted && err == nil {
		logging.Infof("agent", "app_search", "query input route=pip_clipboard_paste")
		return false, nil
	}
	if attempted {
		logging.Infof("agent", "app_search", "query input route=local_hid clipboard_error=%v", err)
		// A failed paste may have inserted text. Clear it without Escape,
		// which could dismiss Spotlight, before the existing local input path.
		engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
		if err := engine.tapKeys(ctx, []string{"meta", "a"}); err != nil {
			return false, err
		}
		if err := engine.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return false, err
		}
		if err := engine.tapKeys(ctx, []string{"backspace"}); err != nil {
			return false, err
		}
	}

	input := map[string]any{
		"text":  term,
		"focus": map[string]any{"x": 500, "y": 120},
	}
	out, err := cfg.entryTool.enterTextInner(ctx, jsonString(input), enterTextOptions{disableBridge: true, replaceField: true})
	if err != nil {
		return true, err
	}
	var result enterTextToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return true, fmt.Errorf("parse search entry result: %w", err)
	}
	if !result.OK {
		return true, &searchQueryUnverifiedError{suggestion: strings.TrimSpace(result.Suggestion)}
	}
	return true, nil
}

// commitPendingSearchQuery recovers an unverified local entry while its keyboard
// batch is still isolated. A pointer action restores the normal HID profile and
// can clear pending IME composition, so recover it before looking up/tapping the
// result. Successful local entry already verifies commitment in RunSegmented.
func commitPendingSearchQuery(ctx context.Context, cfg appSearchOpenFlowConfig, term string) error {
	if cfg.platform != "ios" || cfg.entryTool == nil || cfg.entryTool.iosKeyboardIsolation == nil {
		logging.Infof("agent", "app_search", "phase=query_composition term=%q skipped=true platform=%q", term, cfg.platform)
		return nil
	}
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	args := textInputArgs{
		Text: term, CurrentIMEPart: term,
		Focus: focusPointArgs{X: 500, Y: 120},
	}
	// Always inspect after the last selection; sending Space alone is not proof
	// that the IME committed. Unknown observations must not authorize a restore.
	selectedText := ""
	for attempt := 0; attempt < 3; attempt++ {
		analysis, _, err := engine.analyzeScreen(ctx, cfg.platform, args, nil)
		if err != nil {
			return fmt.Errorf("inspect search query composition: %w", err)
		}
		logging.Infof("agent", "app_search", "phase=query_composition term=%q attempt=%d composition_pending=%t target_matched=%t field_text=%q", term, attempt+1, analysis.CompositionPending, analysis.TargetMatched, truncateForLog(analysis.FieldText, 256))
		if !analysis.CompositionPending && (analysis.TargetMatched || analysis.ObservedMode == textInputModeASCII || analysis.ObservedMode == textInputModeComposition) {
			return nil
		}
		if attempt == 2 {
			break
		}
		if analysis.CompositionPending {
			action, _, err := engine.decideCandidateAction(ctx, cfg.platform, args, nil, selectedText)
			if err != nil {
				return fmt.Errorf("inspect search query candidate: %w", err)
			}
			if action.Action == textInputCandidateActionSelect {
				logging.Infof("agent", "app_search", "phase=query_composition term=%q attempt=%d action=select_candidate offset=%d text=%q", term, attempt+1, action.Offset, action.Text)
				if err := engine.selectCandidateByKeyboard(ctx, action); err != nil {
					return fmt.Errorf("commit search query candidate: %w", err)
				}
				selectedText += action.Text
			}
		}
		if err := engine.sleepFor(ctx, textInputCandidateSettleDelay); err != nil {
			return err
		}
	}
	return fmt.Errorf("search query composition could not be confirmed before HID restore")
}

// PiP can write the clipboard without leaving the focused system search UI.
// The existing result lookup, tap and app-open confirmation verify the outcome.
func pasteSearchQuery(ctx context.Context, cfg appSearchOpenFlowConfig, term string) (attempted bool, err error) {
	platform := cfg.platform
	if platform == "" {
		platform = cfg.entryTool.platform()
	}
	if platform != "ios" || cfg.entryTool.bridgeTool == nil {
		return false, nil
	}
	bridgeTool := cfg.entryTool.bridgeTool
	bridge := bridgeTool.currentBridge()
	if bridge == nil || !phoneBridgeCanUsePiPBackground(bridge.getStatus(), "clipboard_write") {
		return false, nil
	}
	if err := bridgeTool.writeClipboard(ctx, bridge, term); err != nil {
		return true, err
	}
	if err := bridgeTool.sleepAfterClipboardWrite(ctx); err != nil {
		return true, err
	}
	_, _, err = bridgeTool.pasteClipboard(ctx, platform)
	return true, err
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
	logging.Infof("agent", "app_search", "phase=result_screenshot term=%q start", searchTerm)
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		logging.Infof("agent", "app_search", "phase=result_screenshot term=%q error=%q", searchTerm, err.Error())
		return bridgeSearchResult{}, 0, err
	}
	logging.Infof("agent", "app_search", "phase=result_screenshot term=%q complete width=%d height=%d backend=%q", searchTerm, shot.Width, shot.Height, shot.CaptureBackend)
	if cfg.findAppTapFn != nil {
		result, err := cfg.findAppTapFn(ctx, shot, searchTerm)
		if err != nil {
			logging.Infof("agent", "app_search", "phase=result_vision term=%q error=%q", searchTerm, err.Error())
		}
		return result, 0, err
	}
	modelVision, ok := cfg.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return bridgeSearchResult{}, 0, fmt.Errorf("app search vision is not configured")
	}
	return requestVisionDecision(ctx, modelVision, "app_search", buildAppSearchResultPrompt(searchTerm), parseAppSearchResult, shot)
}

func parseAppSearchResult(raw string) (bridgeSearchResult, error) {
	var parsed struct {
		Found    *bool  `json:"found"`
		Label    string `json:"label"`
		TapPoint *struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		} `json:"tap_point"`
	}
	// Model responses may include harmless format metadata. Validate the actual
	// decision and coordinates rather than treating metadata as a tool argument.
	if err := decodeVisionJSONObject(raw, &parsed, "found"); err != nil {
		return bridgeSearchResult{}, fmt.Errorf("parse app search result: %w", err)
	}
	if parsed.Found == nil {
		return bridgeSearchResult{}, fmt.Errorf("parse app search result: found is required")
	}
	result := bridgeSearchResult{Found: *parsed.Found, Label: parsed.Label}
	if result.Found {
		if parsed.TapPoint == nil || parsed.TapPoint.X == nil || parsed.TapPoint.Y == nil {
			return bridgeSearchResult{}, fmt.Errorf("parse app search result: found result is missing tap_point coordinates")
		}
		x, y := *parsed.TapPoint.X, *parsed.TapPoint.Y
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < 0 || x > 1000 || y < 0 || y > 1000 {
			return bridgeSearchResult{}, fmt.Errorf("parse app search result: tap_point is outside normalized bounds")
		}
		result.TapPoint = &focusPointArgs{X: x, Y: y}
	}
	return result, nil
}

func buildAppSearchResultPrompt(searchTerm string) string {
	return strings.TrimSpace(fmt.Sprintf(`Analyze this device screenshot of a system search results page.
Find the visible direct app-launch result for query %q.
Return JSON only:
{
  "found": true,
  "tap_point": {"x": 180, "y": 180},
  "label": "App"
}

Rules:
- A valid result must directly launch the requested app itself. On iOS this is commonly the standalone app icon/tile under a localized "Best Search Result" heading or an Apps section.
- Do not select a result from a localized Settings section, a result with a Settings gear badge, an app settings page, a localized "Search in App" action, a web suggestion, or content from another app even when it contains the exact query text or app icon.
- First discard every result that is not a direct app launch. If multiple valid direct app-launch results remain, scan from top to bottom and choose the topmost one.
- tap_point must be centered inside the actual app icon or its directly associated app-launch tile, using normalized 0-1000 coordinates. Do not use the center of the screen or a large container's empty area.
- Return found=true only when the direct app-launch result is clearly identifiable and tappable. If it cannot be distinguished from Settings or content results, return found=false.
- If not visible, return {"found": false, "tap_point": {"x": 0, "y": 0}}.`, searchTerm))
}

func confirmSearchOpenApp(ctx context.Context, cfg appSearchOpenFlowConfig, engine *textInputEngine, searchTerm string) (bridgeAppOpenResult, int, error) {
	logging.Infof("agent", "app_search", "phase=open_confirm_screenshot term=%q start", searchTerm)
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		logging.Infof("agent", "app_search", "phase=open_confirm_screenshot term=%q error=%q", searchTerm, err.Error())
		return bridgeAppOpenResult{}, 0, err
	}
	logging.Infof("agent", "app_search", "phase=open_confirm_screenshot term=%q complete width=%d height=%d backend=%q", searchTerm, shot.Width, shot.Height, shot.CaptureBackend)
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
	return requestVisionDecision(ctx, modelVision, "app_open_confirmation", prompt, parseAppOpenConfirmation, shot)
}

func parseAppOpenConfirmation(raw string) (bridgeAppOpenResult, error) {
	var parsed struct {
		Opened *bool  `json:"opened"`
		Reason string `json:"reason"`
	}
	if err := decodeVisionJSONObject(raw, &parsed, "opened"); err != nil {
		return bridgeAppOpenResult{}, fmt.Errorf("parse app open confirmation: %w", err)
	}
	if parsed.Opened == nil {
		return bridgeAppOpenResult{}, fmt.Errorf("parse app open confirmation: opened is required")
	}
	return bridgeAppOpenResult{Opened: *parsed.Opened, Reason: parsed.Reason}, nil
}

func tapSearchOpenResult(ctx context.Context, hw *textInputHardwareDeps, point focusPointArgs) error {
	out, err := hw.touchGesture.Call(ctx, jsonString(map[string]any{
		"type":  "tap",
		"point": map[string]any{"x": point.X, "y": point.Y},
	}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}
