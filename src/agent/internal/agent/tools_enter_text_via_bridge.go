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
	textViaBridgePostSendDelay  = 800 * time.Millisecond
	textViaBridgeOpenAttempts   = 2
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
	Attempted         bool
	Committed         bool
	Sent              bool
	SendVerified      bool
	FieldText         string
	PostSendFieldText string
	VLMCalls          int
	Err               error
}

type EnterTextViaBridgeTool struct {
	hw                   *textInputHardwareDeps
	vision               textInputVision
	bridgeFn             func() *PhoneBridge
	clipboardWriteFn     func(context.Context, *PhoneBridge, string) error
	findAppTapFn         func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn     func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	findPrevAppFn        func(context.Context, screenshotResult) (previousAppCardResult, error)
	findPasteMenuFn      func(context.Context, screenshotResult, string) (pasteMenuResult, error)
	sleep                func(context.Context, time.Duration) error
	iosKeyboardIsolation *iosKeyboardIsolationController
}

func (t *EnterTextViaBridgeTool) Name() string { return "enter_text_via_bridge" }

func (t *EnterTextViaBridgeTool) Description() string {
	return `Use the Phone Bridge clipboard path to place known text into an input field, then focus, paste, and verify. ` +
		`Do not call this until the latest screenshot clearly shows the actual editable field or composer and the focus coordinates are inside it. Merely opening an app, reaching a folder/list view, or seeing a create/new button does not mean text entry is ready; first create or open the document/message and observe its editor. Never paste into guessed blank space. ` +
		`Use this for final chat/message composer text when runtime Phone Bridge status reports a usable clipboard route, especially for long, multiline, CJK, or other non-ASCII text. Target-preserving routes such as a prepared clipboard, iOS PiP queue, or Android connected/FGS bridge are suitable even for short replies. ` +
		`It writes the clipboard itself when needed, so do not call bridge_clipboard first as a staging step. It verifies whether shortcut paste actually changed the field and falls back to long-pressing the field and tapping the visible Paste/粘贴 menu action when needed. If Bridge would need app restoration or is unavailable, prefer enter_text_in_field for short search terms, contact lookup, and normal IME/pinyin entry. ` +
		`Returns committed:true only when the exact target text is verified in the field; when send_after_commit=true, ok=true also requires send_verified:true. If the structured result conflicts with the attached screenshot, call wait_for_stable_screen once before deciding. Preserve the current field while evidence conflicts, and perform corrective input only after the fresh observation identifies a concrete mismatch.`
}

func (t *EnterTextViaBridgeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":              stringArgSchema("Exact text that must appear in the field when done."),
		"focus":             focusPointArgSchema("Coordinates inside an actual editable field or composer that is clearly visible in the latest screenshot; do not use blank space, an app/folder/list page, or a create/new button as the field."),
		"send_after_commit": boolArgSchema("After field text is verified, press the platform send/submit key and verify the target text is no longer still present in the input field. Set true when the user asked to send, reply, or message and the target chat/composer is already open."),
	}, "text", "focus")
}

func (t *EnterTextViaBridgeTool) Call(ctx context.Context, input string) (string, error) {
	var controller *iosKeyboardIsolationController
	if t != nil {
		controller = t.iosKeyboardIsolation
	}
	return withIOSKeyboardIsolationBatchCall(ctx, controller, func(batchCtx context.Context) (string, error) {
		return t.call(batchCtx, input)
	})
}

func (t *EnterTextViaBridgeTool) call(ctx context.Context, input string) (string, error) {
	if t == nil || t.hw == nil || t.vision == nil {
		return "error: enter_text_via_bridge is not fully configured", nil
	}
	var args enterTextInFieldArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	platform := t.hw.platform()
	result := enterTextInFieldResult{
		TargetText:   args.Text,
		RequiredMode: string(requiredTextInputMode(args.Text)),
		Attempts:     1,
	}
	if strings.TrimSpace(args.Text) == "" {
		result.Reason = "text is required"
		return jsonString(result), nil
	}
	bridgeResult := t.runBridgeFlow(ctx, platform, args)
	result = textViaBridgeResultToFieldResult(args.Text, bridgeResult, args.SendAfterCommit)
	return jsonString(result), nil
}

