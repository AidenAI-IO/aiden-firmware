package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	textViaBridgePostPasteDelay = 350 * time.Millisecond
	textViaBridgeMenuDelay      = 450 * time.Millisecond
	textViaBridgePostSendDelay  = 800 * time.Millisecond
	textViaBridgePasteAttempts  = 2
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

type pasteMenuResult struct {
	Found    bool           `json:"found"`
	TapPoint focusPointArgs `json:"tap_point"`
	Label    string         `json:"label,omitempty"`
}

type textViaBridgeResult struct {
	Attempted    bool
	Committed    bool
	SendVerified bool
	FieldText    string
	Err          error
}

type textInputBridge struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	bridgeFn         func() *PhoneBridge
	clipboardWriteFn func(context.Context, *PhoneBridge, string) error
	findPasteMenuFn  func(context.Context, screenshotResult, string) (pasteMenuResult, error)
	sleep            func(context.Context, time.Duration) error
}

func (t *textInputBridge) runClipboardFirstResult(ctx context.Context, args textInputArgs) (textInputResult, bool) {
	platform := textInputHardwarePlatform(t.hw)
	if strings.TrimSpace(args.Text) == "" {
		return textInputResult{Reason: "text is required"}, false
	}
	bridgeResult := t.runAutomaticClipboardFirstFlow(ctx, platform, args)
	if !bridgeResult.Attempted {
		if bridgeResult.Err != nil {
			return textViaBridgeResultToTextInputResult(bridgeResult, args.SendAfterCommit), true
		}
		return textInputResult{Reason: "reliable bridge clipboard path unavailable"}, false
	}
	return textViaBridgeResultToTextInputResult(bridgeResult, args.SendAfterCommit), true
}

func textViaBridgeResultToTextInputResult(bridgeResult textViaBridgeResult, sendAfterCommit bool) textInputResult {
	result := textInputResult{}
	result.Committed = bridgeResult.Committed
	result.SendVerified = bridgeResult.SendVerified
	result.FieldText = bridgeResult.FieldText
	result.OK = bridgeResult.Committed
	if sendAfterCommit {
		result.OK = bridgeResult.Committed && bridgeResult.SendVerified
	}
	if bridgeResult.Err != nil {
		result.Reason = bridgeResult.Err.Error()
		return result
	}
	if !bridgeResult.Committed {
		result.Reason = "bridge clipboard input not verified in field"
		return result
	}
	if sendAfterCommit && !bridgeResult.SendVerified {
		result.Reason = "field verified but send was not verified"
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
		if bridge.ClipboardRecentlyContains(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		if phoneBridgeCanUsePiPBackground(status, "clipboard_write") {
			return t.runBackgroundClipboardQueueFlow(ctx, platform, args)
		}
		return textViaBridgeResult{}
	case "android":
		if bridge.ClipboardRecentlyContains(args.Text, preparedClipboardMaxAge) {
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

func (t *textInputBridge) canUseClipboardFirst(platform string, text string) bool {
	bridge := t.currentBridge()
	if bridge == nil {
		return false
	}
	if bridge.ClipboardRecentlyContains(text, preparedClipboardMaxAge) {
		return true
	}
	status := bridge.getStatus()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		return phoneBridgeCanUsePiPBackground(status, "clipboard_write")
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
			if args.SendAfterCommit {
				verified, err := t.keyboardSendAndVerify(ctx, engine, platform, args)
				result.SendVerified = verified
				result.Err = err
			}
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

func (t *textInputBridge) keyboardSendAndVerify(ctx context.Context, engine *textInputEngine, platform string, args textInputArgs) (bool, error) {
	if err := t.keyboardTap(ctx, keyboardSendKeys(platform)); err != nil {
		return false, err
	}
	if err := t.sleepAfterSend(ctx); err != nil {
		return false, err
	}
	analysis, _, err := engine.analyzeScreen(ctx, platform, args, nil)
	if err != nil {
		return false, err
	}
	return sendVerifiedByFieldClearedOrChanged(analysis, args.Text), nil
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

func keyboardSendKeys(_ string) []string {
	return []string{"enter"}
}

func sendVerifiedByFieldClearedOrChanged(analysis textInputScreenAnalysis, targetText string) bool {
	fieldText := strings.TrimSpace(analysis.FieldText)
	targetText = strings.TrimSpace(targetText)
	if fieldText == "" || targetText == "" {
		return fieldText == ""
	}
	if committed, _ := evaluateFieldCommit(analysis, targetText); committed {
		return false
	}
	fieldCompact := compactTextForSendVerify(fieldText)
	targetCompact := compactTextForSendVerify(targetText)
	if fieldCompact == "" || targetCompact == "" {
		return fieldCompact == ""
	}
	if fieldCompact == targetCompact || strings.Contains(fieldCompact, targetCompact) {
		return false
	}
	if len([]rune(fieldCompact)) >= 8 && strings.Contains(targetCompact, fieldCompact) {
		return false
	}
	return true
}

func compactTextForSendVerify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
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

func (t *textInputBridge) sleepAfterSend(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostSendDelay)
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
