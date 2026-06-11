package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

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

	platform, err := normalizeQuickActionPlatform("iPadOS")
	if err != nil || platform != "ios" {
		t.Fatalf("expected ios platform, got %q err=%v", platform, err)
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

func TestQuickActionDescriptionWarnsAgainstActionList(t *testing.T) {
	desc := (&QuickActionTool{}).Description()
	if !strings.Contains(desc, `{"list":true,"platform":"android"}`) {
		t.Fatalf("description missing list=true example: %s", desc)
	}
	if !strings.Contains(desc, `do not pass {"action":"list"}`) {
		t.Fatalf("description missing action=list warning: %s", desc)
	}
}

func TestQuickActionReservedBinding(t *testing.T) {
	tool := &QuickActionTool{}
	out, err := tool.Call(context.Background(), `{"action":"app_drawer","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["ok"] != false || payload["status"] != quickActionStatusReserved {
		t.Fatalf("expected reserved response, got %v", payload)
	}
}

func TestQuickActionExecutesDelegatedTouchGesture(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	tool := &QuickActionTool{
		touch: &TouchGestureTool{
			pc:     testPointerController(dev, &pointerState{}),
			screen: &screenState{},
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

func TestQuickActionAlternativeBinding(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	tool := &QuickActionTool{
		keyboard: &KeyboardTapTool{dev: dev},
		touch: &TouchGestureTool{
			pc:     testPointerController(dev, &pointerState{}),
			screen: &screenState{},
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
	out, err := tool.Call(context.Background(), `{"action":"browser backward","platform":"ios"}`)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %s", out)
	}
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "unknown action") {
		t.Fatalf("unexpected output: %s", out)
	}
	if !strings.Contains(errText, "suggested actions") {
		t.Fatalf("expected suggested actions, got %s", out)
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
