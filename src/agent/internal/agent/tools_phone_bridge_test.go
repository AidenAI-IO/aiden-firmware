package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

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
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextInFieldTool{engine: newTextInputEngine(*hw, vision)}
	called := 0
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden",
		entryTool:  entryTool,
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
	if len(quick.calls) != 1 || len(keyboardText.calls) != 1 || len(touch.calls) != 1 {
		t.Fatalf("unexpected tool calls: quick=%v keyboard=%v touch=%v", quick.calls, keyboardText.calls, touch.calls)
	}
}

func TestAppSearchOpenFlowFallsBackToShorterTerm(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}, {ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: kbTap}
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextInFieldTool{engine: newTextInputEngine(*hw, vision)}
	terms := []string{}
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden Bridge",
		entryTool:  entryTool,
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
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextInFieldTool{engine: newTextInputEngine(*hw, vision)}
	terms := []string{}
	findCalls := 0
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:         hw,
		vision:     vision,
		platform:   "android",
		searchTerm: "Aiden Bridge",
		entryTool:  entryTool,
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
	if len(keyboardText.calls) != 2 {
		t.Fatalf("expected single spaced query entry, got keyboard_text calls=%v", keyboardText.calls)
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

func TestPreparePhoneAppWorkflowBatchesDirectActionsClipboardAndOpen(t *testing.T) {
	var mu sync.Mutex
	var commandTypes []string
	var clipboardText string
	var openedApp string
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		mu.Lock()
		commandTypes = append(commandTypes, cmd.Type)
		mu.Unlock()
		switch cmd.Type {
		case "calendar_query":
			return BridgeCommandResponse{ID: cmd.ID, Data: json.RawMessage(`{"events":[{"event_id":"e1","title":"项目跟进","start_at":"2026-06-30T10:00:00+08:00","notes":"确认报价和交付时间"}]}`)}
		case "clipboard_write":
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(cmd.Payload, &payload)
			mu.Lock()
			clipboardText = payload.Text
			mu.Unlock()
			return BridgeCommandResponse{ID: cmd.ID}
		case "open_app":
			mu.Lock()
			openedApp = cmd.App
			mu.Unlock()
			return BridgeCommandResponse{ID: cmd.ID, Method: "ios_url_scheme"}
		default:
			return BridgeCommandResponse{ID: cmd.ID, Error: NewToolError(CodeToolExecutionFailed, "unexpected command "+cmd.Type)}
		}
	})
	tool := NewPreparePhoneAppWorkflowTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"app_side_actions":[{"id":"calendar_lookup","tool":"calendar","action":"query","payload":{"from":"2026-06-30T00:00:00+08:00","to":"2026-07-01T00:00:00+08:00"}}],"target_text_template":"今天日历备注里写的是：{{calendar_lookup.event_notes}}，这件事进展如何？","target_app":"微信","target_label":"李四","open_target_app":true}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	var payload struct {
		OK                bool   `json:"ok"`
		Workflow          string `json:"workflow"`
		TargetText        string `json:"target_text"`
		ClipboardPrepared bool   `json:"clipboard_prepared"`
		OpenedTargetApp   bool   `json:"opened_target_app"`
		OpenMechanism     string `json:"open_mechanism"`
		Actions           []struct {
			ID          string `json:"id"`
			CommandType string `json:"command_type"`
		} `json:"actions"`
		NextToolHint struct {
			Tool            string `json:"tool"`
			SendAfterCommit bool   `json:"send_after_commit"`
		} `json:"next_tool_hint"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !payload.OK || payload.Workflow != "prepare_phone_app_workflow" || !payload.ClipboardPrepared || !payload.OpenedTargetApp {
		t.Fatalf("workflow output = %+v raw=%s", payload, out)
	}
	if !strings.Contains(payload.TargetText, "确认报价和交付时间") {
		t.Fatalf("target_text = %q", payload.TargetText)
	}
	if len(payload.Actions) != 1 || payload.Actions[0].ID != "calendar_lookup" || payload.Actions[0].CommandType != "calendar_query" {
		t.Fatalf("actions = %+v", payload.Actions)
	}
	if payload.OpenMechanism != "ios_url_scheme" {
		t.Fatalf("open_mechanism = %q", payload.OpenMechanism)
	}
	if payload.NextToolHint.Tool != "enter_text_in_field" || !payload.NextToolHint.SendAfterCommit {
		t.Fatalf("next_tool_hint = %+v", payload.NextToolHint)
	}
	mu.Lock()
	gotTypes := append([]string{}, commandTypes...)
	gotClipboard := clipboardText
	gotOpenedApp := openedApp
	mu.Unlock()
	if strings.Join(gotTypes, ",") != "calendar_query,clipboard_write,open_app" {
		t.Fatalf("command order = %v", gotTypes)
	}
	if gotClipboard != payload.TargetText {
		t.Fatalf("clipboard text = %q, want target text %q", gotClipboard, payload.TargetText)
	}
	if gotOpenedApp != "微信" {
		t.Fatalf("opened app = %q", gotOpenedApp)
	}
}

func TestPreparePhoneAppWorkflowRejectsNonStructuredAppSideTool(t *testing.T) {
	var mu sync.Mutex
	var commandTypes []string
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		mu.Lock()
		commandTypes = append(commandTypes, cmd.Type)
		mu.Unlock()
		return BridgeCommandResponse{ID: cmd.ID, Error: NewToolError(CodeToolExecutionFailed, "unexpected command "+cmd.Type)}
	})
	tool := NewPreparePhoneAppWorkflowTool(bridge, nil)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"app_side_actions":[{"id":"x","tool":"app","action":"query","payload":{"name":"Some App"}}],"target_app":"微信","open_target_app":true}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("tool error = %+v output=%s", te, out)
	}
	if !strings.Contains(out, "unsupported app_side_action tool") || !strings.Contains(out, "structured direct PhoneBridge tools") {
		t.Fatalf("unexpected output: %s", out)
	}
	mu.Lock()
	gotTypes := append([]string{}, commandTypes...)
	mu.Unlock()
	if len(gotTypes) != 0 {
		t.Fatalf("commands = %v, want none before structural rejection", gotTypes)
	}
}

func TestPreparePhoneAppWorkflowRejectsOpenAppInsideAppSideActions(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	t.Cleanup(func() { bridge.queue.Stop() })
	tool := NewPreparePhoneAppWorkflowTool(bridge, nil)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"app_side_actions":[{"id":"bad","tool":"open_app","action":"open","payload":{"app":"微信"}}],"target_app":"微信"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("tool error = %+v output=%s", te, out)
	}
	if !strings.Contains(out, "target-app launch is the final workflow boundary") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestPreparePhoneMessageWorkflowBatchesContactsClipboardAndOpen(t *testing.T) {
	var mu sync.Mutex
	var commandTypes []string
	var clipboardText string
	var openedApp string
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		mu.Lock()
		commandTypes = append(commandTypes, cmd.Type)
		mu.Unlock()
		switch cmd.Type {
		case "contacts_query":
			return BridgeCommandResponse{ID: cmd.ID, Data: json.RawMessage(`{"contacts":[{"contact_id":"c1","name":"Example Contact","phone_numbers":["555 0101","5550102"]}]}`)}
		case "clipboard_write":
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(cmd.Payload, &payload)
			mu.Lock()
			clipboardText = payload.Text
			mu.Unlock()
			return BridgeCommandResponse{ID: cmd.ID}
		case "open_app":
			mu.Lock()
			openedApp = cmd.App
			mu.Unlock()
			return BridgeCommandResponse{ID: cmd.ID, Method: "ios_url_scheme"}
		default:
			return BridgeCommandResponse{ID: cmd.ID, Error: NewToolError(CodeToolExecutionFailed, "unexpected command "+cmd.Type)}
		}
	})
	tool := NewPreparePhoneMessageTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"contact_query":"Example Contact","message_template":"Example Contact的手机号是{{phone_numbers}}，这个号码还在用吗？","target_app":"微信","target_label":"Target Friend","open_target_app":true}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	var payload struct {
		OK                bool   `json:"ok"`
		TargetText        string `json:"target_text"`
		ClipboardPrepared bool   `json:"clipboard_prepared"`
		OpenedTargetApp   bool   `json:"opened_target_app"`
		OpenMechanism     string `json:"open_mechanism"`
		NextToolHint      struct {
			Tool            string `json:"tool"`
			SendAfterCommit bool   `json:"send_after_commit"`
		} `json:"next_tool_hint"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !payload.OK || !payload.ClipboardPrepared || !payload.OpenedTargetApp {
		t.Fatalf("workflow output = %+v raw=%s", payload, out)
	}
	for _, want := range []string{"555 0101", "5550102", "还在用吗"} {
		if !strings.Contains(payload.TargetText, want) {
			t.Fatalf("target_text = %q, missing %q", payload.TargetText, want)
		}
	}
	if payload.OpenMechanism != "ios_url_scheme" {
		t.Fatalf("open_mechanism = %q", payload.OpenMechanism)
	}
	if payload.NextToolHint.Tool != "enter_text_in_field" || !payload.NextToolHint.SendAfterCommit {
		t.Fatalf("next_tool_hint = %+v", payload.NextToolHint)
	}
	mu.Lock()
	gotTypes := append([]string{}, commandTypes...)
	gotClipboard := clipboardText
	gotOpenedApp := openedApp
	mu.Unlock()
	if strings.Join(gotTypes, ",") != "contacts_query,clipboard_write,open_app" {
		t.Fatalf("command order = %v", gotTypes)
	}
	if gotClipboard != payload.TargetText {
		t.Fatalf("clipboard text = %q, want target text %q", gotClipboard, payload.TargetText)
	}
	if gotOpenedApp != "微信" {
		t.Fatalf("opened app = %q", gotOpenedApp)
	}
}

