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
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	IOSURLs         []string `json:"ios_urls,omitempty"`
	AndroidPackages []string `json:"android_packages,omitempty"`
	TimeoutMs       int      `json:"timeout_ms,omitempty"`
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
	Name           string `json:"name"`
	Available      bool   `json:"available"`
	IOSURL         string `json:"ios_url,omitempty"`
	AndroidPackage string `json:"android_package,omitempty"`
	Error          string `json:"error,omitempty"`
}

type PhoneBridgeStatus struct {
	Connected            bool              `json:"connected"`
	Platform             string            `json:"platform,omitempty"`
	LastHeartbeatAt      *time.Time        `json:"last_heartbeat_at,omitempty"`
	Environment          *PhoneEnvironment `json:"environment,omitempty"`
	EnvironmentUpdatedAt *time.Time        `json:"environment_updated_at,omitempty"`
}

type PhoneBridge struct {
	mu              sync.Mutex
	conn            *websocket.Conn
	connected       bool
	platform        string
	lastHeartbeatAt time.Time
	environment     *PhoneEnvironment
	environmentAt   time.Time
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
	pb.environment = nil
	pb.environmentAt = time.Time{}
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
	if resp.ID != "phone_environment" && resp.Method != "phone_environment" {
		return false
	}
	if !resp.OK {
		if pb.logger != nil {
			pb.logger.Warn("phone-bridge: environment report failed: %s", resp.Error)
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
	if len(env.AvailableApps) > 0 {
		env.AvailableApps = append([]AvailableAppInfo(nil), env.AvailableApps...)
	}
	return env
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
		if status.Environment != nil {
			appendPhoneEnvironmentContext(&builder, *status.Environment, status.EnvironmentUpdatedAt)
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

func appendPhoneEnvironmentContext(builder *strings.Builder, env PhoneEnvironment, updatedAt *time.Time) {
	builder.WriteString("Phone environment:\n")
	if updatedAt != nil {
		builder.WriteString("- environment_updated_at: ")
		builder.WriteString(updatedAt.UTC().Format(time.RFC3339))
		builder.WriteByte('\n')
	}
	if text := strings.TrimSpace(env.CapturedAt); text != "" {
		builder.WriteString("- captured_at: ")
		builder.WriteString(text)
		builder.WriteByte('\n')
	}
	appendNonEmptyLine(builder, "- system: ", joinNonEmpty(", ",
		firstNonEmptyPhoneField(env.SystemName, env.Platform),
		env.SystemVersion,
		boolLabel("tablet", env.IsTablet),
	))
	appendNonEmptyLine(builder, "- device: ", joinNonEmpty(", ",
		env.Manufacturer,
		env.Brand,
		env.Model,
		env.DeviceName,
	))
	appendNonEmptyLine(builder, "- locale: ", joinNonEmpty(", ",
		env.Locale,
		fieldLabel("language", env.Language),
		fieldLabel("region", env.Region),
		fieldLabel("timezone", env.TimeZone),
		fieldLabel("utc_offset", env.UTCOffset),
		boolLabel("24h_clock", env.Uses24HourClock),
	))
	appendNonEmptyLine(builder, "- screen: ", formatPhoneScreen(env.Screen))
	appendNonEmptyLine(builder, "- battery: ", formatPhoneBattery(env.Battery))
	if apps := availableAppNames(env.AvailableApps, 30); len(apps) > 0 {
		builder.WriteString("- available_apps: ")
		builder.WriteString(strings.Join(apps, ", "))
		builder.WriteByte('\n')
	}
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
	parts := make([]string, 0, 4)
	if screen.WidthPixels != nil && screen.HeightPixels != nil {
		parts = append(parts, fmt.Sprintf("%dx%d px", *screen.WidthPixels, *screen.HeightPixels))
	}
	if screen.Width != nil && screen.Height != nil {
		parts = append(parts, fmt.Sprintf("%.0fx%.0f pt/dp", *screen.Width, *screen.Height))
	}
	if screen.Scale != nil {
		parts = append(parts, fmt.Sprintf("scale=%.2f", *screen.Scale))
	}
	if screen.NativeScale != nil {
		parts = append(parts, fmt.Sprintf("native_scale=%.2f", *screen.NativeScale))
	}
	if screen.DensityDPI != nil {
		parts = append(parts, fmt.Sprintf("density_dpi=%d", *screen.DensityDPI))
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
