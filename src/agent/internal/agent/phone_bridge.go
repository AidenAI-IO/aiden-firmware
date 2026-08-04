package agent

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"encoding/json"
	"errors"
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
	ID        string `json:"id"`
	Type      string `json:"type"`
	PhoneID   string `json:"phone_id,omitempty"`
	App       string `json:"app,omitempty"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	// Payload carries command-specific JSON (clipboard text, calendar event,
	// query window, etc.). open_app uses semantic top-level fields.
	Payload json.RawMessage `json:"payload,omitempty"`
}

type BridgeCommandResponse struct {
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Error  *ToolError      `json:"error,omitempty"` // nil ⇔ success
	Data   json.RawMessage `json:"data,omitempty"`
}

type PhoneEnvironment struct {
	CapturedAt       string                 `json:"captured_at,omitempty"`
	Source           string                 `json:"source,omitempty"`
	Platform         string                 `json:"platform,omitempty"`
	SystemName       string                 `json:"system_name,omitempty"`
	SystemVersion    string                 `json:"system_version,omitempty"`
	IsTablet         *bool                  `json:"is_tablet,omitempty"`
	Locale           string                 `json:"locale,omitempty"`
	Language         string                 `json:"language,omitempty"`
	Region           string                 `json:"region,omitempty"`
	TimeZone         string                 `json:"time_zone,omitempty"`
	UTCOffsetMinutes *int                   `json:"utc_offset_minutes,omitempty"`
	UTCOffset        string                 `json:"utc_offset,omitempty"`
	Uses24HourClock  *bool                  `json:"uses_24_hour_clock,omitempty"`
	Manufacturer     string                 `json:"manufacturer,omitempty"`
	Brand            string                 `json:"brand,omitempty"`
	Model            string                 `json:"model,omitempty"`
	DeviceName       string                 `json:"device_name,omitempty"`
	Screen           screen.PhoneScreenInfo `json:"screen,omitempty"`
	Battery          PhoneBatteryInfo       `json:"battery,omitempty"`
	SystemApps       []AvailableAppInfo     `json:"system_apps,omitempty"`
	ThirdPartyApps   []AvailableAppInfo     `json:"third_party_apps,omitempty"`
	AvailableApps    []AvailableAppInfo     `json:"available_apps,omitempty"`
}

type PhoneBatteryInfo struct {
	Level    *float64 `json:"level,omitempty"`
	Charging *bool    `json:"charging,omitempty"`
	State    string   `json:"state,omitempty"`
}

type AvailableAppInfo struct {
	Name               string `json:"name"`
	Available          bool   `json:"available"`
	Category           string `json:"category,omitempty"`
	AvailabilitySource string `json:"availability_source,omitempty"`
	IOSURL             string `json:"ios_url,omitempty"`
	AndroidPackage     string `json:"android_package,omitempty"`
	Note               string `json:"note,omitempty"`
	Error              string `json:"error,omitempty"`
}

type PhoneBridgeStatus struct {
	Connected            bool              `json:"connected"`
	Platform             string            `json:"platform,omitempty"`
	PhoneID              string            `json:"phone_id,omitempty"`
	BoardID              string            `json:"board_id,omitempty"`
	LastHeartbeatAt      *time.Time        `json:"last_heartbeat_at,omitempty"`
	AppState             string            `json:"app_state,omitempty"`
	AppStateUpdatedAt    *time.Time        `json:"app_state_updated_at,omitempty"`
	ReturnEntry          string            `json:"return_entry,omitempty"`
	ReturnEntryAvailable *bool             `json:"return_entry_available,omitempty"`
	PipBridgeEnabled     *bool             `json:"pip_bridge_enabled,omitempty"`
	FgsBridgeEnabled     *bool             `json:"fgs_bridge_enabled,omitempty"`
	FgsBridgeUpdatedAt   *time.Time        `json:"fgs_bridge_updated_at,omitempty"`
	Environment          *PhoneEnvironment `json:"environment,omitempty"`
	EnvironmentUpdatedAt *time.Time        `json:"environment_updated_at,omitempty"`
}

type PhoneBridge struct {
	mu                sync.Mutex
	statusExpiryTimer *time.Timer
	conn              *websocket.Conn
	connected         bool
	platform          string
	phoneID           string
	lastHeartbeatAt   time.Time
	appState          string
	appStateAt        time.Time
	returnEntry       string
	returnEntryOK     bool
	returnEntrySeen   bool
	pipBridgeEnabled  bool
	pipBridgeSeen     bool
	fgsBridgeEnabled  bool
	fgsBridgeSeen     bool
	fgsBridgeAt       time.Time
	environment       *PhoneEnvironment
	environmentAt     time.Time
	clipboardText     string
	clipboardAt       time.Time
	pendingCmds       map[string]chan BridgeCommandResponse
	logger            *Logger
	done              chan struct{}
	queue             *CommandQueue // HTTP queue for background-compatible commands
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
	phoneID := strings.TrimSpace(r.URL.Query().Get("phone_id"))

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
	pb.phoneID = phoneID
	pb.lastHeartbeatAt = time.Now()
	pb.environment = nil
	pb.environmentAt = time.Time{}
	pb.done = make(chan struct{})
	done := pb.done
	pb.mu.Unlock()

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: client connected (platform=%s phone_id=%s)", platform, phoneID)
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
			pb.phoneID = ""
			pb.environment = nil
			pb.environmentAt = time.Time{}
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

		if pb.handleAppEvent(resp) {
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

func (pb *PhoneBridge) handleAppEvent(resp BridgeCommandResponse) bool {
	switch {
	case resp.ID == "phone_environment" || resp.Method == "phone_environment":
		return pb.handleEnvironmentEvent(resp)
	case resp.ID == "phone_app_state" || resp.Method == "phone_app_state":
		return pb.handleAppStateEvent(resp)
	default:
		return false
	}
}

func (pb *PhoneBridge) handleEnvironmentEvent(resp BridgeCommandResponse) bool {
	if resp.Error != nil {
		if pb.logger != nil {
			pb.logger.Warn("phone-bridge: environment report failed: code=%s msg=%s",
				resp.Error.Code, resp.Error.Message)
		}
		return true
	}
	if len(resp.Data) == 0 {
		if pb.logger != nil {
			pb.logger.Warn("phone-bridge: environment report missing data")
		}
		return true
	}

	var env PhoneEnvironment
	if err := json.Unmarshal(resp.Data, &env); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: decode environment report failed: %v", err)
		}
		return true
	}

	pb.mu.Lock()
	if strings.TrimSpace(env.Platform) == "" {
		env.Platform = pb.platform
	}
	pb.environment = &env
	pb.environmentAt = time.Now()
	pb.lastHeartbeatAt = pb.environmentAt
	pb.mu.Unlock()

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: environment updated (platform=%s)", env.Platform)
	}
	return true
}

func (pb *PhoneBridge) handleAppStateEvent(resp BridgeCommandResponse) bool {
	if resp.Error != nil {
		if pb.logger != nil {
			pb.logger.Warn("phone-bridge: app state report failed: code=%s msg=%s",
				resp.Error.Code, resp.Error.Message)
		}
		return true
	}
	var payload struct {
		AppState             string `json:"app_state"`
		ReportedAt           string `json:"reported_at,omitempty"`
		ReturnEntry          string `json:"return_entry,omitempty"`
		ReturnEntryAvailable *bool  `json:"return_entry_available,omitempty"`
		PipBridgeEnabled     *bool  `json:"pip_bridge_enabled,omitempty"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: decode app state report failed: %v", err)
		}
		return true
	}
	appState, ok := normalizeAppState(payload.AppState)
	if !ok {
		if pb.logger != nil {
			pb.logger.Warn("phone-bridge: ignoring unknown app_state=%q", payload.AppState)
		}
		return true
	}
	appStateAt := time.Now()
	if ts := strings.TrimSpace(payload.ReportedAt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			appStateAt = parsed
		} else if pb.logger != nil {
			pb.logger.Warn("phone-bridge: invalid reported_at=%q: %v", payload.ReportedAt, err)
		}
	}
	pb.mu.Lock()
	pb.appState = appState
	pb.appStateAt = appStateAt
	if payload.ReturnEntryAvailable != nil {
		pb.returnEntryOK = *payload.ReturnEntryAvailable
		pb.returnEntrySeen = true
	}
	pb.returnEntry = strings.ToLower(strings.TrimSpace(payload.ReturnEntry))
	if payload.PipBridgeEnabled != nil {
		pb.pipBridgeEnabled = *payload.PipBridgeEnabled
		pb.pipBridgeSeen = true
	}
	pb.lastHeartbeatAt = pb.appStateAt
	pb.mu.Unlock()

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: app state updated (%s)", appState)
	}
	return true
}

