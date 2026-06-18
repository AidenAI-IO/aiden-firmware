package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	LiveActivityStatusRunning   = "running"
	LiveActivityStatusNeedsApp  = "needs_app"
	LiveActivityStatusCompleted = "completed"
	LiveActivityStatusFailed    = "failed"
	LiveActivityStatusCanceled  = "canceled"

	LiveActivityPhasePlanning    = "planning"
	LiveActivityPhaseObserving   = "observing"
	LiveActivityPhaseActing      = "acting"
	LiveActivityPhasePhoneBridge = "phone_bridge"
	LiveActivityPhaseWaitingApp  = "waiting_app"
	LiveActivityPhaseWaitingUser = "waiting_user"
	LiveActivityPhaseVerifying   = "verifying"
	LiveActivityPhaseAnswering   = "answering"

	liveActivityFinalStateRetention = 5 * time.Minute
	liveActivityAPNsQueueSize       = 16
)

type LiveActivityState struct {
	RequestID     string     `json:"request_id"`
	Status        string     `json:"status"`
	Phase         string     `json:"phase,omitempty"`
	TaskTitle     string     `json:"task_title"`
	CurrentStep   string     `json:"current_step"`
	CurrentAction string     `json:"current_action,omitempty"`
	CurrentTarget string     `json:"current_target,omitempty"`
	CurrentApp    string     `json:"current_app,omitempty"`
	LastToolName  string     `json:"last_tool_name,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Progress      float64    `json:"progress,omitempty"`
	ShowsProgress bool       `json:"shows_progress"`
	CanStop       bool       `json:"can_stop"`
	RequiresApp   bool       `json:"requires_app,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

type LiveActivityContentState struct {
	RequestID     string  `json:"request_id"`
	Status        string  `json:"status"`
	Phase         string  `json:"phase,omitempty"`
	TaskTitle     string  `json:"task_title"`
	CurrentStep   string  `json:"current_step"`
	CurrentAction string  `json:"current_action,omitempty"`
	CurrentTarget string  `json:"current_target,omitempty"`
	CurrentApp    string  `json:"current_app,omitempty"`
	LastToolName  string  `json:"last_tool_name,omitempty"`
	LastError     string  `json:"last_error,omitempty"`
	Progress      float64 `json:"progress,omitempty"`
	ShowsProgress bool    `json:"shows_progress"`
	CanStop       bool    `json:"can_stop"`
	RequiresApp   bool    `json:"requires_app,omitempty"`
	UpdatedAt     string  `json:"updated_at"`
}

type LiveActivityRegistrationRequest struct {
	RequestID  string `json:"request_id"`
	ActivityID string `json:"activity_id,omitempty"`
	PushToken  string `json:"push_token"`
	Platform   string `json:"platform,omitempty"`
}

type LiveActivityRegistrationResponse struct {
	OK      bool               `json:"ok"`
	State   *LiveActivityState `json:"state,omitempty"`
	APNs    string             `json:"apns"`
	Message string             `json:"message,omitempty"`
}

type liveActivityRegistration struct {
	RequestID    string
	ActivityID   string
	PushToken    string
	Platform     string
	RegisteredAt time.Time
}

type liveActivityPushRequest struct {
	requestID string
	pushToken string
	state     LiveActivityState
	final     bool
}

type LiveActivityManager struct {
	mu              sync.Mutex
	states          map[string]LiveActivityState
	registrations   map[string]liveActivityRegistration
	activeRequestID string
	apns            *APNsClient
	apnsQueue       chan liveActivityPushRequest
	logger          *Logger
}

func NewLiveActivityManager(cfg LiveActivityConfig, logger *Logger) *LiveActivityManager {
	if !cfg.EnabledOrDefault() {
		return nil
	}
	manager := &LiveActivityManager{
		states:        make(map[string]LiveActivityState),
		registrations: make(map[string]liveActivityRegistration),
		logger:        logger,
	}
	if cfg.APNsConfigured() {
		client, err := NewAPNsClient(cfg)
		if err != nil {
			if logger != nil {
				logger.Error("live activity: APNs disabled: %v", err)
			}
		} else {
			manager.apns = client
			manager.apnsQueue = make(chan liveActivityPushRequest, liveActivityAPNsQueueSize)
			go manager.runAPNsPublisher(client, manager.apnsQueue)
			if logger != nil {
				logger.Info("live activity: APNs enabled environment=%s topic=%s", cfg.EnvironmentOrDefault(), cfg.APNsTopic())
			}
		}
	}
	return manager
}

func (m *LiveActivityManager) APNsStatus() string {
	if m == nil || m.apns == nil {
		return "not_configured"
	}
	return "configured"
}

func (m *LiveActivityManager) StartTask(requestID, title string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	now := time.Now()
	state := LiveActivityState{
		RequestID:     strings.TrimSpace(requestID),
		Status:        LiveActivityStatusRunning,
		Phase:         LiveActivityPhasePlanning,
		TaskTitle:     truncateLiveActivityText(firstNonEmptyString([]string{title, "Aiden task"}), 80),
		CurrentStep:   "Planning next step",
		CurrentAction: "plan",
		Progress:      0.05,
		ShowsProgress: true,
		CanStop:       true,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	m.mu.Lock()
	m.states[state.RequestID] = state
	m.activeRequestID = state.RequestID
	m.mu.Unlock()
	m.publish(state.RequestID, false)
	return &state
}

func (m *LiveActivityManager) UpdateFromRunEvent(requestID string, event RunEvent) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	m.mu.Lock()
	requestID = strings.TrimSpace(requestID)
	state, ok := m.states[requestID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	switch event.Type {
	case "role_output":
		state.Status = LiveActivityStatusRunning
		state.ShowsProgress = true
		state.RequiresApp = false
		state.LastError = ""
		state.LastToolName = ""
		state.CurrentAction = liveActivityActionFromRole(event.Role, event.Content)
		state.Phase = liveActivityPhaseFromRole(event.Role, event.Content)
		if step := truncateLiveActivityText(liveActivityStepFromRoleOutput(event), 120); step != "" {
			state.CurrentStep = step
		}
	case runEventToolCall:
		toolStatus := liveActivityToolCallStatus(event)
		state.Status = LiveActivityStatusRunning
		state.Phase = toolStatus.phase
		state.CurrentAction = toolStatus.action
		state.CurrentTarget = truncateLiveActivityText(toolStatus.target, 80)
		state.RequiresApp = toolStatus.requiresApp
		state.ShowsProgress = true
		state.LastError = ""
		state.LastToolName = strings.TrimSpace(event.ToolName)
		if app := toolStatus.app; app != "" {
			state.CurrentApp = truncateLiveActivityText(app, 40)
		}
		state.CurrentStep = truncateLiveActivityText(firstNonEmptyString([]string{
			event.Description,
			event.Content,
			toolStatus.step,
			formatToolStep("Using", event.ToolName),
		}), 120)
		state.Progress = bumpLiveActivityProgress(state.Progress)
	case "tool_result":
		hasError := liveActivityEventHasError(event)
		state.LastToolName = strings.TrimSpace(event.ToolName)
		if hasError {
			errText := liveActivityEventErrorText(event)
			state.LastError = truncateLiveActivityText(errText, 160)
			if liveActivityResultNeedsApp(event, errText) {
				state.Status = LiveActivityStatusNeedsApp
				state.Phase = LiveActivityPhaseWaitingApp
				state.CurrentAction = "open_aiden"
				state.CurrentStep = "Open Aiden to continue"
				state.RequiresApp = true
				state.ShowsProgress = false
			} else {
				state.Status = LiveActivityStatusRunning
				state.Phase = liveActivityToolResultPhase(event.ToolName)
				state.CurrentAction = "recover"
				state.CurrentStep = truncateLiveActivityText(liveActivityToolErrorStep(event.ToolName), 120)
				state.RequiresApp = false
				state.ShowsProgress = true
			}
		} else {
			state.Status = LiveActivityStatusRunning
			state.Phase = liveActivityToolResultPhase(event.ToolName)
			state.CurrentAction = "verify_result"
			state.RequiresApp = false
			state.ShowsProgress = true
			state.LastError = ""
			state.CurrentStep = truncateLiveActivityText(liveActivityToolResultStep(event.ToolName), 120)
			state.Progress = bumpLiveActivityProgress(state.Progress)
		}
	case "steer":
		state.Status = LiveActivityStatusRunning
		state.Phase = LiveActivityPhasePlanning
		state.CurrentAction = "steer"
		state.CurrentStep = "Updating plan from user input"
		state.RequiresApp = false
		state.ShowsProgress = true
		state.LastError = ""
	}
	state.UpdatedAt = time.Now()
	m.states[requestID] = state
	m.activeRequestID = requestID
	m.mu.Unlock()
	m.publish(requestID, false)
	return &state
}

func (m *LiveActivityManager) CompleteTask(requestID, output string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusCompleted, firstNonEmptyString([]string{
		truncateLiveActivityText(output, 120),
		"Completed",
	}), "")
}

func (m *LiveActivityManager) FailTask(requestID, message string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusFailed, "Failed", truncateLiveActivityText(message, 160))
}

