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
	textViaBridgePostSendDelay  = 800 * time.Millisecond
	textViaBridgeOpenAttempts   = 2
	textViaBridgePasteAttempts  = 2
	textViaBridgeRecentsSwipes  = 3
	preparedClipboardMaxAge     = 5 * time.Minute
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

type textViaBridgeResult struct {
	Attempted         bool
	Committed         bool
	Sent              bool
	SendVerified      bool
	FieldText         string
	PostSendFieldText string
	VLMCalls          int
	Steps             []string
	Err               error
}

type EnterTextViaBridgeTool struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	bridgeFn         func() *PhoneBridge
	clipboardWriteFn func(context.Context, *PhoneBridge, string) error
	findAppTapFn     func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	findPrevAppFn    func(context.Context, screenshotResult) (previousAppCardResult, error)
	platformFn       func() string
	sleep            func(context.Context, time.Duration) error
}

func (t *EnterTextViaBridgeTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *EnterTextViaBridgeTool) Name() string { return "enter_text_via_bridge" }

func (t *EnterTextViaBridgeTool) Description() string {
	return strings.TrimSpace(`Use the Phone Bridge clipboard path to place known text into an input field. ` +
		`On iOS, if the target text was already prepared with clipboard write, it focuses the current target field, pastes, and verifies without reopening Aiden. ` +
		`When explicitly needed and Dynamic Island return is available, this tool can restore Aiden, write clipboard in the app, return to the target app, focus the field, paste, and verify the field text. ` +
		`On Android, one call writes clipboard through the connected bridge, focuses the field, pastes, and verifies. ` +
		`For message composition fields, set send_after_commit=true only after the target chat is open; the tool then runs focus → paste → verify field text → keyboard send → verify the input cleared/changed after send. ` +
		`Use enter_text_in_field for normal field entry; it automatically prefers this clipboard strategy when appropriate and falls back to HID/IME input if needed. ` +
		`If the reliable clipboard path is unavailable, it returns committed:false. ` +
		`Returns committed:true only when the exact target text is verified in the input field; when send_after_commit=true, ok=true also requires send_verified:true.`)
}

func (t *EnterTextViaBridgeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":     stringArgSchema("Exact text that must appear in the field when done."),
		"platform": stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"focus":    focusPointArgSchema("Input field coordinates."),
		"send_after_commit": map[string]any{
			"type":        "boolean",
			"description": "After field text is verified, press the platform send/submit key and verify the target text is no longer still present in the input field.",
		},
	}, "text", "focus")
}

func (t *EnterTextViaBridgeTool) Call(ctx context.Context, input string) (string, error) {
	if t == nil || t.hw == nil || t.vision == nil {
		return "error: enter_text_via_bridge is not fully configured", nil
	}
	var args enterTextInFieldArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if t.platformFn != nil {
		if override := strings.TrimSpace(t.platformFn()); override != "" {
			args.Platform = override
		}
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	args.Text = strings.TrimSpace(args.Text)
	result := enterTextInFieldResult{
		TargetText:   args.Text,
		RequiredMode: string(requiredTextInputMode(args.Text)),
		Attempts:     1,
	}
	if args.Text == "" {
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
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
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
	result.Steps = bridgeResult.Steps
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
	status := bridge.Status()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		if bridge.ClipboardRecentlyContains(args.Text, preparedClipboardMaxAge) {
			return t.runPreparedClipboardPasteFlow(ctx, platform, args)
		}
		if phoneBridgeCanRestoreFromReturnEntry(status) || phoneBridgeReadyForCommand(status) {
			return t.runLegacyBridgeFlow(ctx, platform, args)
		}
		return textViaBridgeResult{}
	case "android":
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
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		if !bridge.ClipboardRecentlyContains(args.Text, preparedClipboardMaxAge) {
			return textViaBridgeResult{}
		}
		return t.runPreparedClipboardPasteFlow(ctx, platform, args)
	case "android":
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
	preserved, preserveSteps, preserveErr := t.writeClipboardPreservingTarget(ctx, bridge, platform, args.Text)
	if preserveErr != nil {
		return textViaBridgeResult{Attempted: true, Steps: preserveSteps, Err: preserveErr}
	}
	if !preserved {
		return textViaBridgeResult{}
	}
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	result.Steps = append(preserveSteps, result.Steps...)
	return result
}

func (t *EnterTextViaBridgeTool) runPreparedClipboardPasteFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	result.Steps = append([]string{"clipboard-first: using prepared clipboard in current app"}, result.Steps...)
	return result
}

func (t *EnterTextViaBridgeTool) runLegacyBridgeFlow(ctx context.Context, platform string, args enterTextInFieldArgs) textViaBridgeResult {
	bridge := t.currentBridge()
	if bridge == nil {
		return textViaBridgeResult{Attempted: true, Err: fmt.Errorf("phone bridge is not configured")}
	}
	engine := newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep)
	restoreSteps, restoreCalls, restoreErr := t.restoreBridgeAppIfNeeded(ctx, bridge, platform)
	steps := append([]string{}, restoreSteps...)
	vlmCalls := restoreCalls
	if restoreErr != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: steps, Err: restoreErr}
	}
	bridge = t.currentBridge()
	if bridge == nil || !bridge.Connected() {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: steps, Err: fmt.Errorf("phone bridge did not connect")}
	}
	if err := t.writeClipboard(ctx, bridge, args.Text); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: append(steps, "clipboard write failed"), Err: err}
	}
	steps = append(steps, "clipboard-first: wrote clipboard in bridge app")
	if err := t.sleepAfterClipboardWrite(ctx); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: append(steps, "clipboard-first: wait before app switch canceled"), Err: err}
	}
	steps = append(steps, "clipboard-first: waited before app switch")
	if _, err := t.callQuickAction(ctx, "app_switch", platform); err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: append(steps, "clipboard-first: return to prior app failed"), Err: err}
	}
	steps = append(steps, "clipboard-first: opened app switcher")
	returnCalls, err := t.returnToPreviousApp(ctx, engine)
	vlmCalls += returnCalls
	if err != nil {
		return textViaBridgeResult{Attempted: true, VLMCalls: vlmCalls, Steps: append(steps, "clipboard-first: select previous app failed"), Err: err}
	}
	steps = append(steps, "clipboard-first: returned to prior app")
	result := t.focusPasteVerify(ctx, engine, platform, args)
	result.Attempted = true
	result.VLMCalls += vlmCalls
	result.Steps = append(steps, result.Steps...)
	return result
}

