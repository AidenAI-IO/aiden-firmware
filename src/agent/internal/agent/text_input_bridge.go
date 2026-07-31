package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	textViaBridgeConnectTimeout = 8 * time.Second
	textViaBridgePollInterval   = 200 * time.Millisecond
	textViaBridgePostWriteDelay = 2 * time.Second
	textViaBridgePostPasteDelay = 350 * time.Millisecond
	textViaBridgeMenuDelay      = 450 * time.Millisecond
	textViaBridgePasteAttempts  = 2
	textViaBridgeRecentsSwipes  = 3
	preparedClipboardMaxAge     = 5 * time.Minute
	textViaBridgeLongPressMS    = 900
)

type bridgeSearchResult struct {
	Found    bool           `json:"found"`
	TapPoint focusPointArgs `json:"tap_point"`
	Label    string         `json:"label,omitempty"`
}

type bridgeAppOpenResult struct {
	Opened bool   `json:"opened"`
	Reason string `json:"reason,omitempty"`
}

type previousAppCardResult struct {
	Found    bool           `json:"found"`
	TapPoint focusPointArgs `json:"tap_point"`
	Label    string         `json:"label,omitempty"`
}

type pasteMenuResult struct {
	Found    bool           `json:"found"`
	TapPoint focusPointArgs `json:"tap_point"`
	Label    string         `json:"label,omitempty"`
}

type textViaBridgeResult struct {
	Attempted bool
	Committed bool
	FieldText string
	Err       error
}

type textInputBridge struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	bridgeFn         func() *PhoneBridge
	clipboardWriteFn func(context.Context, *PhoneBridge, string) error
	findAppTapFn     func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	findPrevAppFn    func(context.Context, screenshotResult) (previousAppCardResult, error)
	findPasteMenuFn  func(context.Context, screenshotResult, string) (pasteMenuResult, error)
	sleep            func(context.Context, time.Duration) error
}

func (t *textInputBridge) runClipboardFirstResult(ctx context.Context, args textInputArgs) (textInputResult, bool) {
	platform := textInputHardwarePlatform(t.hw)
	if strings.TrimSpace(args.Text) == "" {
		return textInputResult{Reason: "text is required"}, false
	}
	bridgeResult := t.runClipboardFirstFlow(ctx, platform, args)
	if !bridgeResult.Attempted {
		if bridgeResult.Err != nil {
			return textViaBridgeResultToTextInputResult(bridgeResult), true
		}
		return textInputResult{Reason: "reliable bridge clipboard path unavailable"}, false
	}
	return textViaBridgeResultToTextInputResult(bridgeResult), true
}

func (t *textInputBridge) runClipboardFirstFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Err: fmt.Errorf("phone bridge is not configured")}
	}
	status := bridge.getStatus()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		if bridge.ClipboardRecentlyEquals(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		if phoneBridgeCanUsePiPBackground(status, "clipboard_write") {
			return t.runBackgroundClipboardQueueFlow(ctx, platform, args)
		}
		if phoneBridgeCanRestoreFromReturnEntry(status) || phoneBridgeReadyForCommand(status) {
			return t.runLegacyBridgeFlow(ctx, platform, args)
		}
		return textViaBridgeResult{}
	case "android":
		if bridge.ClipboardRecentlyEquals(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		return t.runTargetPreservingClipboardFlow(ctx, platform, args)
	default:
		return textViaBridgeResult{}
	}
}

func textViaBridgeResultToTextInputResult(bridgeResult textViaBridgeResult) textInputResult {
	result := textInputResult{}
	result.Committed = bridgeResult.Committed
	result.FieldText = bridgeResult.FieldText
	result.OK = bridgeResult.Committed
	if bridgeResult.Err != nil {
		result.Reason = bridgeResult.Err.Error()
		return result
	}
	if !bridgeResult.Committed {
		result.Reason = "bridge clipboard input not verified in field"
		return result
	}
	return result
}