func (pb *PhoneBridge) SendQueuedCommand(ctx context.Context, cmd BridgeCommand) (BridgeCommandResponse, error) {
	if cmd.ID == "" {
		return BridgeCommandResponse{}, fmt.Errorf("command ID must not be empty")
	}
	if pb.queue == nil {
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeBridgeNotConnected, "phone bridge command queue is not available"),
		}, nil
	}
	if strings.TrimSpace(cmd.PhoneID) == "" {
		cmd.PhoneID = pb.currentPhoneID()
	}
	if err := pb.queue.Enqueue(cmd); err != nil {
		if errors.Is(err, ErrCommandExists) {
			return BridgeCommandResponse{
				ID: cmd.ID,
				Error: NewToolErrorWithDetails(CodeCommandIDCollision,
					fmt.Sprintf("duplicate command ID %q already queued", cmd.ID),
					map[string]any{"command_id": cmd.ID}),
			}, nil
		}
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeToolExecutionFailed, fmt.Sprintf("enqueue command: %v", err)),
		}, nil
	}

	timeout := 10 * time.Second
	if cmd.TimeoutMs > 0 {
		timeout = time.Duration(cmd.TimeoutMs) * time.Millisecond
		if timeout < 10*time.Second {
			timeout = 10 * time.Second
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		result, status := pb.queue.QueryResult(cmd.ID)
		switch status {
		case StatusCompleted:
			if result != nil {
				return result.Response, nil
			}
		case StatusExpired:
			return BridgeCommandResponse{
				ID:    cmd.ID,
				Error: NewToolError(CodeBridgeTimeout, "queued command expired before result"),
			}, nil
		}

		select {
		case <-waitCtx.Done():
			if result, status := pb.queue.QueryResult(cmd.ID); status == StatusCompleted && result != nil {
				return result.Response, nil
			}
			pb.queue.Cancel(cmd.ID)
			if ctx.Err() != nil {
				return BridgeCommandResponse{}, ctx.Err()
			}
			return BridgeCommandResponse{
				ID:    cmd.ID,
				Error: NewToolError(CodeBridgeTimeout, "queued command timeout"),
			}, nil
		case <-ticker.C:
		}
	}
}

