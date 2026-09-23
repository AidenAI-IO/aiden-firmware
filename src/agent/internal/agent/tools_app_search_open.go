package agent

import (
	"aiden-agent/internal/logging"
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
	platform := ""
	if t.deviceTypeFn != nil {
		if derived := textInputPlatformFromDeviceType(t.deviceTypeFn()); derived != "" {
			platform = derived
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
		return result, err
	}
	steps = append(steps, "opened system search")
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	searchTerms := appSearchFallbackTerms(searchTerm)
	for index, term := range searchTerms {
		entryRoute, err := enterSearchQueryWithRoute(ctx, cfg, term, index > 0)
		if err != nil {
			result.Steps = append(steps, "search query entry failed")
			return result, err
		}
		logging.Infof("agent", "app_search", "search query entered route=%s term=%q", entryRoute, term)
		steps = append(steps, fmt.Sprintf("searched %q", term))
		if err := sleep(ctx, appSearchResultSettleDelay); err != nil {
			result.Steps = append(steps, "wait for search results canceled")
			return result, err
		}
		steps = append(steps, "waited for search results to settle")
		findAndOpen := func() (bool, error) {
			for attempt := 1; attempt <= 2; attempt++ {
				findResult, calls, err := findSearchOpenAppResult(ctx, cfg, engine, term)
				result.VLMCalls += calls
				if err != nil {
					logging.Infof("agent", "app_search", "app result lookup failed term=%q attempt=%d error=%v", term, attempt, err)
					return false, err
				}
				logging.Infof("agent", "app_search", "app result lookup term=%q attempt=%d found=%t label=%q tap_point=%v", term, attempt, findResult.Found, findResult.Label, findResult.TapPoint)
				if !findResult.Found {
					if attempt < 2 {
						steps = append(steps, fmt.Sprintf("app result not found for %q; rechecking", term))
						if err := sleep(ctx, 350*time.Millisecond); err != nil {
							result.Steps = append(steps, "app result recheck wait canceled")
							return false, err
						}
						continue
					}
					steps = append(steps, fmt.Sprintf("app result not found for %q", term))
					return false, nil
				}
				if findResult.TapPoint == nil {
					result.Steps = append(steps, "app result is missing tap point")
					return false, fmt.Errorf("app search result is missing tap_point")
				}
				if err := tapSearchOpenResult(ctx, cfg.hw, *findResult.TapPoint); err != nil {
					logging.Infof("agent", "app_search", "app result tap failed term=%q label=%q point=%v error=%v", term, findResult.Label, *findResult.TapPoint, err)
					result.Steps = append(steps, "tap app result failed")
					return false, err
				}
				logging.Infof("agent", "app_search", "app result tapped term=%q label=%q point=%v", term, findResult.Label, *findResult.TapPoint)
				if label := strings.TrimSpace(findResult.Label); label != "" {
					steps = append(steps, fmt.Sprintf("tapped app result %q", label))
				} else {
					steps = append(steps, "tapped app result")
				}
				if err := sleep(ctx, launchDelay); err != nil {
					result.Steps = append(steps, "app launch wait canceled")
					return false, err
				}
				opened, calls, err := confirmSearchOpenApp(ctx, cfg, engine, term)
				result.VLMCalls += calls
				if err != nil {
					result.Steps = append(steps, "confirm app open failed")
					return false, err
				}
				logging.Infof("agent", "app_search", "app open confirmation term=%q opened=%t reason=%q", term, opened.Opened, opened.Reason)
				if opened.Opened {
					steps = append(steps, "app open confirmed")
					if cfg.afterOpenFn != nil {
						if err := cfg.afterOpenFn(); err != nil {
							result.Steps = append(steps, "after-open hook failed")
							return false, err
						}
					}
					result.Opened = true
					result.Reason = strings.TrimSpace(opened.Reason)
					return true, nil
				}
				result.Reason = strings.TrimSpace(opened.Reason)
				steps = append(steps, "app did not open; retrying")
			}
			return false, nil
		}
		foundForTerm, err := findAndOpen()
		if err != nil {
			result.Steps = append(steps, "locate app failed")
			return result, err
		}
		if foundForTerm {
			result.Steps = steps
			return result, nil
		}
		// A successful clipboard write/paste only proves that the system
		// accepted a paste. It does not prove that Spotlight committed the
		// requested app query: iOS may display a pinyin transcription or a
		// candidate chip, and the vision verifier can conservatively report a
		// mismatch. Give the target one controlled local retry before moving to
		// another search term. This is also the only place where the local
		// input-mode probe (and its temporary "a") is allowed after a paste.
		if entryRoute == "clipboard" {
			logging.Infof("agent", "app_search", "clipboard query did not surface target term=%q; retrying local HID input", term)
			steps = append(steps, "clipboard query did not surface app; retrying local input")
			if err := enterSearchQueryLocal(ctx, cfg, term, true); err != nil {
				result.Steps = append(steps, "local search query entry failed")
				return result, err
			}
			steps = append(steps, fmt.Sprintf("searched %q with local input", term))
			if err := sleep(ctx, appSearchResultSettleDelay); err != nil {
				result.Steps = append(steps, "wait for local search results canceled")
				return result, err
			}
			foundForTerm, err = findAndOpen()
			if err != nil {
				result.Steps = append(steps, "locate app after local retry failed")
				return result, err
			}
			if foundForTerm {
				result.Steps = steps
				return result, nil
			}
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

func enterSearchQueryWithRoute(ctx context.Context, cfg appSearchOpenFlowConfig, term string, clearFirst bool) (string, error) {
	if cfg.platform == "" {
		cfg.platform = cfg.entryTool.platform()
	}
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	if clearFirst {
		if err := clearSearchQuery(ctx, engine, cfg.platform); err != nil {
			return "", err
		}
	}
	clipboardResult := pasteSearchQuery(ctx, cfg, engine, term)
	if clipboardResult.Committed {
		logging.Infof("agent", "app_search", "search query input route=clipboard_paste verified=true")
		return "clipboard", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if clipboardResult.Pasted {
		// The paste action completed, but screenshot/OCR verification may be
		// inconclusive (for example, iOS renders 微信 as weixin). Keep the
		// query in place and let the app-result lookup validate it. If the
		// requested app is absent, runAppSearchOpenFlow will clear the field
		// and perform one explicit local retry.
		logging.Infof("agent", "app_search", "search query input route=clipboard_paste verification_deferred field=%q error=%v", clipboardResult.FieldText, clipboardResult.Err)
		return "clipboard", nil
	}
	if clipboardResult.Attempted {
		logging.Infof("agent", "app_search", "search query input route=local_hid reason=clipboard_failed error=%v", clipboardResult.Err)
		// The paste may have inserted partial or stale text. Replace it before
		// falling back, while the system search field still owns keyboard focus.
		if err := clearSearchQuery(ctx, engine, cfg.platform); err != nil {
			return "", err
		}
	} else {
		logging.Infof("agent", "app_search", "search query input route=local_hid reason=background_clipboard_unavailable")
	}
	input := map[string]any{
		"text":  term,
		"focus": map[string]any{"x": 500, "y": 120},
	}
	out, err := cfg.entryTool.enterTextInner(ctx, jsonString(input), true)
	if err != nil {
		return "", err
	}
	var result enterTextToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", fmt.Errorf("parse search entry result: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("enter search query: %s", strings.TrimSpace(result.Suggestion))
	}
	return "local_hid", nil
}

func enterSearchQueryLocal(ctx context.Context, cfg appSearchOpenFlowConfig, term string, clearFirst bool) error {
	if cfg.platform == "" {
		cfg.platform = cfg.entryTool.platform()
	}
	engine := newTextInputEngineWithSleep(*cfg.hw, cfg.vision, cfg.sleep)
	if clearFirst {
		if err := clearSearchQuery(ctx, engine, cfg.platform); err != nil {
			return err
		}
	}
	out, err := cfg.entryTool.enterTextInner(ctx, jsonString(map[string]any{
		"text":  term,
		"focus": map[string]any{"x": 500, "y": 120},
	}), true)
	if err != nil {
		return err
	}
	var result enterTextToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("parse local search entry result: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("local search query: %s", strings.TrimSpace(result.Suggestion))
	}
	return nil
}

func clearSearchQuery(ctx context.Context, engine *textInputEngine, platform string) error {
	keys, err := textInputKeyboardKeysForSelectAll(platform)
	if err != nil {
		return err
	}
	if err := engine.tapKeys(ctx, keys); err != nil {
		return err
	}
	if err := engine.sleepFor(ctx, textInputKeystrokeGap); err != nil {
		return err
	}
	return engine.tapKeys(ctx, []string{"backspace"})
}

// System search already owns keyboard focus. Only use clipboard transports
// that preserve this UI: restoring Aiden or tapping a guessed field position
// can dismiss Spotlight. Always write fresh text and verify the pasted query.
func pasteSearchQuery(ctx context.Context, cfg appSearchOpenFlowConfig, engine *textInputEngine, term string) textViaBridgeResult {
	if cfg.entryTool == nil || cfg.entryTool.bridgeTool == nil {
		return textViaBridgeResult{}
	}
	bridgeTool := cfg.entryTool.bridgeTool
	bridge := bridgeTool.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{}
	}
	platform := cfg.platform
	status := bridge.getStatus()
	switch platform {
	case "ios":
		if !phoneBridgeCanUsePiPBackground(status, "clipboard_write") {
			return textViaBridgeResult{}
		}
	case "android":
		if !phoneBridgeReadyForCommand(status, "clipboard_write") && !phoneBridgeCanUseFGSBackground(status, "clipboard_write") {
			return textViaBridgeResult{}
		}
	default:
		return textViaBridgeResult{}
	}
	result := textViaBridgeResult{Attempted: true}
	if result.Err = bridgeTool.writeClipboard(ctx, bridge, term); result.Err != nil {
		return result
	}
	if result.Err = bridgeTool.sleepAfterClipboardWrite(ctx); result.Err != nil {
		return result
	}
	if _, _, result.Err = bridgeTool.pasteClipboard(ctx, platform); result.Err != nil {
		return result
	}
	result.Pasted = true
	if result.Err = bridgeTool.sleepAfterPaste(ctx); result.Err != nil {
		return result
	}
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		result.Err = err
		return result
	}
	analysis, err := cfg.vision.AnalyzeScreen(ctx, shot, textInputScreenAnalysisRequest{
		Platform:            platform,
		TargetText:          term,
		FocusedSystemSearch: true,
	})
	result.Err = err
	if err == nil {
		result.FieldText = strings.TrimSpace(analysis.FieldText)
		result.Committed, _ = evaluateFieldCommit(analysis)
		// Spotlight may normalize a pasted CJK app name to pinyin (for
		// example, "微信" is displayed as "weixin") or keep the committed
		// text in a candidate chip. In either case the paste has reached the
		// focused search field even though target_matched is false. Treat a
		// non-empty field as a successful paste and let the app-result lookup
		// decide whether it is the requested app. Only an empty field should
		// trigger immediate local typing.
		if !result.Committed && result.FieldText != "" {
			result.Committed = true
			logging.Infof("agent", "app_search", "clipboard paste visible but vision target mismatch target=%q field=%q; defer validation to app result lookup", term, result.FieldText)
		}
		if !result.Committed {
			result.Err = fmt.Errorf("pasted search query did not appear in focused field")
		}
	}
	return result
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
		return result, 0, err
	}
	modelVision, ok := cfg.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return bridgeSearchResult{}, 0, fmt.Errorf("app search vision is not configured")
	}
	prompt := buildAppSearchResultPrompt(searchTerm)
	raw, err := modelVision.visionJSON(ctx, "app_search", prompt, shot)
	if err != nil {
		return bridgeSearchResult{}, 1, err
	}
	result, err := decodeAppSearchResult(raw)
	if err != nil {
		return bridgeSearchResult{}, 1, fmt.Errorf("parse app search result: %w", err)
	}
	if result.Found && result.TapPoint == nil {
		return bridgeSearchResult{}, 1, fmt.Errorf("parse app search result: found result is missing tap_point")
	}
	return result, 1, nil
}

// decodeAppSearchResult accepts the normal strict JSON response and also
// recovers a single balanced object when a vision provider surrounds JSON
// with prose or emits a second trailing object. A malformed provider response
// should cause one lookup retry, not skip the tap path entirely.
func decodeAppSearchResult(raw string) (bridgeSearchResult, error) {
	var result bridgeSearchResult
	if err := decodeStrictJSONObject(raw, &result); err == nil {
		return result, nil
	}
	object, ok := firstBalancedJSONObject(raw)
	if !ok {
		return bridgeSearchResult{}, fmt.Errorf("expected app search JSON object")
	}
	if err := json.Unmarshal([]byte(object), &result); err != nil {
		return bridgeSearchResult{}, err
	}
	return result, nil
}

func firstBalancedJSONObject(raw string) (string, bool) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(raw); index++ {
		char := raw[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : index+1], true
			}
		}
	}
	return "", false
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
		"type":  "tap",
		"point": map[string]any{"x": point.X, "y": point.Y},
	}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}