func (t *textInputBridge) runAutomaticClipboardFirstFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Err: fmt.Errorf("phone bridge is not configured")}
	}
	status := bridge.getStatus()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		if bridge.ClipboardRecentlyEquals(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		if phoneBridgeCanUsePiPBackground(status, "clipboard_write") {
			return t.runBackgroundClipboardQueueFlow(ctx, platform, args)
		}
		return textViaBridgeResult{}
	case "android":
		if bridge.ClipboardRecentlyEquals(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		if !phoneBridgeReadyForCommand(status, "clipboard_write") &&
			!phoneBridgeCanUseFGSBackground(status, "clipboard_write") {
			return textViaBridgeResult{}
		}
		return t.runTargetPreservingClipboardFlow(ctx, platform, args)
	default:
		return textViaBridgeResult{}
	}
}

func (t *textInputBridge) runTargetPreservingClipboardFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Err: fmt.Errorf("phone bridge is not configured")}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	preserved, preserveErr := t.writeClipboardPreservingTarget(ctx, bridge, platform, args.Text)
	if preserveErr != nil {
		return textViaBridgeResult{Attempted: true, Err: preserveErr}
	}
	if !preserved {
		return textViaBridgeResult{}
	}
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	return result
}

func (t *textInputBridge) runPreparedClipboardPasteFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	return result
}

func (t *textInputBridge) runBackgroundClipboardQueueFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Attempted: true, Err: fmt.Errorf("phone bridge is not configured")}
	}
	if err := t.writeClipboard(ctx, bridge, args.Text); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	return result
}

func (t *textInputBridge) runLegacyBridgeFlow(ctx context.Context, platform string, args textInputArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Attempted: true, Err: fmt.Errorf("phone bridge is not configured")}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	if err := t.restoreBridgeAppIfNeeded(ctx, bridge, platform); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	bridge = t.currentBridge()
	if bridge == nil || !bridge.Connected() {
		return textViaBridgeResult{Attempted: true, Err: fmt.Errorf("phone bridge did not connect")}
	}
	if err := t.writeClipboard(ctx, bridge, args.Text); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	if err := t.sleepAfterClipboardWrite(ctx); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	if _, err := t.callQuickAction(ctx, "app_switch", platform); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	if err := t.returnToPreviousApp(ctx, engine); err != nil {
		return textViaBridgeResult{Attempted: true, Err: err}
	}
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	return result
}

func (t *textInputBridge) canUseClipboardFirst(platform string, text string) bool {
	bridge := t.currentBridge()
	if bridge == nil {
		return false
	}
	if bridge.ClipboardRecentlyEquals(text, preparedClipboardMaxAge) {
		return true
	}
	status := bridge.getStatus()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		return phoneBridgeCanUsePiPBackground(status, "clipboard_write") ||
			phoneBridgeCanRestoreFromReturnEntry(status) ||
			phoneBridgeReadyForCommand(status)
	case "android":
		return phoneBridgeReadyForCommand(status, "clipboard_write") || phoneBridgeCanUseFGSBackground(status, "clipboard_write")
	default:
		return false
	}
}

func (t *textInputBridge) writeClipboardPreservingTarget(ctx context.Context, bridge *PhoneBridge, platform, text string) (bool, error) {
	if !t.canWriteClipboardPreservingTarget(platform) {
		return false, nil
	}
	if err := t.writeClipboard(ctx, bridge, text); err != nil {
		return true, err
	}
	return true, nil
}

func (t *textInputBridge) canWriteClipboardPreservingTarget(platform string) bool {
	return strings.EqualFold(strings.TrimSpace(platform), "android")
}