func (t *EnterTextViaBridgeTool) runBridgeFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	result := t.runClipboardFirstFlow(ctx, platform, args)
	if result.Attempted || result.Err != nil {
		return result
	}
	result.Err = fmt.Errorf("reliable bridge clipboard path unavailable; use enter_text_in_field fallback instead")
	return result
}

func (t *EnterTextViaBridgeTool) runClipboardFirstResult(ctx context.Context, args enterTextInFieldArgs) (enterTextInFieldResult, bool) {
	platform := textInputHardwarePlatform(t.hw)
	result := enterTextInFieldResult{
		TargetText:   strings.TrimSpace(args.Text),
		RequiredMode: string(requiredTextInputMode(args.Text)),
		Attempts:     1,
	}
	if result.TargetText == "" {
		result.Reason = "text is required"
		return result, false
	}
	bridgeResult := t.runAutomaticClipboardFirstFlow(ctx, platform, args)
	if !bridgeResult.Attempted {
		if bridgeResult.Err != nil {
			return textViaBridgeResultToFieldResult(args.Text, bridgeResult, args.SendAfterCommit), true
		}
		result.Reason = "reliable bridge clipboard path unavailable"
		return result, false
	}
	return textViaBridgeResultToFieldResult(args.Text, bridgeResult, args.SendAfterCommit), true
}

func textViaBridgeResultToFieldResult(text string, bridgeResult textViaBridgeResult, sendAfterCommit bool) enterTextInFieldResult {
	targetText := strings.TrimSpace(text)
	result := enterTextInFieldResult{
		TargetText:   targetText,
		RequiredMode: string(requiredTextInputMode(targetText)),
		Attempts:     1,
	}
	result.Committed = bridgeResult.Committed
	result.Sent = bridgeResult.Sent
	result.SendVerified = bridgeResult.SendVerified
	result.FieldText = bridgeResult.FieldText
	result.PostSendFieldText = bridgeResult.PostSendFieldText
	result.VLMCalls = bridgeResult.VLMCalls
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
	if sendAfterCommit {
		result.Reason = "send verified"
		return result
	}
	result.Reason = "field verified"
	return result
}

func (t *EnterTextViaBridgeTool) runClipboardFirstFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
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
		if phoneBridgeCanRestoreFromReturnEntry(status) || phoneBridgeReadyForCommand(status) {
			return t.runLegacyBridgeFlow(ctx, platform, args)
		}
		return textViaBridgeResult{}
	case "android":
		if bridge.ClipboardRecentlyContains(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		return t.runTargetPreservingClipboardFlow(ctx, platform, args)
	default:
		return textViaBridgeResult{}
	}
}

func (t *EnterTextViaBridgeTool) runAutomaticClipboardFirstFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
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

func (t *EnterTextViaBridgeTool) runTargetPreservingClipboardFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Err: fmt.Errorf("phone bridge is not configured")}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	preserved, _, preserveErr := t.writeClipboardPreservingTarget(ctx, bridge, platform, args.Text)
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

func (t *EnterTextViaBridgeTool) runPreparedClipboardPasteFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	return result
}

func (t *EnterTextViaBridgeTool) runBackgroundClipboardQueueFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
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

func (t *EnterTextViaBridgeTool) runLegacyBridgeFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Attempted: true, Err: fmt.Errorf("phone bridge is not configured")}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	_, restoreCalls, restoreErr := t.restoreBridgeAppIfNeeded(ctx, bridge, platform)
	vlmCalls := restoreCalls
	if restoreErr != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: restoreErr}
	}
	bridge = t.currentBridge()
	if bridge == nil || !bridge.Connected() {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: fmt.Errorf("phone bridge did not connect")}
	}
	if err := t.writeClipboard(ctx, bridge, args.Text); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: err}
	}
	if err := t.sleepAfterClipboardWrite(ctx); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: err}
	}
	if _, err := t.callQuickAction(ctx, "app_switch", platform); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: err}
	}
	returnCalls, err := t.returnToPreviousApp(ctx, engine)
	vlmCalls += returnCalls
	if err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Err: err}
	}
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	result.VLMCalls += vlmCalls
	return result
}

