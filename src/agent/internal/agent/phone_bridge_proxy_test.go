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

func TestPhoneBridgeProxyMode(t *testing.T) {
	// Create a mock remote agent
	remoteAgent := newPhoneBridgeForTest()
	defer remoteAgent.queue.Stop()

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/phone-bridge" {
			remoteAgent.HandleWebSocket(w, r)
		} else if r.URL.Path == "/api/phone-bridge/status" {
			w.Header().Set("Content-Type", "application/json")
			status := remoteAgent.getStatus()
			json.NewEncoder(w).Encode(status)
		} else if strings.HasPrefix(r.URL.Path, "/api/phone-bridge/") {
			if r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/commands" {
				remoteAgent.handleEnqueueCommand(w, r)
			} else if r.Method == http.MethodGet && r.URL.Path == "/api/phone-bridge/commands" {
				remoteAgent.handlePollCommands(w, r)
			} else if r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/results" {
				remoteAgent.handleSubmitResult(w, r)
			} else if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/phone-bridge/results/") {
				remoteAgent.handleQueryResult(w, r)
			}
		}
	}))
	defer remoteServer.Close()

	// Create proxy bridge
	proxyBridge := NewPhoneBridgeProxy(remoteServer.URL, "test-task", nil)
	defer proxyBridge.queue.Stop()

	if !proxyBridge.proxyMode {
		t.Fatal("proxy bridge should be in proxy mode")
	}

	// Test getStatus proxies to remote agent
	status := proxyBridge.getStatus()
	if status.Connected {
		t.Fatal("status.Connected should be false when no app is connected")
	}

	// Create proxy server
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/phone-bridge" {
			proxyBridge.HandleWebSocket(w, r)
		} else if r.URL.Path == "/api/phone-bridge/status" {
			w.Header().Set("Content-Type", "application/json")
			status := proxyBridge.getStatus()
			json.NewEncoder(w).Encode(status)
		}
	}))
	defer proxyServer.Close()

	// Connect app to proxy
	appURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/api/phone-bridge?platform=ios&phone_id=test-phone"
	appCtx, appCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer appCancel()

	appConn, _, err := websocket.Dial(appCtx, appURL, nil)
	if err != nil {
		t.Fatalf("app dial failed: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "")

	// Wait a bit for connections to establish
	time.Sleep(100 * time.Millisecond)

	// Send a message from app
	msg := map[string]interface{}{
		"id":     "test-msg",
		"method": "test",
	}
	msgData, _ := json.Marshal(msg)
	if err := appConn.Write(context.Background(), websocket.MessageText, msgData); err != nil {
		t.Fatalf("app write failed: %v", err)
	}

	// The message should be forwarded to remote agent
	// For a full test, we'd need to verify the remote agent received it
	// For now, we just verify the proxy doesn't crash

	time.Sleep(100 * time.Millisecond)
}

func TestPhoneBridgeProxyHTTPQueue(t *testing.T) {
	// Create a mock remote agent
	remoteAgent := newPhoneBridgeForTest()
	defer remoteAgent.queue.Stop()

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/commands" {
			remoteAgent.handleEnqueueCommand(w, r)
		} else if r.Method == http.MethodGet && r.URL.Path == "/api/phone-bridge/commands" {
			remoteAgent.handlePollCommands(w, r)
		} else if r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/results" {
			remoteAgent.handleSubmitResult(w, r)
		} else if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/phone-bridge/results/") {
			remoteAgent.handleQueryResult(w, r)
		}
	}))
	defer remoteServer.Close()

	// Create proxy bridge
	proxyBridge := NewPhoneBridgeProxy(remoteServer.URL, "test-task", nil)
	defer proxyBridge.queue.Stop()

	// Test enqueue command through proxy
	cmdReq := EnqueueCommandRequest{
		Command: BridgeCommand{
			ID:      "test-cmd-1",
			Type:    "open_app",
			App:     "Calendar",
			PhoneID: "test-phone",
		},
	}
	cmdData, _ := json.Marshal(cmdReq)

	req := httptest.NewRequest(http.MethodPost, "/api/phone-bridge/commands", strings.NewReader(string(cmdData)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxyBridge.handleEnqueueCommand(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("enqueue command status = %d, want %d", w.Code, http.StatusAccepted)
	}

	// Test poll commands through proxy
	pollReq := httptest.NewRequest(http.MethodGet, "/api/phone-bridge/commands?platform=ios&phone_id=test-phone", nil)
	pollW := httptest.NewRecorder()

	proxyBridge.handlePollCommands(pollW, pollReq)

	if pollW.Code != http.StatusOK {
		t.Fatalf("poll commands status = %d, want %d", pollW.Code, http.StatusOK)
	}

	var pollResp PollCommandsResponse
	if err := json.NewDecoder(pollW.Body).Decode(&pollResp); err != nil {
		t.Fatalf("decode poll response failed: %v", err)
	}

	if len(pollResp.Commands) != 1 {
		t.Fatalf("poll commands count = %d, want 1", len(pollResp.Commands))
	}

	if pollResp.Commands[0].ID != "test-cmd-1" {
		t.Fatalf("command ID = %q, want test-cmd-1", pollResp.Commands[0].ID)
	}
}

func TestPhoneBridgeProxySendCommandUsesDeviceSideAppConnection(t *testing.T) {
	remoteAgent := newPhoneBridgeForTest()
	defer remoteAgent.queue.Stop()

	var relayTaskID string
	var relayTaskIDMu sync.Mutex
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/phone-bridge" {
			if r.URL.Query().Get(phoneBridgeBenchmarkRelayQuery) == "1" {
				relayTaskIDMu.Lock()
				relayTaskID = r.Header.Get("benchmark-task-id")
				relayTaskIDMu.Unlock()
			}
			remoteAgent.HandleWebSocket(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer remoteServer.Close()

	appURL := "ws" + strings.TrimPrefix(remoteServer.URL, "http") +
		"/api/phone-bridge?platform=ios&phone_id=device-phone"
	appConn, _, err := websocket.Dial(context.Background(), appURL, nil)
	if err != nil {
		t.Fatalf("app dial failed: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "")

	appDone := make(chan error, 1)
	go func() {
		_, data, err := appConn.Read(context.Background())
		if err != nil {
			appDone <- err
			return
		}
		var cmd BridgeCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			appDone <- err
			return
		}
		if cmd.Type != "clipboard_read" {
			appDone <- fmt.Errorf("command type = %q, want clipboard_read", cmd.Type)
			return
		}
		responseData, _ := json.Marshal(BridgeCommandResponse{
			ID:     cmd.ID,
			Method: "clipboard_read",
			Data:   json.RawMessage(`{"text":"from-device-app"}`),
		})
		appDone <- appConn.Write(context.Background(), websocket.MessageText, responseData)
	}()

	proxyBridge := NewPhoneBridgeProxy(remoteServer.URL, "benchmark-task", nil)
	defer proxyBridge.queue.Stop()
	resp, err := proxyBridge.SendCommand(context.Background(), BridgeCommand{
		ID:        "proxy-command",
		Type:      "clipboard_read",
		TimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("SendCommand failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("SendCommand tool error: %+v", resp.Error)
	}
	if resp.ID != "proxy-command" || resp.Method != "clipboard_read" {
		t.Fatalf("response = %+v", resp)
	}
	if string(resp.Data) != `{"text":"from-device-app"}` {
		t.Fatalf("response data = %s", resp.Data)
	}
	relayTaskIDMu.Lock()
	gotRelayTaskID := relayTaskID
	relayTaskIDMu.Unlock()
	if gotRelayTaskID != "benchmark-task" {
		t.Fatalf("relay task id = %q, want benchmark-task", gotRelayTaskID)
	}
	if err := <-appDone; err != nil {
		t.Fatalf("app command loop failed: %v", err)
	}
}

func TestPhoneBridgeProxySendQueuedCommandUsesDeviceSideHTTPQueue(t *testing.T) {
	remoteAgent := newPhoneBridgeForTest()
	defer remoteAgent.queue.Stop()
	remoteAgent.mu.Lock()
	remoteAgent.phoneID = "device-phone"
	remoteAgent.platform = "ios"
	remoteAgent.mu.Unlock()

	var taskIDs []string
	var taskIDsMu sync.Mutex
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskIDsMu.Lock()
		taskIDs = append(taskIDs, r.Header.Get("benchmark-task-id"))
		taskIDsMu.Unlock()
		switch {
		case r.URL.Path == "/api/phone-bridge/status":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(remoteAgent.getStatus())
		case r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/commands":
			remoteAgent.handleEnqueueCommand(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/phone-bridge/commands":
			remoteAgent.handlePollCommands(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/phone-bridge/results":
			remoteAgent.handleSubmitResult(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/phone-bridge/results/"):
			remoteAgent.handleQueryResult(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remoteServer.Close()

	appDone := make(chan error, 1)
	go func() {
		pollURL := remoteServer.URL +
			"/api/phone-bridge/commands?platform=ios&phone_id=device-phone&app_state=background&pip_bridge_enabled=true"
		var cmd BridgeCommand
		for {
			resp, err := http.Get(pollURL)
			if err != nil {
				appDone <- err
				return
			}
			var poll PollCommandsResponse
			err = json.NewDecoder(resp.Body).Decode(&poll)
			resp.Body.Close()
			if err != nil {
				appDone <- err
				return
			}
			if len(poll.Commands) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			cmd = poll.Commands[0]
			break
		}

		resultData, _ := json.Marshal(SubmitResultRequest{BridgeCommandResponse: BridgeCommandResponse{
			ID:     cmd.ID,
			Method: "clipboard_read",
			Data:   json.RawMessage(`{"text":"from-http-queue"}`),
		}})
		resp, err := http.Post(
			remoteServer.URL+"/api/phone-bridge/results",
			"application/json",
			strings.NewReader(string(resultData)),
		)
		if err == nil {
			resp.Body.Close()
		}
		appDone <- err
	}()

	proxyBridge := NewPhoneBridgeProxy(remoteServer.URL, "benchmark-task", nil)
	defer proxyBridge.queue.Stop()
	resp, err := proxyBridge.SendQueuedCommand(context.Background(), BridgeCommand{
		ID:        "proxy-queued-command",
		Type:      "clipboard_read",
		TimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("SendQueuedCommand failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("SendQueuedCommand tool error: %+v", resp.Error)
	}
	if string(resp.Data) != `{"text":"from-http-queue"}` {
		t.Fatalf("response data = %s", resp.Data)
	}
	if err := <-appDone; err != nil {
		t.Fatalf("app HTTP queue loop failed: %v", err)
	}
	taskIDsMu.Lock()
	gotTaskIDs := append([]string(nil), taskIDs...)
	taskIDsMu.Unlock()
	var proxiedTaskIDs []string
	for _, taskID := range gotTaskIDs {
		if taskID != "" {
			proxiedTaskIDs = append(proxiedTaskIDs, taskID)
		}
	}
	if len(proxiedTaskIDs) < 3 {
		t.Fatalf("proxied task ids = %q, want status, enqueue, and result headers", gotTaskIDs)
	}
	for _, taskID := range proxiedTaskIDs {
		if taskID != "benchmark-task" {
			t.Fatalf("proxied task ids = %q, want benchmark-task headers", gotTaskIDs)
		}
	}
}