func (pb *PhoneBridge) SendCommand(ctx context.Context, cmd BridgeCommand) (BridgeCommandResponse, error) {
	if cmd.ID == "" {
		return BridgeCommandResponse{}, fmt.Errorf("command ID must not be empty")
	}
	pb.mu.Lock()
	if !pb.connected || pb.conn == nil {
		pb.mu.Unlock()
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeBridgeNotConnected, "phone bridge not connected"),
		}, nil
	}
	if _, exists := pb.pendingCmds[cmd.ID]; exists {
		pb.mu.Unlock()
		return BridgeCommandResponse{
			ID: cmd.ID,
			Error: NewToolErrorWithDetails(CodeCommandIDCollision,
				fmt.Sprintf("duplicate command ID %q already in flight", cmd.ID),
				map[string]any{"command_id": cmd.ID}),
		}, nil
	}
	ch := make(chan BridgeCommandResponse, 1)
	pb.pendingCmds[cmd.ID] = ch
	conn := pb.conn
	if strings.TrimSpace(cmd.PhoneID) == "" {
		cmd.PhoneID = pb.phoneID
	}
	pb.mu.Unlock()

	data, err := json.Marshal(cmd)
	if err != nil {
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeCommandMarshalFailed, fmt.Sprintf("marshal command: %v", err)),
		}, nil
	}

	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeBridgeWriteFailed, fmt.Sprintf("write command: %v", err)),
		}, nil
	}

	timeout := 5 * time.Second
	if cmd.TimeoutMs > 0 {
		timeout = time.Duration(cmd.TimeoutMs) * time.Millisecond
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return BridgeCommandResponse{
				ID:    cmd.ID,
				Error: NewToolError(CodeBridgeConnectionClosed, "connection closed before response"),
			}, nil
		}
		return resp, nil
	case <-time.After(timeout):
		pb.mu.Lock()
		delete(pb.pendingCmds, cmd.ID)
		pb.mu.Unlock()
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodeBridgeTimeout, "command timeout"),
		}, nil
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