func (t *EnterTextViaBridgeTool) canUseClipboardFirst(platform string, text string) bool {
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

func (t *EnterTextViaBridgeTool) writeClipboardPreservingTarget(ctx context.Context, bridge *PhoneBridge, platform, text string) (bool, []string, error) {
	if !t.canWriteClipboardPreservingTarget(platform) {
		return false, nil, nil
	}
	if err := t.writeClipboard(ctx, bridge, text); err != nil {
		return true, []string{"clipboard-first: target-preserving clipboard write failed"}, err
	}
	return true, []string{"clipboard-first: wrote clipboard without leaving target app"}, nil
}

func (t *EnterTextViaBridgeTool) canWriteClipboardPreservingTarget(platform string) bool {
	return strings.EqualFold(strings.TrimSpace(platform), "android")
}

func (t *EnterTextViaBridgeTool) focusPasteVerify(ctx context.Context, engine *textInputEngine, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	var result textViaBridgeResult
	for attempt := 1; attempt <= textViaBridgePasteAttempts; attempt++ {
		if err := engine.applyFocus(ctx, args.Focus); err != nil {
			result.Err = err
			return result
		}
		var pasteErr error
		if attempt == 1 {
			_, _, pasteErr = t.pasteClipboard(ctx, platform)
		} else {
			var calls int
			var err error
			_, calls, _, err = t.pasteViaContextMenu(ctx, engine, platform, args.Focus)
			result.VLMCalls += calls
			if err != nil {
				result.Err = err
				return result
			}
		}
		if err := t.sleepAfterPaste(ctx); err != nil {
			result.Err = err
			return result
		}
		analysis, calls, analyzeSteps, err := engine.analyzeScreen(ctx, platform, args, nil)
		result.VLMCalls += calls
		_ = analyzeSteps
		if err != nil {
			result.Err = err
			return result
		}
		if committed, committedFieldText := evaluateFieldCommit(analysis, args.Text); committed {
			result.Committed = true
			result.FieldText = committedFieldText
			if args.SendAfterCommit {
				sent, verified, postFieldText, calls, sendSteps, err := t.keyboardSendAndVerify(ctx, engine, platform, args)
				result.Sent = sent
				result.SendVerified = verified
				result.PostSendFieldText = postFieldText
				result.VLMCalls += calls
				_ = sendSteps
				result.Err = err
			}
			return result
		} else {
			result.FieldText = analysis.FieldText
			if strings.TrimSpace(analysis.FieldText) != "" {
				return result
			}
			if attempt == 1 {
				_ = pasteErr
			}
		}
	}
	return result
}

func (t *EnterTextViaBridgeTool) pasteViaContextMenu(ctx context.Context, engine *textInputEngine, platform string, focus focusPointArgs) (method string, vlmCalls int, steps []string, err error) {
	if t == nil || t.hw == nil || t.hw.touchGesture == nil {
		return "", 0, steps, fmt.Errorf("touch_gesture is not configured for long-press paste fallback")
	}
	coordSpace := strings.TrimSpace(focus.CoordSpace)
	if coordSpace == "" {
		coordSpace = "normalized"
	}
	_, err = callTextInputTool(ctx, t.hw.touchGesture, jsonString(map[string]any{
		"type":        "long_press",
		"point":       map[string]any{"x": focus.X, "y": focus.Y},
		"coord_space": coordSpace,
		"hold_ms":     textViaBridgeLongPressMS,
	}))
	if err != nil {
		return "", 0, steps, fmt.Errorf("long-press focused field: %w", err)
	}
	steps = append(steps, "clipboard-first: long-pressed focused field to open paste menu")
	if err := t.sleepAfterMenuOpen(ctx); err != nil {
		return "", 0, steps, err
	}
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return "", 0, steps, err
	}
	menu, calls, err := t.findPasteMenuAction(ctx, shot, platform)
	vlmCalls += calls
	if err != nil {
		return "", vlmCalls, steps, err
	}
	if !menu.Found {
		return "", vlmCalls, steps, fmt.Errorf("Paste/粘贴 menu action was not visible after long press")
	}
	if _, err := callTextInputTool(ctx, t.hw.touchGesture, jsonString(map[string]any{
		"type":        "tap",
		"point":       map[string]any{"x": menu.TapPoint.X, "y": menu.TapPoint.Y},
		"coord_space": menu.TapPoint.CoordSpace,
	})); err != nil {
		return "", vlmCalls, steps, fmt.Errorf("tap paste menu action: %w", err)
	}
	label := strings.TrimSpace(menu.Label)
	if label == "" {
		label = "Paste/粘贴"
	}
	steps = append(steps, fmt.Sprintf("clipboard-first: tapped context menu action %q", label))
	return "context-menu", vlmCalls, steps, nil
}

