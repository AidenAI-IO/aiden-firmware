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
	textViaBridgeOpenAttempts   = 2
	textViaBridgePasteAttempts  = 2
	textViaBridgeRecentsSwipes  = 3
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

type EnterTextViaBridgeTool struct {
	hw               *textInputHardwareDeps
	vision           textInputVision
	bridgeFn         func() *PhoneBridge
	clipboardWriteFn func(context.Context, *PhoneBridge, string) error
	findAppTapFn     func(context.Context, screenshotResult, string) (bridgeSearchResult, error)
	confirmAppOpenFn func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error)
	findPrevAppFn    func(context.Context, screenshotResult) (previousAppCardResult, error)
	platformFn       func() string
}

func (t *EnterTextViaBridgeTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *EnterTextViaBridgeTool) Name() string { return "enter_text_via_bridge" }

func (t *EnterTextViaBridgeTool) Description() string {
	return strings.TrimSpace(`Use the Aiden companion app and phone bridge clipboard path to place known text into an input field. ` +
		`One call runs: restore/open the Aiden app if needed, wait for bridge connection, write the clipboard, return to the prior app, refocus the field, paste, and verify the field text. ` +
		`Use this only when the user explicitly asks to use bridge/clipboard/companion-app input instead of normal typing. ` +
		`Returns committed:true only when the exact target text is verified in the input field.`)
}

func (t *EnterTextViaBridgeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":     stringArgSchema("Exact text that must appear in the field when done."),
		"platform": stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"focus":    focusPointArgSchema("Input field coordinates."),
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
	committed, fieldText, vlmCalls, steps, err := t.runBridgeFlow(ctx, platform, args)
	result.OK = committed
	result.Committed = committed
	result.FieldText = fieldText
	result.VLMCalls = vlmCalls
	result.Steps = steps
	if err != nil {
		result.Reason = err.Error()
		return jsonString(result), nil
	}
	if !committed {
		result.Reason = "bridge clipboard input not verified in field"
		return jsonString(result), nil
	}
	result.Reason = "field verified"
	return jsonString(result), nil
}

func (t *EnterTextViaBridgeTool) runBridgeFlow(ctx context.Context, platform string, args enterTextInFieldArgs) (committed bool, fieldText string, vlmCalls int, steps []string, err error) {
	bridge := t.currentBridge()
	if bridge == nil {
		return false, "", 0, nil, fmt.Errorf("phone bridge is not configured")
	}
	engine := newTextInputEngine(*t.hw, t.vision)
	restoreSteps, restoreCalls, restoreErr := t.restoreBridgeAppIfNeeded(ctx, engine, bridge, platform)
	steps = append(steps, restoreSteps...)
	vlmCalls += restoreCalls
	if restoreErr != nil {
		return false, "", vlmCalls, steps, restoreErr
	}
	bridge = t.currentBridge()
	if bridge == nil || !bridge.Connected() {
		return false, "", vlmCalls, steps, fmt.Errorf("phone bridge did not connect")
	}
	if err := t.writeClipboard(ctx, bridge, args.Text); err != nil {
		return false, "", vlmCalls, append(steps, "clipboard write failed"), err
	}
	steps = append(steps, "clipboard-first: wrote clipboard in bridge app")
	time.Sleep(textViaBridgePostWriteDelay)
	steps = append(steps, "clipboard-first: waited before app switch")
	if _, err := t.callQuickAction(ctx, "app_switch", platform); err != nil {
		return false, "", 0, append(steps, "clipboard-first: return to prior app failed"), err
	}
	steps = append(steps, "clipboard-first: opened app switcher")
	returnCalls, err := t.returnToPreviousApp(ctx, engine)
	vlmCalls += returnCalls
	if err != nil {
		return false, "", 0, append(steps, "clipboard-first: select previous app failed"), err
	}
	steps = append(steps, "clipboard-first: returned to prior app")
	committed, fieldText, calls, pasteSteps, err := t.focusPasteVerify(ctx, engine, platform, args)
	vlmCalls += calls
	steps = append(steps, pasteSteps...)
	if err != nil {
		return false, fieldText, vlmCalls, steps, err
	}
	if committed {
		return true, fieldText, vlmCalls, steps, nil
	}
	return false, fieldText, vlmCalls, steps, nil
}

func (t *EnterTextViaBridgeTool) focusPasteVerify(ctx context.Context, engine *textInputEngine, platform string, args enterTextInFieldArgs) (committed bool, fieldText string, vlmCalls int, steps []string, err error) {
	for attempt := 1; attempt <= textViaBridgePasteAttempts; attempt++ {
		if err := engine.applyFocus(ctx, args.Focus); err != nil {
			return false, "", vlmCalls, append(steps, "clipboard-first focus failed: "+err.Error()), err
		}
		steps = append(steps, fmt.Sprintf("clipboard-first: focused field (attempt %d)", attempt))
		qaOut, qaErr := t.hw.quickAction.Call(ctx, jsonString(map[string]any{"action": "paste", "platform": platform}))
		if qaErr != nil {
			return false, "", vlmCalls, steps, qaErr
		}
		if err := interpretTextInputToolOutput(qaOut); err != nil {
			return false, "", vlmCalls, append(steps, "clipboard-first paste failed"), err
		}
		steps = append(steps, fmt.Sprintf("clipboard-first: pasted clipboard (attempt %d)", attempt))
		analysis, calls, analyzeSteps, err := engine.analyzeScreen(ctx, platform, args, nil)
		vlmCalls += calls
		steps = append(steps, analyzeSteps...)
		if err != nil {
			return false, "", vlmCalls, steps, err
		}
		if committed, committedFieldText := evaluateFieldCommit(analysis, args.Text); committed {
			return true, committedFieldText, vlmCalls, steps, nil
		} else {
			steps = append(steps, fmt.Sprintf("clipboard-first: field verify failed after paste attempt %d", attempt))
			fieldText = analysis.FieldText
		}
	}
	return false, fieldText, vlmCalls, steps, nil
}

func (t *EnterTextViaBridgeTool) restoreBridgeAppIfNeeded(ctx context.Context, engine *textInputEngine, bridge *PhoneBridge, platform string) (steps []string, vlmCalls int, err error) {
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
		entryTool:        &EnterTextInFieldTool{engine: newTextInputEngine(*t.hw, t.vision), platformFn: t.platformFn},
		launchDelay:      appSearchOpenLaunchDelay,
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
