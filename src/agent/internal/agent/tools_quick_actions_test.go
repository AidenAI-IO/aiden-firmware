package agent

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestBundledQuickActionsPathUsesOEMPartition(t *testing.T) {
	if BundledQuickActionsPath != "/oem/usr/share/aiden/quick_actions.json" {
		t.Fatalf("BundledQuickActionsPath = %q, want OEM path", BundledQuickActionsPath)
	}
}

func TestQuickActionsResolveAliasAndPlatform(t *testing.T) {
	table := newQuickActionsTable()
	if _, ok := table.resolveActionID("返回"); !ok {
		t.Fatal("expected Chinese alias to resolve")
	}
	if _, ok := table.resolveActionID("copy"); !ok {
		t.Fatal("expected action id to resolve")
	}
	if id, ok := table.resolveActionID("go back"); !ok || id != "back" {
		t.Fatalf("expected natural phrase to resolve to back, got %q ok=%v", id, ok)
	}
	if id, ok := table.resolveActionID("browser back"); !ok || id != "browser_back" {
		t.Fatalf("expected spaced action to resolve to browser_back, got %q ok=%v", id, ok)
	}
	if id, ok := table.resolveActionID("quick-switch-left"); !ok || id != "quick_app_switch_left" {
		t.Fatalf("expected hyphenated alias to resolve to quick_app_switch_left, got %q ok=%v", id, ok)
	}
	if id, ok := table.resolveActionID("switch_previous_app"); !ok || id != "app_switch_back" {
		t.Fatalf("expected previous-app alias to resolve to app_switch_back, got %q ok=%v", id, ok)
	}
	_, binding, ok := table.lookup("app_switch_back", "android")
	if !ok || binding.Status != quickActionStatusActive || binding.Tool != "touch_gesture" {
		t.Fatalf("Android app_switch_back binding = %#v, found=%v; want active touch_gesture", binding, ok)
	}
	if id, ok := table.resolveActionID("退格"); !ok || id != "delete_backward" {
		t.Fatalf("expected delete-backward alias to resolve to delete_backward, got %q ok=%v", id, ok)
	}

	platform, err := normalizeQuickActionPlatform("iPadOS")
	if err != nil || platform != "ios" {
		t.Fatalf("expected ios platform, got %q err=%v", platform, err)
	}
}

func TestQuickActionExposesStructuredSchema(t *testing.T) {
	schema := (&QuickActionTool{}).ArgsSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties: %#v", schema)
	}
	if props["action"] == nil || props["platform"] == nil || props["list"] == nil {
		t.Fatalf("quick_action schema missing expected fields: %#v", props)
	}
	platform := props["platform"].(map[string]any)
	if platform["type"] != "string" {
		t.Fatalf("platform type = %#v, want string", platform["type"])
	}
	// quick_action exposes only the actions defined in quick_actions.json; it
	// carries no transport for bridge-invented capabilities such as open_url.
	if props["url"] != nil {
		t.Fatalf("quick_action schema must not carry a url field: %#v", props)
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "action" || required[1] != "platform" {
		t.Fatalf("required = %#v, want action and platform", schema["required"])
	}
}

func TestQuickActionsSuggestUnknownAction(t *testing.T) {
	table := newQuickActionsTable()
	suggestions := table.suggestActionIDs("go browser backward", 3)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions for related unknown action")
	}
	joined := strings.Join(suggestions, ",")
	if !strings.Contains(joined, "browser_back") && !strings.Contains(joined, "back") {
		t.Fatalf("expected back-related suggestions, got %v", suggestions)
	}
}