func (t *textInputBridge) focusPasteVerify(ctx context.Context, engine *textInputEngine, platform string, args textInputArgs) textViaBridgeResult {
	var result textViaBridgeResult
	for attempt := 1; attempt <= textViaBridgePasteAttempts; attempt++ {
		if err := engine.applyFocus(ctx, args.Focus); err != nil {
			result.Err = err
			return result
		}
		if attempt == 1 {
			_, _, _ = t.pasteClipboard(ctx, platform)
		} else {
			if err := t.pasteViaContextMenu(ctx, engine, platform, args.Focus); err != nil {
				result.Err = err
				return result
			}
		}
		if err := t.sleepAfterPaste(ctx); err != nil {
			result.Err = err
			return result
		}
		analysis, _, err := engine.analyzeScreen(ctx, platform, args, nil)
		if err != nil {
			result.Err = err
			return result
		}
		if committed, committedFieldText := evaluateFieldCommit(analysis, args.Text); committed {
			result.Committed = true
			result.FieldText = committedFieldText
			return result
		} else {
			result.FieldText = analysis.FieldText
			if strings.TrimSpace(analysis.FieldText) != "" {
				return result
			}
		}
	}
	return result
}

func (t *textInputBridge) pasteViaContextMenu(ctx context.Context, engine *textInputEngine, platform string, focus focusPointArgs) error {
	if t == nil || t.hw == nil || t.hw.touchGesture == nil {
		return fmt.Errorf("touch_gesture is not configured for long-press paste fallback")
	}
	coordSpace := strings.TrimSpace(focus.CoordSpace)
	if coordSpace == "" {
		coordSpace = "normalized"
	}
	_, err := callTextInputTool(ctx, t.hw.touchGesture, jsonString(map[string]any{
		"type":        "long_press",
		"point":       map[string]any{"x": focus.X, "y": focus.Y},
		"coord_space": coordSpace,
		"hold_ms":     textViaBridgeLongPressMS,
	}))
	if err != nil {
		return fmt.Errorf("long-press focused field: %w", err)
	}
	if err := t.sleepAfterMenuOpen(ctx); err != nil {
		return err
	}
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return err
	}
	menu, err := t.findPasteMenuAction(ctx, shot, platform)
	if err != nil {
		return err
	}
	if !menu.Found {
		return fmt.Errorf("Paste/粘贴 menu action was not visible after long press")
	}
	if _, err := callTextInputTool(ctx, t.hw.touchGesture, jsonString(map[string]any{
		"type":        "tap",
		"point":       map[string]any{"x": menu.TapPoint.X, "y": menu.TapPoint.Y},
		"coord_space": menu.TapPoint.CoordSpace,
	})); err != nil {
		return fmt.Errorf("tap paste menu action: %w", err)
	}
	return nil
}

func (t *textInputBridge) findPasteMenuAction(ctx context.Context, shot screenshotResult, platform string) (pasteMenuResult, error) {
	if t != nil && t.findPasteMenuFn != nil {
		result, err := t.findPasteMenuFn(ctx, shot, platform)
		if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
			result.TapPoint.CoordSpace = "normalized"
		}
		return result, err
	}
	modelVision, ok := t.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return pasteMenuResult{}, fmt.Errorf("paste menu vision is not configured")
	}
	prompt := strings.TrimSpace(fmt.Sprintf(`Analyze this %s device screenshot after a long press inside a focused text field.
Find the visible system context-menu action that pastes the clipboard into that field. The label may be Paste, 粘贴, or another localized equivalent.
Return JSON only:
{
  "found": true,
  "tap_point": {"x": 500, "y": 700, "coord_space": "normalized"},
  "label": "Paste"
}

Rules:
- Return found=true only when a visible paste action is clearly tappable.
- Prefer the plain Paste/粘贴 action over unrelated actions such as Select, Look Up, Share, or Autofill.
- tap_point must be centered inside the visible paste action using normalized 0-1000 coordinates.
- If no paste action is visible, return {"found": false, "tap_point": {"x": 0, "y": 0, "coord_space": "normalized"}}.`, platform))
	raw, err := modelVision.visionJSON(ctx, "paste_menu", prompt, shot)
	if err != nil {
		return pasteMenuResult{}, err
	}
	var result pasteMenuResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return pasteMenuResult{}, fmt.Errorf("parse paste menu action: %w", err)
	}
	if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
		result.TapPoint.CoordSpace = "normalized"
	}
	return result, nil
}