func (m *LiveActivityManager) CancelTask(requestID string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusCanceled, "Canceled", "")
}

func (m *LiveActivityManager) finishTask(requestID, status, step, errText string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	now := time.Now()
	m.mu.Lock()
	state, ok := m.states[requestID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	state.Status = status
	state.Phase = liveActivityFinalPhase(status)
	state.CurrentStep = truncateLiveActivityText(step, 120)
	state.CurrentAction = status
	state.CurrentTarget = ""
	state.LastError = errText
	state.Progress = 1
	state.ShowsProgress = false
	state.CanStop = false
	state.RequiresApp = false
	state.UpdatedAt = now
	state.EndedAt = &now
	m.states[requestID] = state
	m.activeRequestID = requestID
	m.mu.Unlock()
	m.publish(requestID, true)
	m.scheduleCleanup(requestID, now, liveActivityFinalStateRetention)
	return &state
}

func (m *LiveActivityManager) Register(req LiveActivityRegistrationRequest) (*LiveActivityState, string, error) {
	if m == nil {
		return nil, "disabled", errors.New("live activity is disabled")
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.PushToken = strings.TrimSpace(req.PushToken)
	if req.RequestID == "" {
		return nil, "invalid", errors.New("request_id is required")
	}
	if req.PushToken == "" {
		return nil, "invalid", errors.New("push_token is required")
	}
	m.mu.Lock()
	m.registrations[req.RequestID] = liveActivityRegistration{
		RequestID:    req.RequestID,
		ActivityID:   strings.TrimSpace(req.ActivityID),
		PushToken:    req.PushToken,
		Platform:     strings.TrimSpace(req.Platform),
		RegisteredAt: time.Now(),
	}
	state, ok := m.states[req.RequestID]
	m.mu.Unlock()
	if ok {
		m.publish(req.RequestID, isFinalLiveActivityStatus(state.Status))
		return &state, m.APNsStatus(), nil
	}
	return nil, m.APNsStatus(), nil
}

func (m *LiveActivityManager) Unregister(requestID string) bool {
	if m == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	m.mu.Lock()
	_, ok := m.registrations[requestID]
	delete(m.registrations, requestID)
	m.mu.Unlock()
	return ok
}

func (m *LiveActivityManager) Snapshot(requestID string) *LiveActivityState {
	if m == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	state, ok := m.states[requestID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return &state
}

func (m *LiveActivityManager) SnapshotActive() *LiveActivityState {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeRequestID != "" {
		if state, ok := m.states[m.activeRequestID]; ok {
			return &state
		}
	}
	var latest *LiveActivityState
	for _, state := range m.states {
		candidate := state
		if latest == nil || candidate.UpdatedAt.After(latest.UpdatedAt) {
			latest = &candidate
		}
	}
	return latest
}

func (m *LiveActivityManager) publish(requestID string, final bool) {
	if m == nil || m.apns == nil {
		return
	}
	m.mu.Lock()
	state, stateOK := m.states[requestID]
	registration, regOK := m.registrations[requestID]
	queue := m.apnsQueue
	m.mu.Unlock()
	if !stateOK || !regOK {
		return
	}
	m.enqueueAPNsPush(liveActivityPushRequest{
		requestID: requestID,
		pushToken: registration.PushToken,
		state:     state,
		final:     final,
	}, queue)
}

func (m *LiveActivityManager) enqueueAPNsPush(req liveActivityPushRequest, queue chan liveActivityPushRequest) {
	if queue == nil {
		return
	}
	select {
	case queue <- req:
		return
	default:
	}
	if req.final {
		select {
		case <-queue:
		default:
		}
		select {
		case queue <- req:
			return
		default:
		}
	}
	if m.logger != nil {
		m.logger.Warn("live activity: dropping APNs push request_id=%s final=%t: queue full", req.requestID, req.final)
	}
}

func (m *LiveActivityManager) runAPNsPublisher(apns *APNsClient, queue <-chan liveActivityPushRequest) {
	for req := range queue {
		ctx, cancel := context.WithTimeout(context.Background(), apns.timeout)
		err := apns.Push(ctx, req.pushToken, req.state, req.final)
		cancel()
		if err != nil && m.logger != nil {
			m.logger.Error("live activity: APNs push failed request_id=%s: %v", req.requestID, err)
		}
	}
}

func (m *LiveActivityManager) scheduleCleanup(requestID string, endedAt time.Time, after time.Duration) {
	if m == nil || after <= 0 {
		return
	}
	time.AfterFunc(after, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		state, ok := m.states[requestID]
		if !ok || state.EndedAt == nil || !state.EndedAt.Equal(endedAt) {
			return
		}
		delete(m.states, requestID)
		delete(m.registrations, requestID)
		if m.activeRequestID == requestID {
			m.activeRequestID = ""
		}
	})
}

func (s LiveActivityState) ContentState() LiveActivityContentState {
	return LiveActivityContentState{
		RequestID:     s.RequestID,
		Status:        s.Status,
		Phase:         s.Phase,
		TaskTitle:     s.TaskTitle,
		CurrentStep:   s.CurrentStep,
		CurrentAction: s.CurrentAction,
		CurrentTarget: s.CurrentTarget,
		CurrentApp:    s.CurrentApp,
		LastToolName:  s.LastToolName,
		LastError:     s.LastError,
		Progress:      s.Progress,
		ShowsProgress: s.ShowsProgress,
		CanStop:       s.CanStop,
		RequiresApp:   s.RequiresApp,
		UpdatedAt:     s.UpdatedAt.Format(time.RFC3339),
	}
}

func bumpLiveActivityProgress(current float64) float64 {
	if current <= 0 {
		return 0.1
	}
	next := current + 0.08
	if next > 0.92 {
		return 0.92
	}
	return next
}

func formatToolStep(prefix, tool string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return prefix
	}
	return prefix + " " + tool
}

type liveActivityToolStatus struct {
	phase       string
	action      string
	step        string
	app         string
	target      string
	requiresApp bool
}

func liveActivityPhaseFromRole(role, content string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if liveActivityJSONHasKey(content, "final_answer") {
		return LiveActivityPhaseAnswering
	}
	switch role {
	case "planner":
		return LiveActivityPhasePlanning
	case "executor":
		return LiveActivityPhaseActing
	case "verifier":
		return LiveActivityPhaseVerifying
	default:
		return LiveActivityPhasePlanning
	}
}

func liveActivityActionFromRole(role, content string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if liveActivityJSONHasKey(content, "final_answer") {
		return "answer"
	}
	switch role {
	case "planner":
		return "plan"
	case "executor":
		return "execute"
	case "verifier":
		return "verify"
	default:
		return "think"
	}
}

func liveActivityToolCallStatus(event RunEvent) liveActivityToolStatus {
	tool := strings.ToLower(strings.TrimSpace(event.ToolName))
	target := liveActivityTargetFromToolCall(event)
	status := liveActivityToolStatus{
		phase:  LiveActivityPhaseActing,
		action: normalizedLiveActivityAction(tool),
		step:   liveActivityToolCallStep(tool),
		target: target,
	}
	switch tool {
	case "screenshot":
		status.phase = LiveActivityPhaseObserving
		status.action = "observe_screen"
	case "wait_for_stable_screen":
		status.phase = LiveActivityPhaseObserving
		status.action = "wait_for_screen"
	case "open_app":
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "open_app"
		status.requiresApp = true
		status.app = liveActivityAppFromToolCall(event)
		if status.step == "" {
			status.step = "Opening app"
		}
		if target != "" {
			status.step = "Opening " + target
		}
	case "clipboard":
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "clipboard"
		status.requiresApp = true
		status.step = liveActivityClipboardStep(event.ToolInput)
	case "calendar":
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "calendar"
		status.requiresApp = true
		status.step = liveActivityActionStep("calendar", event.ToolInput, map[string]string{
			"create": "Creating calendar event",
			"query":  "Checking calendar",
			"delete": "Deleting calendar event",
		}, "Updating calendar")
	case "contacts":
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "contacts"
		status.requiresApp = true
		status.step = liveActivityActionStep("contacts", event.ToolInput, map[string]string{
			"query":  "Checking contacts",
			"create": "Creating contact",
			"update": "Updating contact",
		}, "Checking contacts")
	case "notification":
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "notification"
		status.requiresApp = true
		status.step = "Sending notification"
	case "request_human_handoff":
		status.phase = LiveActivityPhaseWaitingUser
		status.action = "request_user_input"
		status.step = "Waiting for user input"
	case "touch_gesture", "mouse_click", "quick_action":
		status.action = "control_phone"
	case "mouse_move":
		status.action = "move_pointer"
	case "mouse_scroll":
		status.action = "scroll"
	case "keyboard_text":
		status.action = "type_text"
	case "keyboard_tap":
		status.action = "press_keys"
	case "web_search", "wikipedia", "web_scraper":
		status.action = "search"
	case "current_time", "weather":
		status.action = "check_information"
	}
	if status.step == "" {
		status.step = formatToolStep("Using", tool)
	}
	return status
}

func liveActivityToolResultPhase(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "screenshot", "wait_for_stable_screen", "image_diff":
		return LiveActivityPhaseVerifying
	case "open_app", "clipboard", "calendar", "contacts", "notification":
		return LiveActivityPhasePhoneBridge
	case "request_human_handoff":
		return LiveActivityPhaseWaitingUser
	default:
		return LiveActivityPhaseVerifying
	}
}