func TestQuickActionListForPlatform(t *testing.T) {
	tool := &QuickActionTool{}
	out, err := tool.Call(context.Background(), `{"list":true,"platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var payload struct {
		OK      bool `json:"ok"`
		Actions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK {
		t.Fatalf("expected ok list response: %s", out)
	}
	foundBack := false
	for _, action := range payload.Actions {
		if action.ID == "back" && action.Status == quickActionStatusActive {
			foundBack = true
			break
		}
	}
	if !foundBack {
		t.Fatalf("expected active back action in list: %s", out)
	}
}

func TestQuickActionListActionAlias(t *testing.T) {
	tool := &QuickActionTool{}
	out, err := tool.Call(context.Background(), `{"action":"list","platform":"android"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var payload struct {
		OK      bool `json:"ok"`
		Actions []struct {
			ID string `json:"id"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !payload.OK || len(payload.Actions) == 0 {
		t.Fatalf("expected list response, got %s", out)
	}
}

func TestQuickActionDescriptionDocumentsListInspection(t *testing.T) {
	desc := (&QuickActionTool{}).Description()
	if !strings.Contains(desc, `{"action":"list","platform":"android"}`) {
		t.Fatalf("description missing action=list inspection example: %s", desc)
	}
	if !strings.Contains(desc, `Always pass action and platform`) {
		t.Fatalf("description missing required action/platform guidance: %s", desc)
	}
	// The reserved/alternative/no-retry behavior playbook now lives in the
	// device-operator skill, not the tool description.
}

func TestQuickActionDoesNotExposeScreenshotFull(t *testing.T) {
	table := newQuickActionsTable()
	if id, ok := table.resolveActionID("screenshot_full"); ok {
		t.Fatalf("screenshot_full resolved to %q", id)
	}
	if strings.Contains((&QuickActionTool{}).Description(), "screenshot_full") {
		t.Fatal("description should not mention screenshot_full")
	}
}

// TestQuickActionPlaybookLivesInSkill guards the backstop for the reserved/
// alternative/no-retry guidance trimmed out of the tool description: it must
// remain documented in the device-operator skill so the agent can still recall
// it via skill_read.
func TestQuickActionPlaybookLivesInSkill(t *testing.T) {
	skillPath := filepath.Join("..", "..", "config", "skills", "device-operator", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Skipf("device-operator SKILL.md not readable from test cwd: %v", err)
	}
	content := string(data)
	for _, want := range []string{"status=reserved", "alternative=true", "Never loop on the same binding"} {
		if !strings.Contains(content, want) {
			t.Fatalf("device-operator SKILL.md missing quick_action guidance %q", want)
		}
	}
}

func TestQuickActionReservedBinding(t *testing.T) {
	tool := &QuickActionTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"action":"app_drawer","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeQuickActionReserved {
		t.Fatalf("expected quick_action_reserved; got %+v", te)
	}
	if te.Category != CategoryUnsupported {
		t.Errorf("Category = %q, want %q", te.Category, CategoryUnsupported)
	}
	if out != te.Message {
		t.Errorf("Output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestQuickActionExecutesDelegatedTouchGesture(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, path := newTestHIDDevice(t)
	tool := &QuickActionTool{
		touch: &TouchGestureTool{
			pc:     testPointerController(dev, &pointerState{}),
			screen: &screen.ScreenState{},
		},
	}
	out, err := tool.Call(context.Background(), `{"action":"back","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !quickActionResultOK(t, out) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(readMouseReports(t, dev, path)) == 0 {
		t.Fatal("expected delegated touch gesture writes")
	}
}

func TestQuickActionExecutesDelegatedKeyboardTap(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, path := newTestHIDDevice(t)
	tool := &QuickActionTool{
		keyboard: &KeyboardTapTool{dev: dev},
	}
	out, err := tool.Call(context.Background(), `{"action":"copy","platform":"mac"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !quickActionResultOK(t, out) {
		t.Fatalf("unexpected output: %s", out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Size() < 16 {
		t.Fatalf("expected keyboard press/release writes, got %d bytes", info.Size())
	}
}

func TestQuickActionSpotlightSearchClearsSearchField(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, path := newTestHIDDevice(t)
	tool := &QuickActionTool{
		keyboard: &KeyboardTapTool{dev: dev},
	}
	out, err := tool.Call(context.Background(), `{"action":"spotlight_search","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !quickActionResultOK(t, out) {
		t.Fatalf("unexpected output: %s", out)
	}

	reports := readKeyboardReports(t, dev, path)
	if got, want := len(reports), 10; got != want {
		t.Fatalf("keyboard reports = %d, want %d (Cmd+Space, two Cmd+A/Backspace clear passes): %v", got, want, reports)
	}
	assertKeyboardReport(t, reports[0], hidModifierMap["meta"], hidKeyboardMap["space"], "Cmd+Space")
	assertReleaseReport(t, reports[1], "release after Cmd+Space")
	assertKeyboardReport(t, reports[2], hidModifierMap["meta"], hidKeyboardMap["a"], "first Cmd+A")
	assertReleaseReport(t, reports[3], "release after first Cmd+A")
	assertKeyboardReport(t, reports[4], 0, hidKeyboardMap["backspace"], "first Backspace")
	assertReleaseReport(t, reports[5], "release after first Backspace")
	assertKeyboardReport(t, reports[6], hidModifierMap["meta"], hidKeyboardMap["a"], "second Cmd+A")
	assertReleaseReport(t, reports[7], "release after second Cmd+A")
	assertKeyboardReport(t, reports[8], 0, hidKeyboardMap["backspace"], "second Backspace")
	assertReleaseReport(t, reports[9], "release after second Backspace")
}

func TestQuickActionSpotlightSearchBatchesIOSModifierIsolation(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, _ := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	tool := &QuickActionTool{
		keyboard:             &KeyboardTapTool{dev: dev, iosKeyboardIsolation: controller},
		iosKeyboardIsolation: controller,
	}

	out, err := tool.Call(context.Background(), `{"action":"spotlight_search","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !quickActionResultOK(t, out) {
		t.Fatalf("unexpected output: %s", out)
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("profile events = %v, want %v", events, want)
	}
}

func TestQuickActionRestoresIOSPointerWhenCanceledMidSequence(t *testing.T) {
	skipHIDSleeps(t)

	previousSleep := sleepQuickActionDelay
	sleepQuickActionDelay = func(context.Context, int) error { return context.Canceled }
	t.Cleanup(func() { sleepQuickActionDelay = previousSleep })

	dev, _ := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	tool := &QuickActionTool{
		keyboard:             &KeyboardTapTool{dev: dev, iosKeyboardIsolation: controller},
		iosKeyboardIsolation: controller,
	}

	_, err := tool.Call(context.Background(), `{"action":"spotlight_search","platform":"ios"}`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call error = %v, want context canceled", err)
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("profile events = %v, want %v", events, want)
	}
}

func readKeyboardReports(t *testing.T, dev *HIDDevice, path string) [][]byte {
	t.Helper()

	dev.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data)%8 != 0 {
		t.Fatalf("keyboard report data length = %d, want multiple of 8", len(data))
	}
	reports := make([][]byte, 0, len(data)/8)
	for i := 0; i < len(data); i += 8 {
		reports = append(reports, data[i:i+8])
	}
	return reports
}

func assertKeyboardReport(t *testing.T, report []byte, modifier, key uint8, label string) {
	t.Helper()
	if report[0] != modifier || report[2] != key {
		t.Fatalf("%s report = %v, want modifier 0x%02x key 0x%02x", label, report, modifier, key)
	}
}

func assertReleaseReport(t *testing.T, report []byte, label string) {
	t.Helper()
	for _, b := range report {
		if b != 0 {
			t.Fatalf("%s = %v, want all zeros", label, report)
		}
	}
}

func TestQuickActionDeleteBackwardUsesBackspace(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, path := newTestHIDDevice(t)
	tool := &QuickActionTool{
		keyboard: &KeyboardTapTool{dev: dev},
	}
	out, err := tool.Call(context.Background(), `{"action":"delete_backward","platform":"mac"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if !quickActionResultOK(t, out) {
		t.Fatalf("unexpected output: %s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 16 {
		t.Fatalf("report bytes = %d, want 16 (backspace + release)", len(data))
	}
	if data[2] != hidKeyboardMap["backspace"] {
		t.Fatalf("delete_backward key = 0x%02x, want backspace 0x%02x", data[2], hidKeyboardMap["backspace"])
	}
}

func TestQuickActionAlternativeBinding(t *testing.T) {
	skipHIDSleeps(t)
	skipQuickActionDelays(t)

	dev, _ := newTestHIDDevice(t)
	tool := &QuickActionTool{
		keyboard: &KeyboardTapTool{dev: dev},
		touch: &TouchGestureTool{
			pc:     testPointerController(dev, &pointerState{}),
			screen: &screen.ScreenState{},
		},
	}
	out, err := tool.Call(context.Background(), `{"action":"back","platform":"android","alternative":true}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["binding"] != "alternative_1" {
		t.Fatalf("expected alternative binding, got %s", out)
	}
}

func TestQuickActionUnknownAction(t *testing.T) {
	tool := &QuickActionTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"action":"browser backward","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeQuickActionUnknown {
		t.Fatalf("expected quick_action_unknown; got %+v", te)
	}
	if !strings.Contains(te.Message, "unknown action") {
		t.Fatalf("unexpected message: %s", te.Message)
	}
	if !strings.Contains(te.Message, "suggested actions") {
		t.Fatalf("expected suggested actions in message, got %s", te.Message)
	}
	if out != te.Message {
		t.Errorf("Output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

// In environment-bridge mode quick_action is forwarded verbatim (see
// shouldForwardToEnvironmentBridge) rather than resolved against the local
// quick_actions.json bindings, so the action the model picked must reach the
// bridge untouched.
func TestQuickActionForwardsInputToEnvironmentBridge(t *testing.T) {
	input := `{"action":"app_switch","platform":"ios"}`

	var gotBody string
	bridge := NewEnvironmentBridgeClient("http://bridge.local")
	bridge.httpClient = &http.Client{Transport: bridgeCancelRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		gotBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"output":"ok"}`)),
		}, nil
	})}

	specs := NewToolSpecs([]langtools.Tool{&QuickActionTool{}})
	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: "quick_action", ToolInput: input},
		EnvironmentBridge:      bridge,
		EnvironmentBridgeTools: []string{"quick_action"},
	})
	if result.Error != nil {
		t.Fatalf("unexpected execution error: %v", result.Error)
	}

	var forwarded struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(gotBody), &forwarded); err != nil {
		t.Fatalf("bridge request body is not JSON (%q): %v", gotBody, err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(forwarded.Input), &args); err != nil {
		t.Fatalf("forwarded tool input is not JSON (%q): %v", forwarded.Input, err)
	}
	if args["action"] != "app_switch" || args["platform"] != "ios" {
		t.Fatalf("action/platform lost in forwarding: %s", forwarded.Input)
	}
}

func quickActionResultOK(t *testing.T, out string) bool {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return payload["ok"] == true
}