func (t *textInputBridge) keyboardPaste(ctx context.Context, platform string) error {
	return t.keyboardTap(ctx, keyboardPasteKeys(platform))
}

func (t *textInputBridge) pasteClipboard(ctx context.Context, platform string) (method string, fallbackReason string, err error) {
	if t != nil && t.hw != nil && t.hw.quickAction != nil {
		if _, err := t.callQuickAction(ctx, "paste", platform); err == nil {
			return "quick_action", "", nil
		} else {
			fallbackReason = err.Error()
		}
	}
	if err := t.keyboardPaste(ctx, platform); err != nil {
		if fallbackReason != "" {
			return "", fallbackReason, fmt.Errorf("quick_action paste failed: %s; keyboard paste failed: %w", fallbackReason, err)
		}
		return "", "", err
	}
	return "keyboard", fallbackReason, nil
}

func (t *textInputBridge) keyboardTap(ctx context.Context, keys []string) error {
	if t == nil || t.hw == nil || t.hw.keyboardTap == nil {
		return fmt.Errorf("keyboard_tap is not configured")
	}
	out, err := callTextInputTool(ctx, t.hw.keyboardTap, jsonString(map[string]any{"keys": keys}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func keyboardPasteKeys(platform string) []string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android":
		return []string{"ctrl", "v"}
	default:
		return []string{"meta", "v"}
	}
}

func (t *textInputBridge) restoreBridgeAppIfNeeded(ctx context.Context, bridge *PhoneBridge, platform string) error {
	if t == nil || t.hw == nil || t.hw.quickAction == nil || t.hw.keyboardText == nil || t.hw.touchGesture == nil {
		return fmt.Errorf("bridge recovery tools are not fully configured")
	}
	searchTerm := textViaBridgeSearchTerm(platform, bridge.getStatus())
	localEntry := &EnterTextTool{
		engine:               newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep),
		iosKeyboardIsolation: iosKeyboardIsolationControllerFromContext(ctx),
	}
	openResult, err := runAppSearchOpenFlow(ctx, appSearchOpenFlowConfig{
		hw:               t.hw,
		vision:           t.vision,
		platform:         platform,
		searchTerm:       searchTerm,
		findAppTapFn:     t.findAppTapFn,
		confirmAppOpenFn: t.confirmAppOpenFn,
		entryTool:        localEntry,
		launchDelay:      appSearchOpenLaunchDelay,
		sleep:            t.sleep,
	})
	if err != nil {
		return err
	}
	if !openResult.Opened {
		if reason := strings.TrimSpace(openResult.Reason); reason != "" {
			return fmt.Errorf("bridge app did not open: %s", reason)
		}
		return fmt.Errorf("bridge app did not open")
	}
	return t.waitForBridgeConnection(ctx)
}