func (t *EnterTextViaBridgeTool) canUseClipboardFirst(platform string, text string) bool {
	bridge := t.currentBridge()
	if bridge == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		return bridge.ClipboardRecentlyContains(text, preparedClipboardMaxAge)
	case "android":
		return t.canWriteClipboardPreservingTarget(platform)
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
			result.Steps = append(result.Steps, "clipboard-first focus failed: "+err.Error())
			result.Err = err
			return result
		}
		result.Steps = append(result.Steps, fmt.Sprintf("clipboard-first: focused field (attempt %d)", attempt))
		pasteMethod, fallbackReason, err := t.pasteClipboard(ctx, platform)
		if err != nil {
			result.Steps = append(result.Steps, "clipboard-first paste failed")
			result.Err = err
			return result
		}
		if fallbackReason != "" {
			result.Steps = append(result.Steps, "clipboard-first: quick_action paste failed, used keyboard fallback: "+fallbackReason)
		}
		result.Steps = append(result.Steps, fmt.Sprintf("clipboard-first: %s-pasted clipboard (attempt %d)", pasteMethod, attempt))
		if err := t.sleepAfterPaste(ctx); err != nil {
			result.Steps = append(result.Steps, "clipboard-first: wait after paste canceled")
			result.Err = err
			return result
		}
		analysis, calls, analyzeSteps, err := engine.analyzeScreen(ctx, platform, args, nil)
		result.VLMCalls += calls
		result.Steps = append(result.Steps, analyzeSteps...)
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
				result.Steps = append(result.Steps, sendSteps...)
				result.Err = err
			}
			return result
		} else {
			result.Steps = append(result.Steps, fmt.Sprintf("clipboard-first: field verify failed after paste attempt %d", attempt))
			result.FieldText = analysis.FieldText
			if strings.TrimSpace(analysis.FieldText) != "" {
				result.Steps = append(result.Steps, "clipboard-first: not retrying paste because field already contains unverified text")
				return result
			}
		}
	}
	return result
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
	_, err := callTextInputTool(ctx, t.hw.keyboardTap, jsonString(map[string]any{"keys": keys}))
	return err
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
	searchTerm := textViaBridgeSearchTerm(platform, bridge.Status())
	openResult, err := runAppSearchOpenFlow(ctx, appSearchOpenFlowConfig{
		hw:               t.hw,
		vision:           t.vision,
		platform:         platform,
		searchTerm:       searchTerm,
		findAppTapFn:     t.findAppTapFn,
		confirmAppOpenFn: t.confirmAppOpenFn,
		entryTool:        &EnterTextInFieldTool{engine: newTextInputEngineWithSleep(*t.hw, t.vision, t.sleep), platformFn: t.platformFn},
		launchDelay:      appSearchOpenLaunchDelay,
		sleep:            t.sleep,
	})
	vlmCalls += openResult.VLMCalls
	for _, step := range openResult.Steps {
		steps = append(steps, "clipboard-first: "+step)
	}
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
	raw, err := modelVision.visionJSON(ctx, prompt, shot)
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
	clipOut, clipErr := clipboard.Call(ctx, jsonString(map[string]any{"action": "write", "text": text}))
	if clipErr != nil {
		return clipErr
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
