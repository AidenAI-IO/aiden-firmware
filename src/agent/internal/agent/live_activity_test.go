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
	if state.Progress <= 0 || !state.CanStop || state.Phase != LiveActivityPhasePlanning || state.CurrentAction != "plan" {
		t.Fatalf("unexpected initial state: %#v", state)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:        runEventToolCall,
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
		Type:      runEventToolCall,
		ToolName:  "open_app",
		ToolInput: `{"app":"Maps"}`,
		Timestamp: time.Now(),
	})
	if state == nil || state.CurrentStep != "Opening Maps" || state.CurrentApp != "Maps" || state.Phase != LiveActivityPhasePhoneBridge {
		t.Fatalf("open_app step = %#v, want targeted app step and current app", state)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      runEventToolCall,
		ToolName:  "screenshot",
		Timestamp: time.Now(),
	})
	if state == nil || state.CurrentStep != "Checking the screen" {
		t.Fatalf("screenshot step = %#v, want Checking the screen", state)
	}
}

func TestLiveActivityManagerNeedsAppWhenBridgeUnavailable(t *testing.T) {
	manager := NewLiveActivityManager(LiveActivityConfig{}, nil)
	manager.StartTask("req-1", "Read clipboard")

	state := manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      runEventToolCall,
		ToolName:  "clipboard",
		ToolInput: `{"action":"read"}`,
		Timestamp: time.Now(),
	})
	if state == nil || state.Status != LiveActivityStatusRunning || state.Phase != LiveActivityPhasePhoneBridge || !state.RequiresApp {
		t.Fatalf("clipboard call state = %#v, want phone bridge state requiring app", state)
	}
	if state.CurrentStep != "Reading clipboard" {
		t.Fatalf("clipboard call step = %q, want Reading clipboard", state.CurrentStep)
	}

	state = manager.UpdateFromRunEvent("req-1", RunEvent{
		Type:      "tool_result",
		ToolName:  "clipboard",
		ToolInput: `{"action":"read"}`,
		Content:   `{"ok":false,"error":"phone bridge not connected"}`,
		Timestamp: time.Now(),
	})
	if state == nil || state.Status != LiveActivityStatusNeedsApp || state.Phase != LiveActivityPhaseWaitingApp || !state.RequiresApp {
		t.Fatalf("bridge unavailable state = %#v, want needs_app", state)
	}
	if state.CurrentStep != "Open Aiden to continue" || state.ShowsProgress {
		t.Fatalf("bridge unavailable display = %#v, want open Aiden without progress", state)
	}
	content := state.ContentState()
	if content.Status != LiveActivityStatusNeedsApp || content.Phase != LiveActivityPhaseWaitingApp || !content.RequiresApp {
		t.Fatalf("content state = %#v, want needs_app fields", content)
	}
}

func TestLiveActivityManagerSnapshotActive(t *testing.T) {
	manager := NewLiveActivityManager(LiveActivityConfig{}, nil)
	manager.StartTask("req-1", "First task")
	manager.StartTask("req-2", "Second task")

	active := manager.SnapshotActive()
	if active == nil || active.RequestID != "req-2" {
		t.Fatalf("active state = %#v, want req-2", active)
	}

	manager.CompleteTask("req-2", "Done")
	active = manager.SnapshotActive()
	if active == nil || active.RequestID != "req-2" || active.Status != LiveActivityStatusCompleted {
		t.Fatalf("active terminal state = %#v, want completed req-2", active)
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

	statusReq := httptest.NewRequest(http.MethodGet, "/api/live-activity/status?request_id=%20req-1%20", nil)
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

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/live-activity/registrations", nil)
	deleteRec := httptest.NewRecorder()
	server.handleLiveActivityRegistrations(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusBadRequest {
		t.Fatalf("delete status code = %d body=%s, want 400", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestServerLiveActivityCurrent(t *testing.T) {
	server := &Server{liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil)}
	server.liveActivity.StartTask("req-1", "Do a task")

	req := httptest.NewRequest(http.MethodGet, "/api/live-activity/current", nil)
	rec := httptest.NewRecorder()
	server.handleLiveActivityCurrent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status       string            `json:"status"`
		LiveActivity LiveActivityState `json:"live_activity"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode current response: %v", err)
	}
	if resp.Status != "ok" || resp.LiveActivity.RequestID != "req-1" || resp.LiveActivity.Phase != LiveActivityPhasePlanning {
		t.Fatalf("unexpected current response: %#v", resp)
	}
}

func TestServerLiveActivityCurrentEmpty(t *testing.T) {
	server := &Server{liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil)}

	req := httptest.NewRequest(http.MethodGet, "/api/live-activity/current", nil)
	rec := httptest.NewRecorder()
	server.handleLiveActivityCurrent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode current empty response: %v", err)
	}
	if resp.Status != "not_found" {
		t.Fatalf("unexpected current empty response: %#v", resp)
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

func TestChatResultErrorIncludesQueuedMessages(t *testing.T) {
	server := &Server{
		liveActivity: NewLiveActivityManager(LiveActivityConfig{}, nil),
		pendingResults: map[string]*chatPendingResult{
			"req-1": {
				messages: []Message{{
					Type:      "episode_status",
					RequestID: "req-1",
					Content:   `{"status":"failed"}`,
					Timestamp: time.Now(),
				}},
				err: "boom",
			},
		},
	}
	server.liveActivity.StartTask("req-1", "Do a task")
	server.liveActivity.FailTask("req-1", "boom")

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
	if resp.Status != "error" || resp.Error != "boom" || len(resp.Messages) != 1 {
		t.Fatalf("unexpected error chat result: %#v", resp)
	}
}