func (t *textInputBridge) tapTouchPoint(ctx context.Context, point focusPointArgs) error {
	out, err := t.hw.touchGesture.Call(ctx, jsonString(map[string]any{
		"type":        "tap",
		"point":       map[string]any{"x": point.X, "y": point.Y},
		"coord_space": firstNonEmptyString([]string{strings.TrimSpace(point.CoordSpace), "normalized"}),
	}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func (t *textInputBridge) returnToPreviousApp(ctx context.Context, engine *textInputEngine) error {
	for attempt := 1; attempt <= textViaBridgeRecentsSwipes+1; attempt++ {
		result, err := t.findPreviousAppCard(ctx, engine)
		if err != nil {
			return err
		}
		if result.Found {
			return t.tapTouchPoint(ctx, result.TapPoint)
		}
		if attempt > textViaBridgeRecentsSwipes {
			break
		}
		if err := t.swipeRecentsToFindPreviousApp(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("previous app card not found in app switcher")
}

func (t *textInputBridge) swipeRecentsToFindPreviousApp(ctx context.Context) error {
	out, err := t.hw.touchGesture.Call(ctx, jsonString(map[string]any{
		"type":        "swipe_right",
		"coord_space": "normalized",
		"strength":    "medium",
	}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func (t *textInputBridge) findPreviousAppCard(ctx context.Context, engine *textInputEngine) (previousAppCardResult, error) {
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return previousAppCardResult{}, err
	}
	if t != nil && t.findPrevAppFn != nil {
		result, err := t.findPrevAppFn(ctx, shot)
		return result, err
	}
	modelVision, ok := t.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return previousAppCardResult{}, fmt.Errorf("previous app selection vision is not configured")
	}
	prompt := strings.TrimSpace(`Analyze this device screenshot of the app switcher / recent apps view.
Find the previous app card that should be tapped to return from the Aiden companion app back to the user's prior task.
Return JSON only:
{
  "found": true,
  "tap_point": {"x": 180, "y": 290, "coord_space": "normalized"},
  "label": "Settings"
}

Rules:
- Return found=true only when a non-Aiden previous app card is clearly visible and tappable.
- Prefer the app card that represents the task immediately before Aiden, not the Aiden card itself.
- tap_point must be inside the visible app card body using normalized 0-1000 coordinates.
- If not visible, return {"found": false, "tap_point": {"x": 0, "y": 0, "coord_space": "normalized"}}.`)
	raw, err := modelVision.visionJSON(ctx, "previous_app", prompt, shot)
	if err != nil {
		return previousAppCardResult{}, err
	}
	var result previousAppCardResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return previousAppCardResult{}, fmt.Errorf("parse previous app card: %w", err)
	}
	if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
		result.TapPoint.CoordSpace = "normalized"
	}
	return result, nil
}

func (t *textInputBridge) waitForBridgeConnection(ctx context.Context) error {
	deadline := time.NewTimer(textViaBridgeConnectTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(textViaBridgePollInterval)
	defer ticker.Stop()
	for {
		bridge := t.currentBridge()
		if bridge != nil && bridge.Connected() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for phone bridge to connect")
		case <-ticker.C:
		}
	}
}

func (t *textInputBridge) sleepAfterClipboardWrite(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostWriteDelay)
}

func (t *textInputBridge) sleepAfterPaste(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostPasteDelay)
}

func (t *textInputBridge) sleepAfterMenuOpen(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgeMenuDelay)
}

func (t *textInputBridge) currentBridge() *PhoneBridge {
	if t == nil || t.bridgeFn == nil {
		return nil
	}
	return t.bridgeFn()
}

func (t *textInputBridge) callQuickAction(ctx context.Context, action, platform string) (string, error) {
	out, err := t.hw.quickAction.Call(ctx, jsonString(map[string]any{"action": action, "platform": platform}))
	if err != nil {
		return out, err
	}
	if err := interpretTextInputToolOutput(out); err != nil {
		return out, err
	}
	return out, nil
}

func (t *textInputBridge) writeClipboard(ctx context.Context, bridge *PhoneBridge, text string) error {
	if t != nil && t.clipboardWriteFn != nil {
		return t.clipboardWriteFn(ctx, bridge, text)
	}
	clipboard := NewClipboardTool(bridge, nil)
	// Isolate nested ClipboardTool SetToolError so a bridge failure cannot
	// poison the parent enter_text / search_launch_app observation.
	toolCtx, _ := WithToolError(ctx)
	clipOut, clipErr := clipboard.Call(toolCtx, jsonString(map[string]any{"action": "write", "text": text}))
	if clipErr != nil {
		return clipErr
	}
	if te := ToolErrorFromContext(toolCtx); te != nil {
		return te
	}
	return interpretTextInputToolOutput(clipOut)
}

func textViaBridgeSearchTerm(platform string, status PhoneBridgeStatus) string {
	if strings.EqualFold(strings.TrimSpace(platform), "android") && status.Environment != nil {
		for _, app := range status.Environment.AvailableApps {
			if strings.TrimSpace(app.AndroidPackage) == "com.qing.aidenbridgedaily" && strings.TrimSpace(app.Name) != "" {
				return app.Name
			}
		}
	}
	return "Aiden"
}
