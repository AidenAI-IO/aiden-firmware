package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func newTestPhoneBridge(t *testing.T) *PhoneBridge {
	t.Helper()
	pb := NewPhoneBridge(nil)
	t.Cleanup(pb.queue.Stop)
	return pb
}

func TestEnterTextViaBridgeWriteClipboardIsolatesNestedToolError(t *testing.T) {
	pb := newTestPhoneBridge(t)
	tool := &EnterTextViaBridgeTool{}
	ctx, _ := WithToolError(context.Background())

	err := tool.writeClipboard(ctx, pb, "小红书")
	if err == nil {
		t.Fatal("expected clipboard write to fail when phone bridge is disconnected")
	}
	if !strings.Contains(err.Error(), "phone bridge not connected") {
		t.Fatalf("error = %v, want phone bridge not connected", err)
	}
	if got := ToolErrorFromContext(ctx); got != nil {
		t.Fatalf("parent ToolError = %+v, want nil after nested clipboard isolation", got)
	}
}

func TestEnterTextViaBridgeUsesClipboardPathAndVerifiesField(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "hello world",
	}}}
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	pb := newTestPhoneBridge(t)
	pb.connected = true
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   mouse,
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			return previousAppCardResult{Found: true, TapPoint: focusPointArgs{X: 180, Y: 290, CoordSpace: "normalized"}, Label: "Settings"}, nil
		},
		clipboardWriteFn: func(_ context.Context, _ *PhoneBridge, text string) error {
			if text != "hello world" {
				t.Fatalf("clipboard text = %q", text)
			}
			return nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(mouse.calls) != 1 {
		t.Fatalf("mouse_click calls=%v", mouse.calls)
	}
	if len(touch.calls) != 0 {
		t.Fatalf("touch_gesture calls=%v, want no app switching on Android target-preserving clipboard path", touch.calls)
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if !strings.Contains(out, "clipboard-first: wrote clipboard without leaving target app") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextViaBridgeRestoresBridgeAppWhenDisconnected(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "hello world",
	}}}
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.returnEntry = "dynamic_island"
	pb.returnEntrySeen = true
	pb.returnEntryOK = true
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	status := PhoneEnvironment{AvailableApps: []AvailableAppInfo{{Name: "Aiden Bridge", AndroidPackage: "com.qing.aidenbridgedaily"}}}
	pb.environment = &status
	quickAction := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quickAction,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden Bridge"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			pb.connected = true
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			return previousAppCardResult{Found: true, TapPoint: focusPointArgs{X: 180, Y: 290, CoordSpace: "normalized"}, Label: "Settings"}, nil
		},
		clipboardWriteFn: func(_ context.Context, bridge *PhoneBridge, text string) error {
			if text != "hello world" {
				t.Fatalf("clipboard text = %q", text)
			}
			return nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"ios","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(keyboardText.calls) != 1 {
		t.Fatalf("keyboard_text calls=%v", keyboardText.calls)
	}
	if !strings.Contains(keyboardText.calls[0], `"text":`) {
		t.Fatalf("keyboard_text calls=%v", keyboardText.calls)
	}
	if len(touch.calls) != 2 {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
	if !strings.Contains(touch.calls[0], `"type": "tap"`) || !strings.Contains(touch.calls[0], `"coord_space": "normalized"`) || !strings.Contains(touch.calls[1], `"type": "tap"`) {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
	if len(quickAction.calls) != 3 {
		t.Fatalf("quick_action calls=%v", quickAction.calls)
	}
	if !strings.Contains(quickAction.calls[0], `"action": "spotlight_search"`) || !strings.Contains(quickAction.calls[1], `"action": "app_switch"`) || !strings.Contains(quickAction.calls[2], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quickAction.calls)
	}
	if len(keyboardText.calls) == 1 && len(quickAction.calls) == 3 {
		if strings.Index(quickAction.calls[1], `"action": "app_switch"`) > strings.Index(quickAction.calls[2], `"action": "paste"`) {
			t.Fatalf("expected app_switch before paste: %v", quickAction.calls)
		}
	}
	if !pb.connected {
		t.Fatal("expected bridge connected after restore flow")
	}
}

func TestEnterTextViaBridgeRetriesOpeningBridgeAppBeforeFailing(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "hello world",
	}}}
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.returnEntry = "dynamic_island"
	pb.returnEntrySeen = true
	pb.returnEntryOK = true
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	quickAction := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	confirmCalls := 0
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quickAction,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden Bridge"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			confirmCalls++
			return bridgeAppOpenResult{Opened: false, Reason: "Still on search results page"}, nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"ios","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, `Still on search results page`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if confirmCalls != 2 {
		t.Fatalf("confirm app open calls=%d", confirmCalls)
	}
	if len(quickAction.calls) != 1 {
		t.Fatalf("quick_action calls=%v", quickAction.calls)
	}
	if len(keyboardText.calls) != 1 {
		t.Fatalf("keyboard_text calls=%v", keyboardText.calls)
	}
	if len(touch.calls) != 2 {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
}