func (t *EnterTextViaBridgeTool) findPasteMenuAction(ctx context.Context, shot screenshotResult, platform string) (pasteMenuResult, int, error) {
	if t != nil && t.findPasteMenuFn != nil {
		result, err := t.findPasteMenuFn(ctx, shot, platform)
		if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
			result.TapPoint.CoordSpace = "normalized"
		}
		return result, 0, err
	}
	modelVision, ok := t.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return pasteMenuResult{}, 0, fmt.Errorf("paste menu vision is not configured")
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
		return pasteMenuResult{}, 1, err
	}
	var result pasteMenuResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return pasteMenuResult{}, 1, fmt.Errorf("parse paste menu action: %w", err)
	}
	if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
		result.TapPoint.CoordSpace = "normalized"
	}
	return result, 1, nil
}

func (t *EnterTextViaBridgeTool) keyboardPaste(ctx context.Context, platform string) error {
	return t.keyboardTap(ctx, keyboardPasteKeys(platform))
}

func (t *EnterTextViaBridgeTool) pasteClipboard(ctx context.Context, platform string) (method string, fallbackReason string, err error) {
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

func (t *EnterTextViaBridgeTool) keyboardSendAndVerify(ctx context.Context, engine *textInputEngine, platform string, args enterTextInFieldArgs) (sent bool, verified bool, postFieldText string, vlmCalls int, steps []string, err error) {
	if err := t.keyboardTap(ctx, keyboardSendKeys(platform)); err != nil {
		return false, false, "", 0, append(steps, "clipboard-first keyboard send failed"), err
	}
	sent = true
	steps = append(steps, "clipboard-first: keyboard send submitted")
	if err := t.sleepAfterSend(ctx); err != nil {
		return sent, false, "", 0, append(steps, "clipboard-first: wait after send canceled"), err
	}
	analysis, calls, analyzeSteps, err := engine.analyzeScreen(ctx, platform, args, nil)
	vlmCalls += calls
	steps = append(steps, analyzeSteps...)
	if err != nil {
		return sent, false, "", vlmCalls, steps, err
	}
	postFieldText = analysis.FieldText
	if sendVerifiedByFieldClearedOrChanged(analysis, args.Text) {
		steps = append(steps, "clipboard-first: send verified by cleared/changed input field")
		return sent, true, postFieldText, vlmCalls, steps, nil
	}
	steps = append(steps, fmt.Sprintf("clipboard-first: send not verified; input still contains target text %q", postFieldText))
	return sent, false, postFieldText, vlmCalls, steps, nil
}

func (t *EnterTextViaBridgeTool) keyboardTap(ctx context.Context, keys []string) error {
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

func (t *EnterTextViaBridgeTool) restoreBridgeAppIfNeeded(ctx context.Context, bridge *PhoneBridge, platform string) (steps []string, vlmCalls int, err error) {
	if t.hw.quickAction == nil || t.hw.keyboardText == nil || t.hw.touchGesture == nil {
		return nil, 0, fmt.Errorf("bridge recovery tools are not fully configured")
	}
	searchTerm := textViaBridgeSearchTerm(platform, bridge.getStatus())
	openResult, err := runAppSearchOpenFlow(ctx, appSearchOpenFlowConfig{
		hw:               t.hw,
		vision:           t.vision,
		platform:         platform,
		searchTerm:       searchTerm,
		findAppTapFn:     t.findAppTapFn,
		confirmAppOpenFn: t.confirmAppOpenFn,
		entryTool:        &EnterTextInFieldTool{engine: newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)},
		launchDelay:      appSearchOpenLaunchDelay,
		sleep:            t.sleep,
	})
	vlmCalls += openResult.VLMCalls
	if err != nil {
		return steps, vlmCalls, err
	}
	if !openResult.Opened {
		vlmCalls++
		if strings.TrimSpace(openResult.Reason) != "" {
			return append(steps, "clipboard-first: bridge app did not open"), vlmCalls, fmt.Errorf("bridge app did not open: %s", strings.TrimSpace(openResult.Reason))
		}
		return append(steps, "clipboard-first: bridge app did not open"), vlmCalls, fmt.Errorf("bridge app did not open")
	}
	if err := t.waitForBridgeConnection(ctx); err != nil {
		return append(steps, "clipboard-first: bridge did not reconnect"), vlmCalls, err
	}
	steps = append(steps, "clipboard-first: bridge connected")
	return steps, vlmCalls, nil
}

func (t *EnterTextViaBridgeTool) tapTouchPoint(ctx context.Context, point focusPointArgs) error {
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

func (t *EnterTextViaBridgeTool) returnToPreviousApp(ctx context.Context, engine *textInputEngine) (int, error) {
	vlmCalls := 0
	for attempt := 1; attempt <= textViaBridgeRecentsSwipes+1; attempt++ {
		result, calls, err := t.findPreviousAppCard(ctx, engine)
		vlmCalls += calls
		if err != nil {
			return vlmCalls, err
		}
		if result.Found {
			return vlmCalls, t.tapTouchPoint(ctx, result.TapPoint)
		}
		if attempt > textViaBridgeRecentsSwipes {
			break
		}
		if err := t.swipeRecentsToFindPreviousApp(ctx); err != nil {
			return vlmCalls, err
		}
	}
	return vlmCalls, fmt.Errorf("previous app card not found in app switcher")
}

func (t *EnterTextViaBridgeTool) swipeRecentsToFindPreviousApp(ctx context.Context) error {
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

func (t *EnterTextViaBridgeTool) findPreviousAppCard(ctx context.Context, engine *textInputEngine) (previousAppCardResult, int, error) {
	shot, err := engine.captureScreenshot(ctx)
	if err != nil {
		return previousAppCardResult{}, 0, err
	}
	if t != nil && t.findPrevAppFn != nil {
		result, err := t.findPrevAppFn(ctx, shot)
		return result, 0, err
	}
	modelVision, ok := t.vision.(*llmTextInputVision)
	if !ok || modelVision == nil {
		return previousAppCardResult{}, 0, fmt.Errorf("previous app selection vision is not configured")
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
		return previousAppCardResult{}, 1, err
	}
	var result previousAppCardResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return previousAppCardResult{}, 1, fmt.Errorf("parse previous app card: %w", err)
	}
	if strings.TrimSpace(result.TapPoint.CoordSpace) == "" {
		result.TapPoint.CoordSpace = "normalized"
	}
	return result, 1, nil
}

func (t *EnterTextViaBridgeTool) waitForBridgeConnection(ctx context.Context) error {
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

func (t *EnterTextViaBridgeTool) sleepAfterClipboardWrite(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostWriteDelay)
}

func (t *EnterTextViaBridgeTool) sleepAfterPaste(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostPasteDelay)
}

func (t *EnterTextViaBridgeTool) sleepAfterMenuOpen(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgeMenuDelay)
}

func (t *EnterTextViaBridgeTool) sleepAfterSend(ctx context.Context) error {
	sleep := sleepWithContext
	if t != nil && t.sleep != nil {
		sleep = t.sleep
	}
	return sleep(ctx, textViaBridgePostSendDelay)
}

func (t *EnterTextViaBridgeTool) currentBridge() *PhoneBridge {
	if t == nil || t.bridgeFn == nil {
		return nil
	}
	return t.bridgeFn()
}

func (t *EnterTextViaBridgeTool) callQuickAction(ctx context.Context, action, platform string) (string, error) {
	out, err := t.hw.quickAction.Call(ctx, jsonString(map[string]any{"action": action, "platform": platform}))
	if err != nil {
		return out, err
	}
	if err := interpretTextInputToolOutput(out); err != nil {
		return out, err
	}
	return out, nil
}

func (t *EnterTextViaBridgeTool) writeClipboard(ctx context.Context, bridge *PhoneBridge, text string) error {
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
