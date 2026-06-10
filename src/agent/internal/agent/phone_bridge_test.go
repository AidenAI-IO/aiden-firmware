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
	if got := len(status.Environment.AvailableApps); got != 2 {
		t.Fatalf("available app count = %d, want 2", got)
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
