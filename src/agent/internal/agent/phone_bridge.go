package agent

import (
	"aiden-agent/internal/agent/statemanager"
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
	ID          string `json:"id"`
	Type        string `json:"type"`
	PhoneID     string `json:"phone_id,omitempty"`
	App         string `json:"app,omitempty"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	TimeoutMs   int    `json:"timeout_ms,omitempty"`
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
	CapturedAt       string             `json:"captured_at,omitempty"`
	Source           string             `json:"source,omitempty"`
	Platform         string             `json:"platform,omitempty"`
	SystemName       string             `json:"system_name,omitempty"`
	SystemVersion    string             `json:"system_version,omitempty"`
	IsTablet         *bool              `json:"is_tablet,omitempty"`
	Locale           string             `json:"locale,omitempty"`
	Language         string             `json:"language,omitempty"`
	Region           string             `json:"region,omitempty"`
	TimeZone         string             `json:"time_zone,omitempty"`
	UTCOffsetMinutes *int               `json:"utc_offset_minutes,omitempty"`
	UTCOffset        string             `json:"utc_offset,omitempty"`
	Uses24HourClock  *bool              `json:"uses_24_hour_clock,omitempty"`
	Manufacturer     string             `json:"manufacturer,omitempty"`
	Brand            string             `json:"brand,omitempty"`
	Model            string             `json:"model,omitempty"`
	DeviceName       string             `json:"device_name,omitempty"`
	Screen           PhoneScreenInfo    `json:"screen,omitempty"`
	Battery          PhoneBatteryInfo   `json:"battery,omitempty"`
	SystemApps       []AvailableAppInfo `json:"system_apps,omitempty"`
	ThirdPartyApps   []AvailableAppInfo `json:"third_party_apps,omitempty"`
	AvailableApps    []AvailableAppInfo `json:"available_apps,omitempty"`
}

type PhoneScreenInfo struct {
	Width              *float64 `json:"width,omitempty"`
	Height             *float64 `json:"height,omitempty"`
	WidthPixels        *int     `json:"width_pixels,omitempty"`
	HeightPixels       *int     `json:"height_pixels,omitempty"`
	NativeWidthPixels  *int     `json:"native_width_pixels,omitempty"`
	NativeHeightPixels *int     `json:"native_height_pixels,omitempty"`
	Scale              *float64 `json:"scale,omitempty"`
	NativeScale        *float64 `json:"native_scale,omitempty"`
	Density            *float64 `json:"density,omitempty"`
	DensityDPI         *int     `json:"density_dpi,omitempty"`
	ScaledDensity      *float64 `json:"scaled_density,omitempty"`
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
	mu               sync.Mutex
	conn             *websocket.Conn
	connected        bool
	platform         string
	phoneID          string
	lastHeartbeatAt  time.Time
	appState         string
	appStateAt       time.Time
	returnEntry      string
	returnEntryOK    bool
	returnEntrySeen  bool
	pipBridgeEnabled bool
	pipBridgeSeen    bool
	fgsBridgeEnabled bool
	fgsBridgeSeen    bool
	fgsBridgeAt      time.Time
	environment      *PhoneEnvironment
	environmentAt    time.Time
	clipboardText    string
	clipboardAt      time.Time
	pendingCmds      map[string]chan BridgeCommandResponse
	logger           *Logger
	done             chan struct{}
	queue            *CommandQueue // HTTP queue for background-compatible commands
	stateManager     *statemanager.StateManager
}

func NewPhoneBridge(logger *Logger, stateManager *statemanager.StateManager) *PhoneBridge {
	return &PhoneBridge{
		pendingCmds:  make(map[string]chan BridgeCommandResponse),
		logger:       logger,
		queue:        NewCommandQueue(logger),
		stateManager: stateManager,
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

	pb.statusUpdated()

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
		pb.statusUpdated()
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

	pb.statusUpdated()

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

	pb.statusUpdated()

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
	pb.clipboardText = strings.TrimSpace(text)
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

func (pb *PhoneBridge) currentPhoneID() string {
	if pb == nil {
		return ""
	}
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return strings.TrimSpace(pb.phoneID)
}

func (pb *PhoneBridge) statusUpdated() {
	status := pb.getStatus()
	// update stateManager with the new status
	// if connected, set app_connected to true
	if status.Connected {
		pb.stateManager.SetState("app_connected", "true")
	} else {
		pb.stateManager.SetState("app_connected", "false")
	}

	if status.AppState != "" {
		pb.stateManager.SetState("app_state", status.AppState)
	} else {
		pb.stateManager.DeleteState("app_state")
	}

	if status.PipBridgeEnabled != nil {
		pb.stateManager.SetState("app_pip_enabled", fmt.Sprintf("%t", *status.PipBridgeEnabled))
	} else {
		pb.stateManager.DeleteState("app_pip_enabled")
	}

	if status.FgsBridgeEnabled != nil {
		pb.stateManager.SetState("app_fgs_enabled", fmt.Sprintf("%t", *status.FgsBridgeEnabled))
	} else {
		pb.stateManager.DeleteState("app_fgs_enabled")
	}

	if status.Environment != nil {
		pb.stateManager.SetState("app_platform", status.Platform)
	} else {
		pb.stateManager.DeleteState("app_platform")
	}
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
		builder.WriteString("- Android Foreground Service bridge is enabled while Aiden is backgrounded. Background-safe data tools can run through the HTTP command queue; open_app and UI actions still require foreground app control or HID fallback.\n")
	} else if phoneBridgePiPBackgroundEnabled(status) {
		builder.WriteString("- PiP Bridge mode is enabled while Aiden is backgrounded. iOS gives PiP priority over the Dynamic Island, so the Dynamic Island return entry is not visible in this state.\n")
	} else if phoneBridgeAppNeedsForeground(status) {
		if phoneBridgeIsIOS(status) {
			builder.WriteString("- The Aiden companion app is backgrounded or inactive. On iOS, Phone Bridge commands may time out until Aiden returns to foreground.\n")
			builder.WriteString("- If return_entry=dynamic_island and return_entry_available=true, bridge_open_app, bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification will first tap the Aiden Dynamic Island entry, wait for app_state=active/Phone Bridge reconnect, then send the command. For lock-screen Live Activity entries, use screenshot/HID fallback or visual confirmation instead of blind tapping.\n")
		} else if phoneBridgeIsAndroid(status) {
			builder.WriteString("- The Android Aiden companion app is backgrounded or inactive. bridge_open_app and UI actions require foreground app control or HID fallback; Android FGS Bridge mode only supports background-safe data tools.\n")
		} else {
			builder.WriteString("- The Aiden companion app is backgrounded or inactive. Phone Bridge foreground commands may time out until Aiden returns to foreground; use screenshot/HID fallback when reconnect is unavailable.\n")
		}
	} else if status.Connected {
		builder.WriteString("- The phone companion app is connected. Use bridge_open_app as the primary path for opening apps, webpages, and phone dialer screens before falling back to screenshot/HID navigation.\n")
		builder.WriteString("- bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification tools are available through the companion app: prefer them over manual UI navigation for reading/writing the system clipboard, creating/querying/deleting system calendar events, managing contacts, or sending notifications.\n")
		builder.WriteString("- For long or non-ASCII text entry, prefer clipboard write through the companion app, switch to the target app, then paste with quick_action/keyboard shortcut instead of typing via HID.\n")
		builder.WriteString("- If bridge_open_app returns {\"ok\":true}, treat the app launch as complete unless the user requested additional in-app actions.")
	} else {
		builder.WriteString("- The phone companion app is not connected. Do not assume bridge_open_app, bridge_clipboard, bridge_calendar, bridge_contacts, or bridge_notification tools can control the phone right now.\n")
		if phoneBridgeIsIOS(status) {
			builder.WriteString("- If return_entry=dynamic_island and return_entry_available=true, bridge_open_app, bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification will first try to reopen Aiden through Dynamic Island and wait for Phone Bridge before sending the command. Otherwise use screenshot plus HID/touch fallback and tell the user when app-only actions cannot be completed.")
		} else if phoneBridgeIsAndroid(status) {
			builder.WriteString("- Keep Aiden open in the foreground and wait for Phone Bridge to reconnect before retrying bridge_open_app; otherwise use screenshot plus HID/touch fallback.")
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

func formatPhoneScreen(screen PhoneScreenInfo) string {
	parts := make([]string, 0, 8)
	if screen.Width != nil && screen.Height != nil {
		parts = append(parts, fmt.Sprintf("%.2fx%.2f pt/dp", *screen.Width, *screen.Height))
	}
	if screen.WidthPixels != nil && screen.HeightPixels != nil {
		parts = append(parts, fmt.Sprintf("%dx%d px", *screen.WidthPixels, *screen.HeightPixels))
	}
	if screen.NativeWidthPixels != nil && screen.NativeHeightPixels != nil {
		parts = append(parts, fmt.Sprintf("native=%dx%d px", *screen.NativeWidthPixels, *screen.NativeHeightPixels))
	}
	if screen.Scale != nil {
		parts = append(parts, fmt.Sprintf("scale=%.2f", *screen.Scale))
	}
	if screen.NativeScale != nil {
		parts = append(parts, fmt.Sprintf("native_scale=%.2f", *screen.NativeScale))
	}
	if screen.Density != nil {
		parts = append(parts, fmt.Sprintf("density=%.2f", *screen.Density))
	}
	if screen.DensityDPI != nil {
		parts = append(parts, fmt.Sprintf("density_dpi=%d", *screen.DensityDPI))
	}
	if screen.ScaledDensity != nil {
		parts = append(parts, fmt.Sprintf("scaled_density=%.2f", *screen.ScaledDensity))
	}
	return strings.Join(parts, ", ")
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
