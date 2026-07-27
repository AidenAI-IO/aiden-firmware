package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func newPhoneBridgeForTest() *PhoneBridge {
	return NewPhoneBridge(newTestLogger())
}

func newTestPhoneBridge(t *testing.T) *PhoneBridge {
	t.Helper()
	pb := newPhoneBridgeForTest()
	t.Cleanup(func() {
		pb.queue.Stop()
	})
	return pb
}

func waitForQueuedBridgeCommand(queue *CommandQueue, platform string, timeout time.Duration) (BridgeCommand, error) {
	deadline := time.Now().Add(timeout)
	for {
		commands := queue.PollForPhone(platform, "", 10)
		if len(commands) > 0 {
			if len(commands) != 1 {
				return BridgeCommand{}, fmt.Errorf("expected one queued command, got %d", len(commands))
			}
			return commands[0], nil
		}
		if time.Now().After(deadline) {
			return BridgeCommand{}, fmt.Errorf("timed out waiting for queued %s command", platform)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEnterTextViaBridgeDescriptionDocumentsChatClipboardPath(t *testing.T) {
	desc := (&EnterTextViaBridgeTool{}).Description()
	for _, want := range []string{
		"final chat/message composer",
		"latest screenshot",
		"actual editable field",
		"folder/list view",
		"create/new button",
		"guessed blank space",
		"runtime Phone Bridge status",
		"CJK",
		"iOS PiP queue",
		"Android connected/FGS bridge",
		"do not call bridge_clipboard first",
		"long-pressing the field",
		"Paste/粘贴",
		"wait_for_stable_screen once",
		"Preserve the current field while evidence conflicts",
		"corrective input only after the fresh observation identifies a concrete mismatch",
		"search terms",
		"contact lookup",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	props, _ := (&EnterTextViaBridgeTool{}).ArgsSchema()["properties"].(map[string]any)
	focusSchema, _ := props["focus"].(map[string]any)
	if focusDesc, _ := focusSchema["description"].(string); !strings.Contains(focusDesc, "actual editable field") || !strings.Contains(focusDesc, "blank space") {
		t.Fatalf("focus schema missing input-readiness guard:\n%v", focusSchema)
	}
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

func TestEnterTextViaBridgeUsesPiPBackgroundClipboardQueue(t *testing.T) {
	message := "桥接输入测试"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeComposition,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	pb.pipBridgeEnabled = true
	pb.pipBridgeSeen = true
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   mouse,
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
			keyboardText: keyboardText,
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}

	queueResult := make(chan string, 1)
	go func() {
		command, err := waitForQueuedBridgeCommand(pb.queue, "ios", 500*time.Millisecond)
		if err != nil {
			queueResult <- err.Error()
			return
		}
		if command.Type != "clipboard_write" {
			queueResult <- "unexpected queued command type: " + command.Type
			return
		}
		if !strings.Contains(string(command.Payload), message) {
			queueResult <- "queued clipboard payload missing target text: " + string(command.Payload)
			return
		}
		if err := pb.queue.SubmitResult(BridgeCommandResponse{ID: command.ID, Method: "queued"}); err != nil {
			queueResult <- "submit queued clipboard result failed: " + err.Error()
			return
		}
		queueResult <- ""
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := tool.Call(ctx, `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if queueErr := <-queueResult; queueErr != "" {
		t.Fatal(queueErr)
	}
	for _, want := range []string{
		`"committed": true`,
		"clipboard-first: wrote clipboard through background bridge queue",
		"clipboard-first: quick_action-pasted clipboard",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if len(mouse.calls) != 1 {
		t.Fatalf("mouse_click calls=%v", mouse.calls)
	}
	if len(touch.calls) != 0 {
		t.Fatalf("touch_gesture calls=%v, want no app switching in PiP background path", touch.calls)
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("keyboard_text calls=%v, want no bridge app search in PiP background path", keyboardText.calls)
	}
}

func TestEnterTextViaBridgeRejectsStalePiPBackgroundClipboardState(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second)
	pb.pipBridgeEnabled = true
	pb.pipBridgeSeen = true

	tool := &EnterTextViaBridgeTool{bridgeFn: func() *PhoneBridge { return pb }}
	result := tool.runClipboardFirstFlow(context.Background(), "ios", enterTextInFieldArgs{Text: "过期状态不应写入"})
	if result.Attempted || result.Err != nil {
		t.Fatalf("stale PiP state result = %+v, want unavailable without attempt", result)
	}
	if commands := pb.queue.PollForPhone("ios", "", 10); len(commands) != 0 {
		t.Fatalf("stale PiP state queued commands = %+v, want none", commands)
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

func TestEnterTextViaBridgeFallsBackToLongPressPasteMenuWhenShortcutHasNoEffect(t *testing.T) {
	message := "桥接粘贴测试"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeComposition,
		FieldText:    "",
	}, {
		ObservedMode: textInputModeComposition,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  keyboardTap,
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  quick,
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findPasteMenuFn: func(context.Context, screenshotResult, string) (pasteMenuResult, error) {
			return pasteMenuResult{
				Found:    true,
				TapPoint: focusPointArgs{X: 430, Y: 720, CoordSpace: "normalized"},
				Label:    "粘贴",
			}, nil
		},
	}

	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":300,"y":940,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"committed": true`,
		"shortcut paste had no visible effect",
		"long-pressed focused field",
		`context menu action \"粘贴\"`,
		"context-menu-pasted clipboard",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 1 {
		t.Fatalf("quick_action calls=%v, want one shortcut attempt", quick.calls)
	}
	if len(keyboardTap.calls) != 0 {
		t.Fatalf("keyboard_tap calls=%v, want no keyboard fallback after quick_action returned ok", keyboardTap.calls)
	}
	if len(touch.calls) != 2 || !strings.Contains(touch.calls[0], `"type": "long_press"`) || !strings.Contains(touch.calls[1], `"type": "tap"`) {
		t.Fatalf("touch_gesture calls=%v, want long_press then paste-menu tap", touch.calls)
	}
}

func TestEnterTextViaBridgeObservesFieldBeforeFallbackAfterShortcutError(t *testing.T) {
	message := "已由快捷键粘贴"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeComposition,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	tool := &EnterTextViaBridgeTool{
		hw: &textInputHardwareDeps{
			mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
			touchGesture: touch,
			keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: `{"ok":false,"message":"keyboard report unavailable"}`},
			keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
			quickAction:  &recordingTextInputTool{name: "quick_action", out: `{"ok":false,"message":"post-action capture failed"}`},
			screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		},
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
		findPasteMenuFn: func(context.Context, screenshotResult, string) (pasteMenuResult, error) {
			t.Fatal("paste menu lookup must not run when fresh observation shows the target text")
			return pasteMenuResult{}, nil
		},
	}

	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":300,"y":940,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"committed": true`,
		"shortcut paste reported an error; observing field before fallback",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(touch.calls) != 0 {
		t.Fatalf("touch_gesture calls=%v, want no long-press fallback after observed success", touch.calls)
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