func TestEnterTextViaBridgeSwipesRecentsWhenPreviousAppCardNotInitiallyVisible(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "hello world",
	}}}
	pb := newTestPhoneBridge(t)
	pb.connected = true
	pb.platform = "ios"
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	quickAction := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	findPrevCalls := 0
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quickAction,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			findPrevCalls++
			if findPrevCalls == 1 {
				return previousAppCardResult{Found: false, TapPoint: focusPointArgs{CoordSpace: "normalized"}}, nil
			}
			return previousAppCardResult{Found: true, TapPoint: focusPointArgs{X: 180, Y: 290, CoordSpace: "normalized"}, Label: "Settings"}, nil
		},
		clipboardWriteFn: func(_ context.Context, _ *PhoneBridge, text string) error {
			if text != "hello world" {
				t.Fatalf("clipboard text = %q", text)
			}
			return nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"ios","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if findPrevCalls != 2 {
		t.Fatalf("find previous app calls=%d", findPrevCalls)
	}
	if len(touch.calls) != 3 {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
	if !strings.Contains(touch.calls[1], `"type": "swipe_right"`) {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
}

func TestEnterTextViaBridgeCountsBridgeVisionCalls(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse(`{"observed_mode":"ascii","field_text":"hello world","composition_pending":false,"wrong_ime_suspected":false,"suggest_switch_ime":false,"candidates":[],"evidence":["verified"]}`),
	}}
	resolver := &testModelResolver{model: model}
	pb := newTestPhoneBridge(t)
	pb.connected = true
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`},
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:           newLLMTextInputVision(resolver),
		bridgeFn:         func() *PhoneBridge { return pb },
		sleep:            testNoWaitSleep,
		clipboardWriteFn: func(context.Context, *PhoneBridge, string) error { return nil },
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"vlm_calls": 1`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count=%d", model.callCount)
	}
}

func TestEnterTextViaBridgeReturnsLastObservedFieldTextAfterFailedPasteAttempts(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "hello",
	}, {
		ObservedMode: textInputModeASCII,
		FieldText:    "hello wor",
	}}}
	pb := newTestPhoneBridge(t)
	pb.connected = true
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`},
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			return previousAppCardResult{Found: true, TapPoint: focusPointArgs{X: 180, Y: 290, CoordSpace: "normalized"}, Label: "Settings"}, nil
		},
		clipboardWriteFn: func(context.Context, *PhoneBridge, string) error { return nil },
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, `"field_text": "hello"`) || !strings.Contains(out, "not retrying paste because field already contains unverified text") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextViaBridgeCanPasteSendAndVerify(t *testing.T) {
	message := "Example Contact number 555-0101 and 555-0102 still active?"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    message,
	}, {
		ObservedMode: textInputModeASCII,
		FieldText:    "",
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
			keyboardTap:  keyboardTap,
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}
	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"},"send_after_commit":true}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ok": true`,
		`"committed": true`,
		`"sent": true`,
		`"send_verified": true`,
		"clipboard-first: quick_action-pasted clipboard",
		"clipboard-first: keyboard send submitted",
		"clipboard-first: send verified by cleared/changed input field",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if len(keyboardTap.calls) != 1 || !strings.Contains(keyboardTap.calls[0], `"enter"`) {
		t.Fatalf("keyboard_tap calls=%v, want send only", keyboardTap.calls)
	}
}

func TestEnterTextViaBridgeFallsBackToKeyboardPasteWhenQuickActionFails(t *testing.T) {
	message := "Example Contact number 555-0101 still active?"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":false,"status":"reserved","message":"paste unavailable"}`}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
			keyboardTap:  keyboardTap,
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}
	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"committed": true`,
		"clipboard-first: quick_action paste failed, used keyboard fallback: paste unavailable",
		"clipboard-first: keyboard-pasted clipboard",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if len(keyboardTap.calls) != 1 || !strings.Contains(keyboardTap.calls[0], `"meta"`) || !strings.Contains(keyboardTap.calls[0], `"v"`) {
		t.Fatalf("keyboard_tap calls=%v", keyboardTap.calls)
	}
}

type recordingBridgeQuickActionTool struct {
	*recordingTextInputTool
	onCall func(string)
}

func (s *recordingBridgeQuickActionTool) Call(ctx context.Context, input string) (string, error) {
	if s.onCall != nil {
		s.onCall(input)
	}
	return s.recordingTextInputTool.Call(ctx, input)
}