func liveActivityFinalPhase(status string) string {
	switch status {
	case LiveActivityStatusCompleted:
		return LiveActivityPhaseAnswering
	case LiveActivityStatusFailed:
		return "failed"
	case LiveActivityStatusCanceled:
		return "canceled"
	default:
		return ""
	}
}

func liveActivityEventHasError(event RunEvent) bool {
	if event.IsError || toolOutputLooksLikeError(event.Content) {
		return true
	}
	payload, ok := liveActivityJSONObject(event.Content)
	if !ok {
		return false
	}
	if okValue, ok := payload["ok"].(bool); ok && !okValue {
		return true
	}
	return false
}

func liveActivityEventErrorText(event RunEvent) string {
	payload, ok := liveActivityJSONObject(event.Content)
	if ok {
		if value, ok := payload["error"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	content := strings.TrimSpace(event.Content)
	if strings.HasPrefix(strings.ToLower(content), "error:") {
		return strings.TrimSpace(content[len("error:"):])
	}
	return content
}

func liveActivityResultNeedsApp(event RunEvent, errText string) bool {
	if !liveActivityToolRequiresApp(event.ToolName) {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(errText))
	for _, marker := range []string{
		"phone bridge not connected",
		"not connected",
		"connection closed",
		"command timeout",
		"write command",
		"websocket",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func liveActivityToolRequiresApp(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "open_app", "clipboard", "calendar", "contacts", "notification":
		return true
	default:
		return false
	}
}

func liveActivityClipboardStep(input string) string {
	payload, ok := liveActivityJSONObject(input)
	if !ok {
		return "Using clipboard"
	}
	switch strings.ToLower(strings.TrimSpace(liveActivityString(payload, "action"))) {
	case "read":
		return "Reading clipboard"
	case "write":
		return "Writing clipboard"
	default:
		return "Using clipboard"
	}
}

func liveActivityActionStep(tool, input string, labels map[string]string, fallback string) string {
	payload, ok := liveActivityJSONObject(input)
	if !ok {
		return fallback
	}
	action := strings.ToLower(strings.TrimSpace(liveActivityString(payload, "action")))
	if label := strings.TrimSpace(labels[action]); label != "" {
		return label
	}
	return fallback
}

func liveActivityTargetFromToolCall(event RunEvent) string {
	payload, ok := liveActivityJSONObject(event.ToolInput)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(event.ToolName)) {
	case "open_app":
		return firstNonEmptyString([]string{
			liveActivityString(payload, "app"),
			liveActivityString(payload, "name"),
			liveActivityString(payload, "url"),
			liveActivityString(payload, "phone_number"),
			liveActivityFirstString(payload, "ios_urls"),
			liveActivityFirstString(payload, "android_packages"),
		})
	case "calendar":
		return firstNonEmptyString([]string{
			liveActivityString(payload, "title"),
			liveActivityString(payload, "from"),
			liveActivityString(payload, "event_id"),
		})
	case "contacts":
		return firstNonEmptyString([]string{
			liveActivityString(payload, "name"),
			liveActivityString(payload, "query"),
			liveActivityString(payload, "contact_id"),
		})
	case "notification":
		return liveActivityString(payload, "title")
	case "weather":
		return liveActivityString(payload, "location")
	case "web_search", "wikipedia", "web_scraper":
		return firstNonEmptyString([]string{
			liveActivityString(payload, "query"),
			liveActivityString(payload, "url"),
		})
	default:
		return ""
	}
}

func liveActivityJSONObject(input string) (map[string]interface{}, bool) {
	input = strings.TrimSpace(input)
	if input == "" || !strings.HasPrefix(input, "{") {
		return nil, false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func liveActivityJSONHasKey(input, key string) bool {
	payload, ok := liveActivityJSONObject(input)
	if !ok {
		return false
	}
	_, ok = payload[key]
	return ok
}

func liveActivityString(payload map[string]interface{}, key string) string {
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func liveActivityFirstString(payload map[string]interface{}, key string) string {
	values, ok := payload[key].([]interface{})
	if !ok {
		return ""
	}
	for _, value := range values {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func normalizedLiveActivityAction(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "" {
		return "use_tool"
	}
	var builder strings.Builder
	for _, r := range tool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "use_tool"
	}
	return builder.String()
}

func liveActivityStepFromRoleOutput(event RunEvent) string {
	role := strings.ToLower(strings.TrimSpace(event.Role))
	content := strings.TrimSpace(event.Content)
	if content != "" && strings.HasPrefix(content, "{") {
		if step := liveActivityStepFromJSONRoleOutput(role, content); step != "" {
			return step
		}
	}
	switch role {
	case "planner":
		return "Planning next step"
	case "executor":
		return "Working on the phone"
	case "verifier":
		return "Checking result"
	default:
		if content != "" && !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
			return content
		}
		return "Thinking"
	}
}

func liveActivityStepFromJSONRoleOutput(role, content string) string {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	if _, ok := payload["final_answer"]; ok {
		return "Preparing answer"
	}
	for _, key := range []string{"current_step", "next_step", "executor_summary", "summary", "reason"} {
		if value, ok := payload[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				switch role {
				case "planner":
					return "Planning: " + value
				case "verifier":
					return "Checking: " + value
				default:
					return value
				}
			}
		}
	}
	if plan, ok := payload["plan"].([]interface{}); ok && len(plan) > 0 {
		if first, ok := plan[0].(string); ok && strings.TrimSpace(first) != "" {
			return "Planning: " + strings.TrimSpace(first)
		}
	}
	return ""
}

func liveActivityToolCallStep(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "screenshot":
		return "Checking the screen"
	case "wait_for_stable_screen":
		return "Waiting for the screen"
	case "open_app":
		return "Opening app"
	case "touch_gesture", "mouse_click", "quick_action":
		return "Controlling the phone"
	case "mouse_move":
		return "Moving pointer"
	case "mouse_scroll":
		return "Scrolling"
	case "keyboard_text":
		return "Typing text"
	case "keyboard_tap":
		return "Pressing keys"
	case "clipboard":
		return "Using clipboard"
	case "calendar":
		return "Updating calendar"
	case "contacts":
		return "Checking contacts"
	case "notification":
		return "Sending notification"
	case "web_search", "wikipedia", "web_scraper":
		return "Searching"
	case "audio_volume":
		return "Adjusting audio"
	case "image_diff":
		return "Comparing screen changes"
	case "calculator":
		return "Calculating"
	case "current_time", "weather":
		return "Checking information"
	case "recall_memory", "recall_session_chunks", "recall_device_memory", "inspect_episode":
		return "Recalling context"
	case "save_memory", "forget_memory":
		return "Updating memory"
	case "skill_list", "skill_read", "skill_manage", "skill_mark_used":
		return "Using skills"
	case "request_human_handoff":
		return "Waiting for user input"
	default:
		return ""
	}
}

func liveActivityToolResultStep(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "screenshot":
		return "Screen checked"
	case "wait_for_stable_screen":
		return "Screen is ready"
	case "open_app":
		return "App opened"
	case "touch_gesture", "mouse_click", "quick_action", "mouse_move", "mouse_scroll", "keyboard_tap", "keyboard_text":
		return "Action sent; checking result"
	case "request_human_handoff":
		return "Waiting for user input"
	default:
		if step := liveActivityToolCallStep(tool); step != "" {
			return "Finished: " + step
		}
		return formatToolStep("Finished", tool)
	}
}

func liveActivityToolErrorStep(tool string) string {
	if step := liveActivityToolCallStep(tool); step != "" {
		return "Problem while " + strings.ToLower(step)
	}
	return formatToolStep("Tool failed", tool)
}

func liveActivityAppFromToolCall(event RunEvent) string {
	if strings.ToLower(strings.TrimSpace(event.ToolName)) != "open_app" {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(event.ToolInput)), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"app", "name"} {
		if value, ok := payload[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	if value, ok := payload["url"].(string); ok && strings.TrimSpace(value) != "" {
		return "Browser"
	}
	if value, ok := payload["phone_number"].(string); ok && strings.TrimSpace(value) != "" {
		return "Phone"
	}
	return ""
}

func truncateLiveActivityText(s string, limit int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if limit <= 0 || len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "..."
}

func isFinalLiveActivityStatus(status string) bool {
	switch status {
	case LiveActivityStatusCompleted, LiveActivityStatusFailed, LiveActivityStatusCanceled:
		return true
	default:
		return false
	}
}

type APNsClient struct {
	httpClient *http.Client
	endpoint   string
	topic      string
	teamID     string
	keyID      string
	privateKey *ecdsa.PrivateKey
	timeout    time.Duration

	mu          sync.Mutex
	cachedJWT   string
	cachedJWTAt time.Time
}

func NewAPNsClient(cfg LiveActivityConfig) (*APNsClient, error) {
	key, err := loadAPNsPrivateKey(cfg)
	if err != nil {
		return nil, err
	}
	topic := cfg.APNsTopic()
	if topic == "" {
		return nil, errors.New("missing APNs topic")
	}
	endpoint := "https://api.sandbox.push.apple.com"
	if cfg.EnvironmentOrDefault() == "production" {
		endpoint = "https://api.push.apple.com"
	}
	return &APNsClient{
		httpClient: &http.Client{Timeout: cfg.TimeoutOrDefault()},
		endpoint:   endpoint,
		topic:      topic,
		teamID:     strings.TrimSpace(cfg.TeamID),
		keyID:      strings.TrimSpace(cfg.KeyID),
		privateKey: key,
		timeout:    cfg.TimeoutOrDefault(),
	}, nil
}

func (c *APNsClient) Push(ctx context.Context, pushToken string, state LiveActivityState, final bool) error {
	if c == nil {
		return nil
	}
	pushToken = strings.TrimSpace(pushToken)
	if pushToken == "" {
		return errors.New("missing live activity push token")
	}
	event := "update"
	if final {
		event = "end"
	}
	apsPayload := map[string]interface{}{
		"timestamp":     time.Now().Unix(),
		"event":         event,
		"content-state": state.ContentState(),
	}
	if final {
		apsPayload["dismissal-date"] = time.Now().Add(30 * time.Second).Unix()
	}
	payload := map[string]interface{}{"aps": apsPayload}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode APNs payload: %w", err)
	}
	jwt, err := c.providerJWT()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/3/device/"+pushToken, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-push-type", "liveactivity")
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("APNs status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

func (c *APNsClient) providerJWT() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedJWT != "" && time.Since(c.cachedJWTAt) < 50*time.Minute {
		return c.cachedJWT, nil
	}
	header := map[string]string{"alg": "ES256", "kid": c.keyID}
	claims := map[string]interface{}{"iss": c.teamID, "iat": time.Now().Unix()}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, sum[:])
	if err != nil {
		return "", err
	}
	signature := append(fixedWidthBigInt(r, 32), fixedWidthBigInt(s, 32)...)
	c.cachedJWT = signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
	c.cachedJWTAt = time.Now()
	return c.cachedJWT, nil
}

func loadAPNsPrivateKey(cfg LiveActivityConfig) (*ecdsa.PrivateKey, error) {
	raw := strings.TrimSpace(cfg.PrivateKeyPEM)
	if raw == "" {
		data, err := os.ReadFile(strings.TrimSpace(cfg.PrivateKeyPath))
		if err != nil {
			return nil, fmt.Errorf("read APNs private key: %w", err)
		}
		raw = string(data)
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("APNs private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ecKey, ecErr := x509.ParseECPrivateKey(block.Bytes); ecErr == nil {
			return ecKey, nil
		}
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("APNs private key must be an ECDSA key")
	}
	return key, nil
}

func fixedWidthBigInt(v *big.Int, width int) []byte {
	raw := v.Bytes()
	if len(raw) >= width {
		return raw[len(raw)-width:]
	}
	out := make([]byte, width)
	copy(out[width-len(raw):], raw)
	return out
}