func TestPreparePhoneMessageWorkflowRejectsMissingSourcePhoneNumbers(t *testing.T) {
	var mu sync.Mutex
	var commandTypes []string
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		mu.Lock()
		commandTypes = append(commandTypes, cmd.Type)
		mu.Unlock()
		switch cmd.Type {
		case "contacts_query":
			return BridgeCommandResponse{ID: cmd.ID, Data: json.RawMessage(`{"contacts":[{"name":"Example Contact","phone_numbers":["555 0101"]}]}`)}
		default:
			return BridgeCommandResponse{ID: cmd.ID, Error: NewToolError(CodeToolExecutionFailed, "unexpected command "+cmd.Type)}
		}
	})
	tool := NewPreparePhoneMessageTool(bridge, nil)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"contact_query":"Example Contact","message_template":"Example Contact的手机号还在用吗？","target_app":"微信","target_label":"Target Friend","open_target_app":true}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("tool error = %+v output=%s", te, out)
	}
	if !strings.Contains(out, "omits phone number") {
		t.Fatalf("unexpected output: %s", out)
	}
	if got := te.Details["missing_phone_numbers"]; !strings.Contains(fmt.Sprint(got), "555 0101") {
		t.Fatalf("missing_phone_numbers detail = %#v", got)
	}
	mu.Lock()
	gotTypes := append([]string{}, commandTypes...)
	mu.Unlock()
	if strings.Join(gotTypes, ",") != "contacts_query" {
		t.Fatalf("commands = %v, want only contacts_query before rejection", gotTypes)
	}
}

