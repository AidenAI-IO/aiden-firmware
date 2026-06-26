package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestEnterTextViaBridgeUsesDynamicIslandClipboardWriteForBackgroundIOS(t *testing.T) {
	message := "这两个号码13204503813和18846189806还在用吗"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeComposition,
		FieldText:    message,
	}}}
	pb := NewPhoneBridge(nil)
	defer pb.queue.Stop()
	pb.connected = true
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	pb.returnEntry = "dynamic_island"
	pb.returnEntrySeen = true
	pb.returnEntryOK = true

	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	restorer := NewPhoneBridgeRestorer(pb, nil)
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		pb.appState = "active"
		return nil
	}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		restorer: restorer,
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			return previousAppCardResult{Found: true, TapPoint: focusPointArgs{X: 180, Y: 290, CoordSpace: "normalized"}, Label: "WeChat"}, nil
		},
		clipboardWriteFn: func(_ context.Context, _ *PhoneBridge, text string) error {
			if text != message {
				t.Fatalf("clipboard text = %q", text)
			}
			return nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"committed": true`,
		"clipboard-first: restored Aiden via Dynamic Island",
		"clipboard-first: wrote clipboard in bridge app",
		"clipboard-first: returned to prior app",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 2 ||
		!strings.Contains(quick.calls[0], `"action": "app_switch"`) ||
		!strings.Contains(quick.calls[1], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if len(touch.calls) != 1 {
		t.Fatalf("expected one tap to return to previous app: %v", touch.calls)
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("Dynamic Island path should not type app search text: %v", keyboardText.calls)
	}
}

func TestEnterTextViaBridgeDoesNotTrustBackgroundIOSQueueWithoutReturnEntry(t *testing.T) {
	message := "hello from clipboard"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    message,
	}}}
	pb := NewPhoneBridge(nil)
	defer pb.queue.Stop()
	pb.connected = false
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()

	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
	}
	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, "reliable bridge clipboard path unavailable") {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(quick.calls) != 0 || len(touch.calls) != 0 {
		t.Fatalf("background iOS queue path should not be used: quick=%v touch=%v", quick.calls, touch.calls)
	}
}

func TestEnterTextViaBridgeRefusesUnsafeRestoreAndAppSwitcherReturn(t *testing.T) {
	pb := NewPhoneBridge(nil)
	defer pb.queue.Stop()
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   &stubTextInputVision{},
		bridgeFn: func() *PhoneBridge { return pb },
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			t.Fatal("must not search for Aiden when target cannot be preserved")
			return bridgeSearchResult{}, nil
		},
		findPrevAppFn: func(context.Context, screenshotResult) (previousAppCardResult, error) {
			t.Fatal("must not inspect app switcher when target cannot be preserved")
			return previousAppCardResult{}, nil
		},
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"ios","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, "reliable bridge clipboard path unavailable") {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(keyboardText.calls) != 0 || len(touch.calls) != 0 || len(quick.calls) != 0 {
		t.Fatalf("unsafe bridge path should not invoke navigation/input tools: keyboard=%v touch=%v quick=%v", keyboardText.calls, touch.calls, quick.calls)
	}
}

func TestEnterTextViaBridgeCountsTargetPreservingVisionCalls(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse(`{"observed_mode":"ascii","field_text":"hello world","composition_pending":false,"wrong_ime_suspected":false,"suggest_switch_ime":false,"candidates":[],"evidence":["verified"]}`),
	}}
	resolver := &testModelResolver{model: model}
	pb := NewPhoneBridge(nil)
	defer pb.queue.Stop()
	pb.connected = true
	pb.platform = "android"
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
		clipboardWriteFn: func(context.Context, *PhoneBridge, string) error { return nil },
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"android","focus":{"x":500,"y":100}}`)
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
	pb := NewPhoneBridge(nil)
	defer pb.queue.Stop()
	pb.connected = true
	pb.platform = "android"
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`},
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:           vision,
		bridgeFn:         func() *PhoneBridge { return pb },
		clipboardWriteFn: func(context.Context, *PhoneBridge, string) error { return nil },
	}
	out, err := tool.Call(context.Background(), `{"text":"hello world","platform":"android","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, `"field_text": "hello wor"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestBridgeClipboardPreservesTargetOnlyForConnectedAndroid(t *testing.T) {
	now := time.Now()
	if bridgeClipboardPreservesTargetAt("ios", PhoneBridgeStatus{
		Platform:          "ios",
		AppState:          "background",
		AppStateUpdatedAt: &now,
	}, now) {
		t.Fatal("iOS background queue must not be treated as target-preserving")
	}
	if bridgeClipboardPreservesTargetAt("ios", PhoneBridgeStatus{
		Connected: true,
		Platform:  "ios",
		AppState:  "active",
	}, now) {
		t.Fatal("foreground Aiden does not preserve the external target app")
	}
	if !bridgeClipboardPreservesTargetAt("android", PhoneBridgeStatus{
		Connected: true,
		Platform:  "android",
	}, now) {
		t.Fatal("connected Android bridge should preserve the target app")
	}
	if bridgeClipboardPreservesTargetAt("android", PhoneBridgeStatus{
		Connected: false,
		Platform:  "android",
	}, now) {
		t.Fatal("disconnected Android bridge should not preserve the target app")
	}
}
