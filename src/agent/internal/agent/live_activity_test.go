package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveActivityManagerLifecycle(t *testing.T) {
	manager := NewLiveActivityManager(LiveActivityConfig{}, nil)
	if manager == nil {
		t.Fatal("NewLiveActivityManager() = nil")
	}

	state := manager.StartTask("req-1", "Open Settings and enable Wi-Fi")
	if state == nil || state.Status != LiveActivityStatusRunning {
		t.Fatalf("StartTask() state = %#v", state)
	}
	if state.Progress <= 0 || !state.CanStop {
		t.Fatalf("unexpected initial state: %#v", state)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:        "tool_call",
		ToolName:    "screenshot",
		Description: "Checking the current screen",
		Timestamp:   time.Now(),
	})
	if state == nil || state.LastToolName != "screenshot" || !strings.Contains(state.CurrentStep, "Checking") {
		t.Fatalf("unexpected tool update state: %#v", state)
	}

	state = manager.CompleteTask("req-1", "Done")
	if state == nil || state.Status != LiveActivityStatusCompleted || state.CanStop || state.Progress != 1 {
		t.Fatalf("unexpected completion state: %#v", state)
	}

	content := state.ContentState()
	if content.RequestID != "req-1" || content.Status != LiveActivityStatusCompleted {
		t.Fatalf("unexpected content state: %#v", content)
	}
}

func TestLiveActivityManagerSummarizesAgentSteps(t *testing.T) {
	manager := NewLiveActivityManager(LiveActivityConfig{}, nil)
	manager.StartTask("req-1", "Book a table")

	state := manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      "role_output",
		Role:      "planner",
		Content:   `{"plan":["Open Maps","Search restaurant"],"next_step":"Open Maps"}`,
		Timestamp: time.Now(),
	})
	if state == nil || state.CurrentStep != "Planning: Open Maps" {
		t.Fatalf("planner step = %#v, want Planning: Open Maps", state)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      "tool_call",
		ToolName:  "open_app",
		ToolInput: `{"app":"Maps"}`,
		Timestamp: time.Now(),
	})
	if state == nil || state.CurrentStep != "Opening app" || state.CurrentApp != "Maps" {
		t.Fatalf("open_app step = %#v, want app step and current app", state)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      "tool_call",
		ToolName:  "screenshot",
		Timestamp: time.Now(),
	})
	if state == nil || state.CurrentStep != "Checking the screen" {
		t.Fatalf("screenshot step = %#v, want Checking the screen", state)
	}
}

func TestServerLiveActivityRegistrationAndStatus(t *testing.T) {
	server := &Server{liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil)}
	server.liveActivity.StartTask("req-1", "Do a task")

	body := bytes.NewBufferString(`{"request_id":"req-1","activity_id":"act-1","push_token":"token-1","platform":"ios"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/live-activity/registrations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleLiveActivityRegistrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register status = %d body=%s", rec.Code, rec.Body.String())
	}
	var registerResp LiveActivityRegistrationResponse
	if err := json.NewDecoder(rec.Body).Decode(&registerResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if !registerResp.OK || registerResp.APNs != "not_configured" || registerResp.State == nil {
		t.Fatalf("unexpected register response: %#v", registerResp)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/live-activity/status?request_id=req-1", nil)
	statusRec := httptest.NewRecorder()
	server.handleLiveActivityStatus(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var statusResp struct {
		Status       string            `json:"status"`
		LiveActivity LiveActivityState `json:"live_activity"`
	}
	if err := json.NewDecoder(statusRec.Body).Decode(&statusResp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if statusResp.Status != "ok" || statusResp.LiveActivity.RequestID != "req-1" {
		t.Fatalf("unexpected status response: %#v", statusResp)
	}
}

func TestServerLiveActivityStatusRequiresRequestID(t *testing.T) {
	server := &Server{liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil)}
	req := httptest.NewRequest(http.MethodGet, "/api/live-activity/status", nil)
	rec := httptest.NewRecorder()

	server.handleLiveActivityStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestChatResultIncludesLiveActivityState(t *testing.T) {
	server := &Server{
		liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil),
		pendingResults: map[string]*chatPendingResult{
			"req-1": {
				messages: []Message{},
				history:  []Message{},
			},
		},
	}
	server.liveActivity.StartTask("req-1", "Do a task")

	req := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id=req-1", nil)
	rec := httptest.NewRecorder()
	server.handleChatResult(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResultResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode chat result: %v", err)
	}
	if resp.Status != "running" || resp.LiveActivity == nil || resp.LiveActivity.RequestID != "req-1" {
		t.Fatalf("unexpected chat result: %#v", resp)
	}
}

func TestChatResultIncludesTerminalLiveActivityState(t *testing.T) {
	server := &Server{
		liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil),
		pendingResults: map[string]*chatPendingResult{
			"req-1": {
				messages: []Message{{
					Type:      "assistant",
					RequestID: "req-1",
					Content:   "Done",
					Timestamp: time.Now(),
				}},
				history: []Message{{
					Type:      "assistant",
					RequestID: "req-1",
					Content:   "Done",
					Timestamp: time.Now(),
				}},
				done: true,
			},
		},
	}
	server.liveActivity.StartTask("req-1", "Do a task")
	server.liveActivity.CompleteTask("req-1", "Done")

	req := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id=req-1", nil)
	rec := httptest.NewRecorder()
	server.handleChatResult(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResultResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode chat result: %v", err)
	}
	if resp.Status != "complete" || resp.LiveActivity == nil || resp.LiveActivity.Status != LiveActivityStatusCompleted {
		t.Fatalf("unexpected terminal chat result: %#v", resp)
	}
}
