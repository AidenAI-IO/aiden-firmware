package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	// heartbeatTimeout matches the protocol contract: a connection that goes
	// this long without a heartbeat is considered dead. The app sends one every
	// 10-15s, so 60s tolerates a few missed beats before we tear down.
	heartbeatTimeout = 60 * time.Second
	// heartbeatCheckInterval is how often the monitor re-checks liveness.
	heartbeatCheckInterval = 15 * time.Second
)

type BridgeCommand struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	IOSURLs         []string        `json:"ios_urls,omitempty"`
	AndroidPackages []string        `json:"android_packages,omitempty"`
	TimeoutMs       int             `json:"timeout_ms,omitempty"`
	// Payload carries command-specific JSON (clipboard text, calendar event,
	// query window, etc.). Older command types like open_app leave this empty
	// because their parameters are top-level.
	Payload json.RawMessage `json:"payload,omitempty"`
}

type BridgeCommandResponse struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Method string `json:"method,omitempty"`
	Error  string `json:"error,omitempty"`
	// Data carries command-specific JSON returned by the companion app
	// (clipboard contents, calendar event id, query results, etc.). Empty for
	// commands that only need an ok/error status.
	Data json.RawMessage `json:"data,omitempty"`
}

type PhoneBridgeStatus struct {
	Connected       bool       `json:"connected"`
	Platform        string     `json:"platform,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

type PhoneBridge struct {
	mu              sync.Mutex
	conn            *websocket.Conn
	connected       bool
	platform        string
	lastHeartbeatAt time.Time
	pendingCmds     map[string]chan BridgeCommandResponse
	logger          *Logger
	done            chan struct{}
	queue           *CommandQueue // HTTP queue for background-compatible commands
}

func NewPhoneBridge(logger *Logger) *PhoneBridge {
	return &PhoneBridge{
		pendingCmds: make(map[string]chan BridgeCommandResponse),
		logger:      logger,
		queue:       NewCommandQueue(logger),
	}
}

func (pb *PhoneBridge) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Leave origin verification on (the library default): a request with an
	// empty Origin header — i.e. the native companion app — is accepted, while
	// a cross-origin browser handshake is rejected. This blocks cross-site
	// WebSocket hijacking, where a malicious page would otherwise connect and
	// impersonate the app by sending BridgeCommandResponse messages.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: websocket accept failed: %v", err)
		}
		return
	}

	platform := r.URL.Query().Get("platform")

	pb.mu.Lock()
	if pb.conn != nil {
		oldConn := pb.conn
		pb.mu.Unlock()
		oldConn.Close(websocket.StatusGoingAway, "new connection")
		pb.mu.Lock()
	}
	pb.conn = conn
	pb.connected = true
	pb.platform = platform
	pb.lastHeartbeatAt = time.Now()
	pb.done = make(chan struct{})
	done := pb.done
	pb.mu.Unlock()

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: client connected (platform=%s)", platform)
	}

	go pb.monitorHeartbeat(conn, done)
	pb.readLoop(conn, done)
}

// monitorHeartbeat tears down a connection that stops sending heartbeats. The
// readLoop's blocking conn.Read never returns on a half-open socket, so without
// this the bridge would keep reporting connected:true while every command
// silently times out. Closing the conn here unblocks readLoop, which performs
// the actual cleanup in its deferred handler.
func (pb *PhoneBridge) monitorHeartbeat(conn *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(heartbeatCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			pb.mu.Lock()
			stale := pb.conn == conn && time.Since(pb.lastHeartbeatAt) > heartbeatTimeout
			pb.mu.Unlock()
			if stale {
				if pb.logger != nil {
					pb.logger.Error("phone-bridge: heartbeat timeout, closing connection")
				}
				conn.Close(websocket.StatusGoingAway, "heartbeat timeout")
				return
			}
		}
	}
}

func (pb *PhoneBridge) readLoop(conn *websocket.Conn, done chan struct{}) {
	defer func() {
		pb.mu.Lock()
		if pb.conn == conn {
			pb.conn = nil
			pb.connected = false
			for id, ch := range pb.pendingCmds {
				close(ch)
				delete(pb.pendingCmds, id)
			}
		}
		pb.mu.Unlock()
		close(done)
		conn.Close(websocket.StatusNormalClosure, "")
		if pb.logger != nil {
			pb.logger.Info("phone-bridge: client disconnected")
		}
	}()

	for {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			if pb.logger != nil && websocket.CloseStatus(err) == -1 {
				pb.logger.Error("phone-bridge: read error: %v", err)
			}
			return
		}

		var resp BridgeCommandResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			if pb.logger != nil {
				pb.logger.Error("phone-bridge: invalid message: %v", err)
			}
			continue
		}

		// Echo heartbeat/ping back to the app so it can reset its missedPongs counter.
		// Without this, the app forces a reconnect every HEARTBEAT_INTERVAL * MAX_MISSED_PONGS.
		isHeartbeat := resp.ID == "heartbeat" || resp.ID == "ping"
		if isHeartbeat {
			if echoData, err := json.Marshal(resp); err == nil {
				_ = conn.Write(context.Background(), websocket.MessageText, echoData)
			}
		}

		pb.mu.Lock()
		if isHeartbeat {
			pb.lastHeartbeatAt = time.Now()
		}

		if ch, ok := pb.pendingCmds[resp.ID]; ok {
			select {
			case ch <- resp:
			default:
			}
			delete(pb.pendingCmds, resp.ID)
		}
		pb.mu.Unlock()
	}
}

func (pb *PhoneBridge) SendCommand(ctx context.Context, cmd BridgeCommand) (BridgeCommandResponse, error) {
	if cmd.ID == "" {
		return BridgeCommandResponse{}, fmt.Errorf("command ID must not be empty")
	}
	pb.mu.Lock()
	if !pb.connected || pb.conn == nil {
		pb.mu.Unlock()
		return BridgeCommandResponse{}, fmt.Errorf("phone bridge not connected")
	}
	if _, exists := pb.pendingCmds[cmd.ID]; exists {
		pb.mu.Unlock()
		return BridgeCommandResponse{}, fmt.Errorf("duplicate command ID %q already in flight", cmd.ID)
	}
	ch := make(chan BridgeCommandResponse, 1)
	pb.pendingCmds[cmd.ID] = ch
	conn := pb.conn
	pb.mu.Unlock()

	data, err := json.Marshal(cmd)
	if err != nil {
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{}, fmt.Errorf("marshal command: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{}, fmt.Errorf("write command: %w", err)
	}

	timeout := 5 * time.Second
	if cmd.TimeoutMs > 0 {
		timeout = time.Duration(cmd.TimeoutMs) * time.Millisecond
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return BridgeCommandResponse{}, fmt.Errorf("connection closed")
		}
		return resp, nil
	case <-time.After(timeout):
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{}, fmt.Errorf("command timeout")
	case <-ctx.Done():
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{}, ctx.Err()
	}
}

func (pb *PhoneBridge) Connected() bool {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.connected
}

func (pb *PhoneBridge) Status() PhoneBridgeStatus {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	status := PhoneBridgeStatus{
		Connected: pb.connected,
		Platform:  pb.platform,
	}
	if pb.connected && !pb.lastHeartbeatAt.IsZero() {
		t := pb.lastHeartbeatAt
		status.LastHeartbeatAt = &t
	}
	return status
}

func phoneBridgeRuntimeContext(status PhoneBridgeStatus) string {
	var builder strings.Builder
	builder.WriteString("Phone bridge status:\n")
	if status.Connected {
		builder.WriteString("- connected: true\n")
		if platform := strings.TrimSpace(status.Platform); platform != "" {
			builder.WriteString("- platform: ")
			builder.WriteString(platform)
			builder.WriteByte('\n')
		}
		if status.LastHeartbeatAt != nil {
			builder.WriteString("- last_heartbeat_at: ")
			builder.WriteString(status.LastHeartbeatAt.UTC().Format(time.RFC3339))
			builder.WriteByte('\n')
		}
		builder.WriteString("- The phone companion app is connected. Use open_app as the primary path for opening apps, URLs, deeplinks, and phone dialer screens before falling back to screenshot/HID navigation.\n")
		builder.WriteString("- clipboard, calendar, contacts, and notification tools are available through the companion app: prefer them over manual UI navigation for reading/writing the system clipboard, creating/querying/deleting system calendar events, managing contacts, or sending notifications.\n")
		builder.WriteString("- If open_app returns {\"ok\":true}, treat the app launch as complete unless the user requested additional in-app actions.")
		return builder.String()
	}
	builder.WriteString("- connected: false\n")
	builder.WriteString("- The phone companion app is not connected. Do not assume open_app, clipboard, calendar, contacts, or notification tools can control the phone; use screenshot plus HID/touch fallback for phone UI tasks, and tell the user when calendar/clipboard/contacts/notification actions cannot be completed without the companion app.")
	return builder.String()
}
