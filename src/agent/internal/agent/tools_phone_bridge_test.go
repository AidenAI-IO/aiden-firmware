package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
	"nhooyr.io/websocket"
)

type iosIsolationPointerTestTool struct {
	controller *iosKeyboardIsolationController
	events     *[]string
}

func (t iosIsolationPointerTestTool) Name() string        { return "touch_gesture" }
func (t iosIsolationPointerTestTool) Description() string { return "touch_gesture" }
func (t iosIsolationPointerTestTool) Call(ctx context.Context, _ string) (string, error) {
	return t.controller.withPointerCall(ctx, func(context.Context) (string, error) {
		*t.events = append(*t.events, "pointer")
		return "ok", nil
	})
}

func TestSearchLaunchAppDescriptionRequiresFollowUpNavigation(t *testing.T) {
	desc := (&appSearchOpenTool{}).Description()
	for _, want := range []string{"target app is visibly opened", "does not mean", "editor", "input field is ready", "create/open/navigation step"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("search_launch_app description missing %q: %s", want, desc)
		}
	}
}

func TestResolveOpenAppTargetsAppAliasStaysSemantic(t *testing.T) {
	args := openAppArgs{App: " weixin "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "weixin" {
		t.Fatalf("app = %q, want semantic alias", args.App)
	}
	if args.URL != "" {
		t.Fatalf("url = %q, want empty", args.URL)
	}
}

func TestResolveOpenAppTargetsURL(t *testing.T) {
	args := openAppArgs{URL: " https://example.com/path?q=1 "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.URL != "https://example.com/path?q=1" {
		t.Fatalf("url = %q, want trimmed URL", args.URL)
	}
}

func TestResolveOpenAppTargetsRejectsURLInApp(t *testing.T) {
	args := openAppArgs{App: "https://example.org"}

	if err := resolveOpenAppTargets(&args); err == nil {
		t.Fatal("resolveOpenAppTargets returned nil error, want URL-in-app rejected")
	}
}

func TestResolveOpenAppTargetsPhoneNumber(t *testing.T) {
	args := openAppArgs{PhoneNumber: " 10086 "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.PhoneNumber != "10086" {
		t.Fatalf("phone_number = %q, want trimmed phone number", args.PhoneNumber)
	}
}

func TestResolveOpenAppTargetsRejectsInvalidCombinations(t *testing.T) {
	tests := []openAppArgs{
		{},
		{App: "  "},
		{URL: "not-a-url"},
		{App: "微信", URL: "https://example.com"},
		{App: "微信", PhoneNumber: "10086"},
		{App: "phone", PhoneNumber: "10086"},
		{URL: "https://example.com", PhoneNumber: "10086"},
	}

	for _, args := range tests {
		if err := resolveOpenAppTargets(&args); err == nil {
			t.Fatalf("resolveOpenAppTargets(%#v) returned nil error, want error", args)
		}
	}
}

func TestOpenAppResultMetadataForApp(t *testing.T) {
	args := openAppArgs{App: "微信"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "open_app" {
		t.Fatalf("method = %q, want open_app", got)
	}
	if got := openAppResultTarget(args); got != "微信" {
		t.Fatalf("target = %q, want app alias", got)
	}
	if got := openAppResultMechanism(args, "ios_url_scheme"); got != "ios_url_scheme" {
		t.Fatalf("mechanism = %q, want app-side launch method", got)
	}
}

func TestOpenAppResultMetadataForURL(t *testing.T) {
	args := openAppArgs{URL: "https://example.com"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "open_url" {
		t.Fatalf("method = %q, want open_url", got)
	}
	if got := openAppResultTarget(args); got != "https://example.com" {
		t.Fatalf("target = %q, want requested URL", got)
	}
	if got := openAppResultMechanism(args, "open_url"); got != "open_url" {
		t.Fatalf("mechanism = %q, want open_url", got)
	}
}

func TestOpenAppResultMetadataForDial(t *testing.T) {
	args := openAppArgs{PhoneNumber: "10086"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "dial" {
		t.Fatalf("method = %q, want dial", got)
	}
	if got := openAppResultTarget(args); got != "10086" {
		t.Fatalf("target = %q, want phone number", got)
	}
	if got := openAppResultMechanism(args, "dial"); got != "dial" {
		t.Fatalf("mechanism = %q, want dial", got)
	}
}

func TestSearchOpenAppToolAvailable(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	if _, ok := runtime.tools.Get("search_launch_app"); !ok {
		t.Fatal("expected search_launch_app tool to be registered")
	}
}

func TestAppSearchOpenFlowCanBeReused(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: textInputStubTool{name: "keyboard_tap", out: "ok"}}
	hw.pointerMode = "touchscreen"
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextTool{engine: newFastTextInputEngine(*hw, vision)}
	called := 0
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden",
		entryTool:  entryTool,
		sleep:      testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			called++
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 200, CoordSpace: "normalized"}, Label: "Aiden"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Opened {
		t.Fatalf("expected opened result, got %#v", result)
	}
	if called != 1 {
		t.Fatalf("findAppTapFn calls=%d", called)
	}
	if len(quick.calls) != 1 || len(keyboardText.calls) != 2 || len(touch.calls) != 1 {
		t.Fatalf("unexpected tool calls: quick=%v keyboard=%v touch=%v", quick.calls, keyboardText.calls, touch.calls)
	}
}

func TestSearchLaunchAppUsesRuntimeAndroidPlatformBeforeIOSFallback(t *testing.T) {
	vision := &stubTextInputVision{}
	quick := &recordingTextInputTool{name: "quick_action", out: "ok"}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	hw := &textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		quickAction:  quick,
		touchGesture: touch,
		keyboardText: keyboardText,
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	tool := &appSearchOpenTool{
		hw:                   hw,
		vision:               vision,
		entryTool:            &EnterTextTool{engine: newFastTextInputEngine(*hw, vision)},
		sleep:                testNoWaitSleep,
		iosKeyboardIsolation: newTestIOSKeyboardIsolationController(&[]string{}),
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 200, CoordSpace: "normalized"}, Label: "WeChat"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "WeChat visible"}, nil
		},
	}
	toolSet := &ToolSet{tools: map[string]langtools.Tool{"search_launch_app": tool}}
	toolSet.SetRuntimePlatformFn(func() string { return "android" })

	out, err := tool.Call(context.Background(), `{"app":"WeChat"}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode Call() output: %v: %s", err, out)
	}
	if !result.OK {
		t.Fatalf("Call() output = %s, want ok", out)
	}
	if len(quick.calls) != 1 {
		t.Fatalf("quick_action calls = %v, want one", quick.calls)
	}
	var quickArgs quickActionArgs
	if err := json.Unmarshal([]byte(quick.calls[0]), &quickArgs); err != nil {
		t.Fatalf("decode quick_action input: %v", err)
	}
	if quickArgs.Platform != "android" {
		t.Fatalf("quick_action platform = %q, want android", quickArgs.Platform)
	}
}

func TestSearchLaunchAppBatchesIOSModifierIsolationAcrossSubtools(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	keyboardDev, _ := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = keyboardDev
	keyboardTap := &KeyboardTapTool{dev: keyboardDev, iosKeyboardIsolation: controller}
	keyboardText := &KeyboardTextTool{dev: keyboardDev, iosKeyboardIsolation: controller}
	quickAction := &QuickActionTool{
		keyboard:             keyboardTap,
		iosKeyboardIsolation: controller,
	}
	vision := &stubTextInputVision{}
	hw := &textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		touchGesture: iosIsolationPointerTestTool{controller: controller, events: &events},
		keyboardTap:  keyboardTap,
		keyboardText: keyboardText,
		quickAction:  quickAction,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	entryTool := &EnterTextTool{
		engine:               newFastTextInputEngine(*hw, vision),
		iosKeyboardIsolation: controller,
	}
	tool := &appSearchOpenTool{
		hw:                   hw,
		vision:               vision,
		entryTool:            entryTool,
		sleep:                testNoWaitSleep,
		iosKeyboardIsolation: controller,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 200, CoordSpace: "normalized"}, Label: "WeChat"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "WeChat visible"}, nil
		},
	}

	out, err := tool.Call(context.Background(), `{"app":"WeChat"}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("Call() output is not JSON: %v: %s", err, out)
	}
	if !result.OK {
		t.Fatalf("Call() output = %s, want ok", out)
	}
	if want := []string{"isolate", "restore", "pointer"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("profile events = %v, want %v", events, want)
	}
}

func TestAppSearchOpenFlowFallsBackToShorterTerm(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}, {ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: kbTap}
	hw.pointerMode = "touchscreen"
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextTool{engine: newFastTextInputEngine(*hw, vision)}
	terms := []string{}
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden Bridge",
		entryTool:  entryTool,
		sleep:      testNoWaitSleep,
		findAppTapFn: func(_ context.Context, _ screenshotResult, term string) (bridgeSearchResult, error) {
			terms = append(terms, term)
			if term == "Aiden" {
				return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden"}, nil
			}
			return bridgeSearchResult{Found: false, TapPoint: focusPointArgs{CoordSpace: "normalized"}}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Opened {
		t.Fatalf("expected opened result, got %#v", result)
	}
	if len(terms) < 3 || terms[0] != "Aiden Bridge" || terms[1] != "Aiden Bridge" || terms[2] != "Aiden" {
		t.Fatalf("unexpected search terms: %#v", terms)
	}
	if len(keyboardText.calls) < 2 {
		t.Fatalf("expected two search entry attempts, got keyboard_text calls=%v", keyboardText.calls)
	}
}

func TestAppSearchOpenFlowRechecksSameTermBeforeFallback(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: kbTap}
	hw.pointerMode = "touchscreen"
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextTool{engine: newFastTextInputEngine(*hw, vision)}
	terms := []string{}
	findCalls := 0
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden Bridge",
		entryTool:  entryTool,
		sleep:      testNoWaitSleep,
		findAppTapFn: func(_ context.Context, _ screenshotResult, term string) (bridgeSearchResult, error) {
			terms = append(terms, term)
			if term == "Aiden Bridge" {
				findCalls++
				if findCalls == 1 {
					return bridgeSearchResult{Found: false, TapPoint: focusPointArgs{CoordSpace: "normalized"}}, nil
				}
				return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden Bridge"}, nil
			}
			return bridgeSearchResult{Found: false, TapPoint: focusPointArgs{CoordSpace: "normalized"}}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Opened {
		t.Fatalf("expected opened result, got %#v", result)
	}
	if len(terms) != 2 || terms[0] != "Aiden Bridge" || terms[1] != "Aiden Bridge" {
		t.Fatalf("expected same-term recheck before fallback, got terms=%#v", terms)
	}
	if len(keyboardText.calls) != 3 {
		t.Fatalf("expected probe followed by the two ASCII query parts, got keyboard_text calls=%v", keyboardText.calls)
	}
	if len(touch.calls) != 1 {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
}

func TestOpenAppMissingArgsReturnsInvalidArguments(t *testing.T) {
	tool := &OpenAppTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil {
		t.Fatalf("expected structured ToolError on context; got nil")
	}
	if te.Code != CodeInvalidArguments {
		t.Errorf("Code = %q want %q", te.Code, CodeInvalidArguments)
	}
	if out != te.Message {
		t.Errorf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestOpenAppNameFieldIsNotAccepted(t *testing.T) {
	tool := &OpenAppTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"name":"微信"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil {
		t.Fatalf("expected structured ToolError on context; got nil")
	}
	if te.Code != CodeInvalidArguments {
		t.Errorf("Code = %q want %q", te.Code, CodeInvalidArguments)
	}
	if out != te.Message {
		t.Errorf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestOpenAppDisconnectedGuidesUIFallbackBeforeHandoff(t *testing.T) {
	tool := &OpenAppTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"app":"小红书"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeBridgeNotConnected {
		t.Fatalf("expected bridge_not_connected; got %+v", te)
	}
	for _, want := range []string{"call screenshot first", "search_launch_app", "request_human_handoff only after"} {
		if !strings.Contains(out, want) {
			t.Fatalf("disconnected output missing %q: %s", want, out)
		}
	}
	if got := te.Details["fallback"]; got != phoneBridgeOpenAppRecoveryGuidance(PhoneBridgeStatus{}) {
		t.Fatalf("fallback detail = %q, want ordered recovery guidance", got)
	}
}

func TestOpenAppDisconnectedAndroidDoesNotMentionDynamicIsland(t *testing.T) {
	bridge := newPhoneBridgeForTest()
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.platform = "android"
	bridge.mu.Unlock()
	tool := NewOpenAppTool(bridge, nil)

	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"app":"微信"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeBridgeNotConnected {
		t.Fatalf("expected bridge_not_connected; got %+v", te)
	}
	if strings.Contains(out, "Dynamic Island") {
		t.Fatalf("Android disconnected output should not mention Dynamic Island: %s", out)
	}
	for _, want := range []string{"Android", "Aiden open in the foreground", "search_launch_app"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Android disconnected output missing %q: %s", want, out)
		}
	}
	if fallback, _ := te.Details["fallback"].(string); strings.Contains(fallback, "Dynamic Island") {
		t.Fatalf("Android fallback should not mention Dynamic Island: %s", fallback)
	}
}

func TestOpenAppConnectedAndroidBackgroundRequiresForeground(t *testing.T) {
	sent := false
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		sent = true
		return BridgeCommandResponse{ID: cmd.ID}
	})
	bridge.mu.Lock()
	bridge.platform = "android"
	bridge.appState = "background"
	bridge.appStateAt = time.Now()
	bridge.mu.Unlock()
	tool := NewOpenAppTool(bridge, nil)

	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"app":"微信"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	if sent {
		t.Fatal("background Android open_app was sent over Phone Bridge")
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeBridgeNotConnected {
		t.Fatalf("expected bridge_not_connected; got %+v", te)
	}
	if strings.Contains(out, "Dynamic Island") {
		t.Fatalf("Android background output should not mention Dynamic Island: %s", out)
	}
	for _, want := range []string{"Android", "foreground", "search_launch_app"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Android background output missing %q: %s", want, out)
		}
	}
}

func TestResolveOpenAppTargetsUnknownAppStaysSemantic(t *testing.T) {
	args := openAppArgs{App: "NoSuchApp12345"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "NoSuchApp12345" {
		t.Fatalf("app = %q, want semantic alias", args.App)
	}
	if args.URL != "" {
		t.Fatalf("url = %q, want empty", args.URL)
	}
}

func TestContactsInvalidInputReturnsStructuredError(t *testing.T) {
	tool := NewContactsTool(nil, nil)
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{bad json`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("expected invalid_arguments; got %+v", te)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestNotificationInvalidInputReturnsStructuredError(t *testing.T) {
	tool := NewNotificationTool(nil, nil)
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{bad json`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("expected invalid_arguments; got %+v", te)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestContactsUpdatePropagatesAppToolError(t *testing.T) {
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodePermissionDenied, "contacts permission denied"),
		}
	})
	tool := NewContactsTool(bridge, nil)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"action":"update","contact_id":"abc","name":"Alice"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodePermissionDenied {
		t.Fatalf("expected permission_denied; got %+v output=%q", te, out)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestClipboardReadSuccessPreservesOKField(t *testing.T) {
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		return BridgeCommandResponse{
			ID:   cmd.ID,
			Data: json.RawMessage(`{"text":"hello"}`),
		}
	})
	tool := NewClipboardTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	var payload struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if !payload.OK || payload.Text != "hello" {
		t.Fatalf("clipboard success payload = %+v, raw=%s; want ok=true text=hello", payload, out)
	}
}

func TestClipboardReadUsesPiPBackgroundQueueWhenActive(t *testing.T) {
	bridge := newPhoneBridgeForTest()
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.appStateAt = time.Now()
	bridge.pipBridgeEnabled = true
	bridge.pipBridgeSeen = true
	bridge.mu.Unlock()

	go func() {
		time.Sleep(10 * time.Millisecond)
		commands := bridge.queue.PollForPhone("ios", "", 10)
		if len(commands) != 1 {
			t.Errorf("expected one background command, got %d", len(commands))
			return
		}
		if commands[0].Type != "clipboard_read" {
			t.Errorf("background command type = %q, want clipboard_read", commands[0].Type)
			return
		}
		if err := bridge.queue.SubmitResult(BridgeCommandResponse{
			ID:   commands[0].ID,
			Data: json.RawMessage(`{"text":"queued"}`),
		}); err != nil {
			t.Errorf("SubmitResult() error = %v", err)
		}
	}()

	tool := NewClipboardTool(bridge, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := tool.Call(ctx, `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	var payload struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if !payload.OK || payload.Text != "queued" {
		t.Fatalf("clipboard queue payload = %+v, raw=%s; want ok=true text=queued", payload, out)
	}
}

func newTestPhoneBridgeWithApp(t *testing.T, handle func(BridgeCommand) BridgeCommandResponse) *PhoneBridge {
	t.Helper()
	bridge := newPhoneBridgeForTest()
	t.Cleanup(func() { bridge.queue.Stop() })

	server := httptest.NewServer(http.HandlerFunc(bridge.HandleWebSocket))
	t.Cleanup(server.Close)

	dialURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?platform=android"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, dialURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	go func() {
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var cmd BridgeCommand
			if err := json.Unmarshal(data, &cmd); err != nil {
				return
			}
			resp := handle(cmd)
			if resp.ID == "" {
				resp.ID = cmd.ID
			}
			payload, err := json.Marshal(resp)
			if err != nil {
				return
			}
			if err := conn.Write(context.Background(), websocket.MessageText, payload); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bridge.getStatus().Connected {
			return bridge
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("phone bridge did not connect")
	return nil
}
