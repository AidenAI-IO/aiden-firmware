package agent

import (
	"encoding/json"
	"testing"
)

func TestPhoneBridgeHandlesEnvironmentEvent(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()

	bridge.mu.Lock()
	bridge.connected = true
	bridge.platform = "ios"
	bridge.pendingCmds["phone_environment"] = make(chan BridgeCommandResponse, 1)
	bridge.mu.Unlock()

	handled := bridge.handleAppEvent(BridgeCommandResponse{
		ID:     "phone_environment",
		OK:     true,
		Method: "phone_environment",
		Data: json.RawMessage(`{
			"captured_at":"2026-06-01T02:03:05Z",
			"system_name":"iOS",
			"system_version":"18.5",
			"locale":"zh-Hans-CN",
			"system_apps":[
				{"name":"Camera","available":true,"category":"system","availability_source":"builtin"}
			],
			"third_party_apps":[
				{"name":"WeChat","available":true,"category":"third_party","availability_source":"can_open_url"},
				{"name":"Douyin","available":false,"category":"third_party","availability_source":"can_open_url"}
			],
			"available_apps":[
				{"name":"WeChat","available":true},
				{"name":"Douyin","available":false}
			]
		}`),
	})
	if !handled {
		t.Fatal("environment event was not handled")
	}

	status := bridge.Status()
	if status.Environment == nil {
		t.Fatal("expected environment in status")
	}
	if status.Environment.Platform != "ios" {
		t.Fatalf("environment platform = %q, want ios", status.Environment.Platform)
	}
	if status.Environment.SystemName != "iOS" {
		t.Fatalf("environment system_name = %q, want iOS", status.Environment.SystemName)
	}
	if got := len(status.Environment.SystemApps); got != 1 {
		t.Fatalf("system app count = %d, want 1", got)
	}
	if got := len(status.Environment.ThirdPartyApps); got != 2 {
		t.Fatalf("third-party app count = %d, want 2", got)
	}
	if status.EnvironmentUpdatedAt == nil {
		t.Fatal("expected environment_updated_at")
	}

	bridge.mu.Lock()
	_, pendingStillExists := bridge.pendingCmds["phone_environment"]
	bridge.mu.Unlock()
	if !pendingStillExists {
		t.Fatal("environment event should not consume pending command responses")
	}
}

func TestPhoneBridgeHandlesAppStateEvent(t *testing.T) {
	bridge := NewPhoneBridge(nil)
	defer bridge.queue.Stop()

	handled := bridge.handleAppEvent(BridgeCommandResponse{
		ID:     "phone_app_state",
		OK:     true,
		Method: "phone_app_state",
		Data:   json.RawMessage(`{"app_state":"background","reported_at":"2026-06-01T02:03:06Z"}`),
	})
	if !handled {
		t.Fatal("app state event was not handled")
	}

	status := bridge.Status()
	if status.AppState != "background" {
		t.Fatalf("app_state = %q, want background", status.AppState)
	}
	if status.AppStateUpdatedAt == nil {
		t.Fatal("expected app_state_updated_at")
	}
}
