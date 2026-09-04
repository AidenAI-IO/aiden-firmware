package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