func (pb *PhoneBridge) NoteClipboardWrite(text string) {
	if pb == nil {
		return
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.clipboardText = text
	pb.clipboardAt = time.Now()
}

func (pb *PhoneBridge) ClipboardRecentlyContains(text string, maxAge time.Duration) bool {
	if pb == nil || maxAge <= 0 {
		return false
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.clipboardAt.IsZero() || time.Since(pb.clipboardAt) > maxAge {
		return false
	}
	return strings.TrimSpace(pb.clipboardText) == strings.TrimSpace(text)
}

func (pb *PhoneBridge) ClipboardRecentlyEquals(text string, maxAge time.Duration) bool {
	if pb == nil || maxAge <= 0 {
		return false
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	if pb.clipboardAt.IsZero() || time.Since(pb.clipboardAt) > maxAge {
		return false
	}
	return pb.clipboardText == text
}

func (pb *PhoneBridge) currentPhoneID() string {
	if pb == nil {
		return ""
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return strings.TrimSpace(pb.phoneID)
}

func (pb *PhoneBridge) UpdateState() map[string]string {
	ret := make(map[string]string)
	if pb == nil {
		return ret
	}

	// Serialize snapshot publication so an older status callback cannot publish
	// after a newer HTTP poll or WebSocket event. The freshness expiry callback
	// uses the same path.
	status := pb.getStatus()
	// update stateManager with the new status
	// if connected, set app_connected to true
	if status.Connected {
		ret["app_connected"] = "true"
	} else {
		ret["app_connected"] = "false"
	}

	// Empty values overwrite stale state-manager entries and are omitted from
	// the model-facing <state> message by the state hook.
	ret["app_state"] = strings.TrimSpace(status.AppState)

	// Unknown capability flags are not usable routes, so expose them as false
	// instead of adding a third state for the model to reason about.
	ret["app_pip_enabled"] = fmt.Sprintf("%t", status.PipBridgeEnabled != nil && *status.PipBridgeEnabled)

	ret["app_fgs_enabled"] = fmt.Sprintf("%t", status.FgsBridgeEnabled != nil && *status.FgsBridgeEnabled)

	platform := strings.ToLower(strings.TrimSpace(status.Platform))
	if platform == "ios" || platform == "android" {
		ret["app_platform"] = platform
	} else {
		ret["app_platform"] = ""
	}

	return ret
}

func (pb *PhoneBridge) getStatus() PhoneBridgeStatus {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	status := PhoneBridgeStatus{
		Connected: pb.connected,
		Platform:  pb.platform,
		PhoneID:   pb.phoneID,
	}
	if pb.connected && !pb.lastHeartbeatAt.IsZero() {
		t := pb.lastHeartbeatAt
		status.LastHeartbeatAt = &t
	}
	if pb.appState != "" {
		status.AppState = pb.appState
		if !pb.appStateAt.IsZero() {
			t := pb.appStateAt
			status.AppStateUpdatedAt = &t
		}
	}
	if pb.returnEntrySeen {
		v := pb.returnEntryOK
		status.ReturnEntryAvailable = &v
	}
	if pb.returnEntry != "" {
		status.ReturnEntry = pb.returnEntry
	}
	if pb.pipBridgeSeen {
		enabled := pb.pipBridgeEnabled
		status.PipBridgeEnabled = &enabled
	}
	if pb.fgsBridgeSeen {
		enabled := pb.fgsBridgeEnabled
		status.FgsBridgeEnabled = &enabled
		if !pb.fgsBridgeAt.IsZero() {
			t := pb.fgsBridgeAt
			status.FgsBridgeUpdatedAt = &t
		}
	}
	if pb.connected && pb.environment != nil {
		env := clonePhoneEnvironment(*pb.environment)
		status.Environment = &env
		if !pb.environmentAt.IsZero() {
			t := pb.environmentAt
			status.EnvironmentUpdatedAt = &t
		}
	}
	return status
}

func clonePhoneEnvironment(env PhoneEnvironment) PhoneEnvironment {
	if len(env.SystemApps) > 0 {
		env.SystemApps = append([]AvailableAppInfo(nil), env.SystemApps...)
	}
	if len(env.ThirdPartyApps) > 0 {
		env.ThirdPartyApps = append([]AvailableAppInfo(nil), env.ThirdPartyApps...)
	}
	if len(env.AvailableApps) > 0 {
		env.AvailableApps = append([]AvailableAppInfo(nil), env.AvailableApps...)
	}
	return env
}

// ApplyBenchmarkStatus replaces the runtime Phone Bridge state for benchmark-only
// daemon workers. The caller is responsible for restricting access to a
// benchmark-authenticated endpoint. Freshness timestamps are set to now so
// PiP/FGS policy tests remain deterministic for the following task.
func (pb *PhoneBridge) ApplyBenchmarkStatus(status PhoneBridgeStatus) error {
	if pb == nil {
		return fmt.Errorf("phone bridge is not configured")
	}
	platform := strings.ToLower(strings.TrimSpace(status.Platform))
	if platform != "" && platform != "ios" && platform != "android" {
		return fmt.Errorf("unsupported platform %q", status.Platform)
	}
	appState := ""
	if strings.TrimSpace(status.AppState) != "" {
		var ok bool
		appState, ok = normalizeAppState(status.AppState)
		if !ok {
			return fmt.Errorf("unsupported app_state %q", status.AppState)
		}
	}
	now := time.Now()

	pb.mu.Lock()
	pb.connected = status.Connected
	pb.platform = platform
	pb.phoneID = strings.TrimSpace(status.PhoneID)
	if status.Connected {
		pb.lastHeartbeatAt = now
	} else {
		pb.lastHeartbeatAt = time.Time{}
	}
	pb.appState = appState
	if appState != "" {
		pb.appStateAt = now
	} else {
		pb.appStateAt = time.Time{}
	}
	pb.returnEntry = strings.ToLower(strings.TrimSpace(status.ReturnEntry))
	pb.returnEntrySeen = status.ReturnEntryAvailable != nil
	if status.ReturnEntryAvailable != nil {
		pb.returnEntryOK = *status.ReturnEntryAvailable
	} else {
		pb.returnEntryOK = false
	}
	pb.pipBridgeSeen = status.PipBridgeEnabled != nil
	if status.PipBridgeEnabled != nil {
		pb.pipBridgeEnabled = *status.PipBridgeEnabled
	} else {
		pb.pipBridgeEnabled = false
	}
	pb.fgsBridgeSeen = status.FgsBridgeEnabled != nil
	if status.FgsBridgeEnabled != nil {
		pb.fgsBridgeEnabled = *status.FgsBridgeEnabled
		pb.fgsBridgeAt = now
	} else {
		pb.fgsBridgeEnabled = false
		pb.fgsBridgeAt = time.Time{}
	}
	if status.Environment != nil {
		env := clonePhoneEnvironment(*status.Environment)
		if strings.TrimSpace(env.Platform) == "" {
			env.Platform = platform
		}
		pb.environment = &env
		pb.environmentAt = now
	} else {
		pb.environment = nil
		pb.environmentAt = time.Time{}
	}
	pb.mu.Unlock()

	return nil
}

func phoneBridgeRuntimeContext(status PhoneBridgeStatus) string {
	if !status.Connected &&
		strings.TrimSpace(status.AppState) == "" &&
		strings.TrimSpace(status.Platform) == "" &&
		status.PipBridgeEnabled == nil &&
		status.FgsBridgeEnabled == nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Phone bridge status:\n")
	if status.Connected {
		builder.WriteString("- connected: true\n")
	} else {
		builder.WriteString("- connected: false\n")
	}
	if platform := strings.TrimSpace(status.Platform); platform != "" {
		builder.WriteString("- platform: ")
		builder.WriteString(platform)
		builder.WriteByte('\n')
	}
	if phoneID := strings.TrimSpace(status.PhoneID); phoneID != "" {
		builder.WriteString("- phone_id: ")
		builder.WriteString(phoneID)
		builder.WriteByte('\n')
	}
	if status.Connected && status.LastHeartbeatAt != nil {
		builder.WriteString("- last_heartbeat_at: ")
		builder.WriteString(status.LastHeartbeatAt.UTC().Format(time.RFC3339))
		builder.WriteByte('\n')
	}
	if appState := strings.TrimSpace(status.AppState); appState != "" {
		builder.WriteString("- app_state: ")
		builder.WriteString(appState)
		builder.WriteByte('\n')
		if status.AppStateUpdatedAt != nil {
			builder.WriteString("- app_state_updated_at: ")
			builder.WriteString(status.AppStateUpdatedAt.UTC().Format(time.RFC3339))
			builder.WriteByte('\n')
		}
	}
	if returnEntry := strings.TrimSpace(status.ReturnEntry); returnEntry != "" || status.ReturnEntryAvailable != nil {
		builder.WriteString("- return_entry: ")
		if returnEntry == "" {
			returnEntry = "unknown"
		}
		builder.WriteString(returnEntry)
		if status.ReturnEntryAvailable != nil {
			visible := *status.ReturnEntryAvailable && !phoneBridgePiPBackgroundEnabled(status)
			builder.WriteString(" available=")
			if visible {
				builder.WriteString("true")
			} else {
				builder.WriteString("false")
			}
			if *status.ReturnEntryAvailable && !visible {
				builder.WriteString(" hidden_by_pip=true")
			}
		}
		builder.WriteByte('\n')
	}
	if status.PipBridgeEnabled != nil {
		builder.WriteString("- pip_bridge: ")
		builder.WriteString("enabled=")
		builder.WriteString(fmt.Sprintf("%t", *status.PipBridgeEnabled))
		builder.WriteByte(' ')
		if phoneBridgeIsIOS(status) {
			builder.WriteString("(PiP mode in the background can hide the Dynamic Island return entry)\n")
		} else {
			builder.WriteString("(background bridge mode status)\n")
		}
	}
	if status.FgsBridgeEnabled != nil {
		builder.WriteString("- fgs_bridge: ")
		builder.WriteString("enabled=")
		builder.WriteString(fmt.Sprintf("%t", *status.FgsBridgeEnabled))
		if status.FgsBridgeUpdatedAt != nil {
			builder.WriteString(" updated_at=")
			builder.WriteString(status.FgsBridgeUpdatedAt.UTC().Format(time.RFC3339))
		}
		builder.WriteByte('\n')
	}
	if status.Environment != nil {
		builder.WriteString("- device environment is available in World State for structured use\n")
		if status.EnvironmentUpdatedAt != nil {
			builder.WriteString("- environment_updated_at: ")
			builder.WriteString(status.EnvironmentUpdatedAt.UTC().Format(time.RFC3339))
			builder.WriteByte('\n')
		}
	}
	if phoneBridgeFGSBackgroundEnabled(status) {
		builder.WriteString("- Android Foreground Service bridge is enabled while Aiden is backgrounded. Background-safe data tools can run through the HTTP command queue; open_app will use visible system search because foreground Phone Bridge app launch is unavailable.\n")
	} else if phoneBridgePiPBackgroundEnabled(status) {
		builder.WriteString("- PiP Bridge mode is enabled while Aiden is backgrounded. iOS gives PiP priority over the Dynamic Island, so the Dynamic Island return entry is not visible in this state.\n")
	} else if phoneBridgeAppNeedsForeground(status) {
		if phoneBridgeIsIOS(status) {
			builder.WriteString("- The Aiden companion app is backgrounded or inactive. On iOS, Phone Bridge commands may time out until Aiden returns to foreground.\n")
			builder.WriteString("- If return_entry=dynamic_island and return_entry_available=true, open_url and the bridge data tools can first tap the Aiden Dynamic Island entry, wait for app_state=active/Phone Bridge reconnect, then send the command. open_app selects visible system search while foreground Phone Bridge app launch is unavailable. For lock-screen Live Activity entries, use screenshot/HID fallback or visual confirmation instead of blind tapping.\n")
		} else if phoneBridgeIsAndroid(status) {
			builder.WriteString("- The Android Aiden companion app is backgrounded or inactive. open_app will use visible system search; Android FGS Bridge mode only supports background-safe data tools.\n")
		} else {
			builder.WriteString("- The Aiden companion app is backgrounded or inactive. Phone Bridge foreground commands may time out until Aiden returns to foreground; use screenshot/HID fallback when reconnect is unavailable.\n")
		}
	} else if status.Connected {
		builder.WriteString("- The phone companion app is connected. Use open_app for semantic app launches and open_url for HTTP or HTTPS webpages. open_app selects Phone Bridge or visible system search internally.\n")
		builder.WriteString("- bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification tools are available through the companion app: prefer them over manual UI navigation for reading/writing the system clipboard, creating/querying/deleting system calendar events, managing contacts, or sending notifications.\n")
		builder.WriteString("- For long or non-ASCII text entry into a visible field, prefer enter_text_via_bridge when the runtime reports a usable clipboard route. It owns clipboard write, target-preserving quick_action paste, and field verification; do not manually chain bridge_clipboard with quick_action or construct a ctrl/meta keyboard_tap paste shortcut.\n")
		builder.WriteString("- If open_app or open_url returns {\"ok\":true}, treat the launch as complete unless the user requested additional in-app actions.")
	} else {
		builder.WriteString("- The phone companion app is not connected. open_app will use visible system search. open_url and bridge data tools become available after Phone Bridge reconnects or a supported recovery route succeeds.\n")
		if phoneBridgeIsIOS(status) {
			builder.WriteString("- If return_entry=dynamic_island and return_entry_available=true, open_url and bridge data tools can try to reopen Aiden through Dynamic Island and wait for Phone Bridge before sending the command. Otherwise use screenshot plus HID/touch fallback for operations that require Phone Bridge.")
		} else if phoneBridgeIsAndroid(status) {
			builder.WriteString("- Keep Aiden open in the foreground and wait for Phone Bridge to reconnect before retrying open_url or bridge data tools; open_app can continue through visible system search.")
		} else {
			builder.WriteString("- Retry companion app tools only after Phone Bridge reconnects or a visible platform return entry is confirmed; otherwise use screenshot plus HID/touch fallback and tell the user when app-only actions cannot be completed.")
		}
	}
	if !status.Connected {
		if !strings.HasSuffix(builder.String(), "\n") {
			builder.WriteByte('\n')
		}
		builder.WriteString("- ")
		builder.WriteString(phoneBridgeDisconnectedRecoveryGuidance)
	}
	return builder.String()
}

func appendNonEmptyLine(builder *strings.Builder, prefix, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	builder.WriteString(prefix)
	builder.WriteString(value)
	builder.WriteByte('\n')
}

func joinNonEmpty(sep string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, sep)
}

func firstNonEmptyPhoneField(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func fieldLabel(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return label + "=" + value
}

func boolLabel(label string, value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return label + "=true"
	}
	return label + "=false"
}

func formatPhoneBattery(battery PhoneBatteryInfo) string {
	parts := make([]string, 0, 3)
	if battery.Level != nil {
		parts = append(parts, fmt.Sprintf("level=%.0f%%", *battery.Level*100))
	}
	if battery.Charging != nil {
		parts = append(parts, boolLabel("charging", battery.Charging))
	}
	if state := strings.TrimSpace(battery.State); state != "" {
		parts = append(parts, "state="+state)
	}
	return strings.Join(parts, ", ")
}

func availableAppNames(apps []AvailableAppInfo, limit int) []string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if !app.Available {
			continue
		}
		name := strings.TrimSpace(app.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if limit > 0 && len(names) >= limit {
			break
		}
	}
	return names
}