func TestPreparePhoneMessageWorkflowFixedTextWritesClipboardOnly(t *testing.T) {
	var mu sync.Mutex
	var commandTypes []string
	var clipboardText string
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		mu.Lock()
		commandTypes = append(commandTypes, cmd.Type)
		mu.Unlock()
		switch cmd.Type {
		case "clipboard_write":
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(cmd.Payload, &payload)
			mu.Lock()
			clipboardText = payload.Text
			mu.Unlock()
			return BridgeCommandResponse{ID: cmd.ID}
		default:
			return BridgeCommandResponse{ID: cmd.ID, Error: NewToolError(CodeToolExecutionFailed, "unexpected command "+cmd.Type)}
		}
	})
	tool := NewPreparePhoneMessageTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"message_text":"Sample Recipient，你手机号还在用吗？","target_app":"QQ","target_label":"Sample Recipient"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	if !strings.Contains(out, `"ok": true`) || strings.Contains(out, `"opened_target_app": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	mu.Lock()
	gotTypes := append([]string{}, commandTypes...)
	gotClipboard := clipboardText
	mu.Unlock()
	if strings.Join(gotTypes, ",") != "clipboard_write" {
		t.Fatalf("commands = %v, want clipboard only", gotTypes)
	}
	if gotClipboard != "Sample Recipient，你手机号还在用吗？" {
		t.Fatalf("clipboard text = %q", gotClipboard)
	}
}

func newTestPhoneBridgeWithApp(t *testing.T, handle func(BridgeCommand) BridgeCommandResponse) *PhoneBridge {
	t.Helper()
	bridge := NewPhoneBridge(nil)
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
		if bridge.Status().Connected {
			return bridge
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("phone bridge did not connect")
	return nil
}
