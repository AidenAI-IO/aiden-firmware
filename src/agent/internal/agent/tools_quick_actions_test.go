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

	platform, err := normalizeQuickActionPlatform("iPadOS")
	if err != nil || platform != "ios" {
		t.Fatalf("expected ios platform, got %q err=%v", platform, err)
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
	out, err := tool.Call(context.Background(), `{"action":"does_not_exist","platform":"ios"}`)
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
}

func quickActionResultOK(t *testing.T, out string) bool {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return payload["ok"] == true
}
