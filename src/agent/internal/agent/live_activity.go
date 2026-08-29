package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent/speech"
)

const (
	LiveActivityStatusRunning   = "running"
	LiveActivityStatusReady     = "ready"
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
	liveActivityLocalNotifyInterval = 750 * time.Millisecond
	liveActivityLocalNotifyTimeout  = time.Second
)

type LiveActivityState struct {
	RequestID     string     `json:"request_id"`
	PhoneID       string     `json:"phone_id,omitempty"`
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

type LiveActivityManager struct {
	mu                 sync.Mutex
	states             map[string]LiveActivityState
	activeRequestID    string
	logger             *Logger
	localNotifyMu      sync.RWMutex
	localNotifier      func(context.Context, string) error
	localNotifyQueue   chan struct{}
	localNotifyStarted bool
}

func NewLiveActivityManager(cfg LiveActivityConfig, logger *Logger) *LiveActivityManager {
	if !cfg.EnabledOrDefault() {
		return nil
	}
	manager := &LiveActivityManager{
		states: make(map[string]LiveActivityState),
		logger: logger,
	}
	return manager
}

func (m *LiveActivityManager) SetLocalUpdateNotifier(notifier func(context.Context, string) error) {
	if m == nil {
		return
	}
	m.localNotifyMu.Lock()
	m.localNotifier = notifier
	queue := m.localNotifyQueue
	start := false
	if notifier != nil && queue == nil {
		queue = make(chan struct{}, 1)
		m.localNotifyQueue = queue
	}
	if notifier != nil && !m.localNotifyStarted {
		m.localNotifyStarted = true
		start = true
	}
	m.localNotifyMu.Unlock()
	if start && queue != nil {
		go m.runLocalUpdateNotifier(queue)
	}
}

func (m *LiveActivityManager) enqueueLocalUpdate() {
	if m == nil {
		return
	}
	m.localNotifyMu.RLock()
	queue := m.localNotifyQueue
	m.localNotifyMu.RUnlock()
	if queue == nil {
		return
	}
	select {
	case queue <- struct{}{}:
	default:
	}
}

func (m *LiveActivityManager) runLocalUpdateNotifier(queue <-chan struct{}) {
	var lastAttempt time.Time
	for range queue {
		if wait := liveActivityLocalNotifyInterval - time.Since(lastAttempt); !lastAttempt.IsZero() && wait > 0 {
			timer := time.NewTimer(wait)
			<-timer.C
		}
		m.localNotifyMu.RLock()
		notifier := m.localNotifier
		m.localNotifyMu.RUnlock()
		if notifier == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), liveActivityLocalNotifyTimeout)
		err := notifier(ctx, "live_activity")
		cancel()
		lastAttempt = time.Now()
		if m.logger == nil {
			continue
		}
		if err != nil {
			m.logger.Debug("live activity: local BLE update wake unavailable: %v", err)
		} else {
			m.logger.Debug("live activity: local BLE update wake delivered")
		}
	}
}

func (m *LiveActivityManager) StartTask(requestID, title string, phoneIDs ...string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	m.logger.Info("Starting live activity task: %s, %s", requestID, title)
	phoneID := ""
	if len(phoneIDs) > 0 {
		phoneID = firstNonEmptyString(phoneIDs)
	}
	phoneID = strings.TrimSpace(phoneID)
	now := time.Now()
	state := LiveActivityState{
		RequestID:     strings.TrimSpace(requestID),
		PhoneID:       phoneID,
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
		state.CurrentAction = liveActivityActionFromRole(event.Content)
		state.Phase = liveActivityPhaseFromRole(event.Content)
		if step := truncateLiveActivityText(liveActivityStepFromRoleOutput(event), 120); step != "" {
			state.CurrentStep = step
		}
	case runEventToolCall:
		toolStatus := liveActivityToolCallStatus(event)
		state.Status = firstNonEmptyString([]string{toolStatus.status, LiveActivityStatusRunning})
		state.Phase = toolStatus.phase
		state.CurrentAction = toolStatus.action
		state.CurrentTarget = truncateLiveActivityText(toolStatus.target, 80)
		state.RequiresApp = toolStatus.requiresApp
		state.ShowsProgress = toolStatus.status != LiveActivityStatusNeedsApp
		state.LastError = ""
		state.LastToolName = strings.TrimSpace(event.ToolName)
		if app := toolStatus.app; app != "" {
			state.CurrentApp = truncateLiveActivityText(app, 40)
		}
		stepCandidates := []string{
			event.Content,
			toolStatus.step,
			formatToolStep("Using", event.ToolName),
		}
		if toolStatus.status == LiveActivityStatusNeedsApp {
			stepCandidates[0], stepCandidates[1] = stepCandidates[1], stepCandidates[0]
		}
		state.CurrentStep = truncateLiveActivityText(firstNonEmptyString(stepCandidates), 120)
		state.Progress = bumpLiveActivityProgress(state.Progress)
	case "tool_result":
		hasError := liveActivityEventHasError(event)
		state.LastToolName = strings.TrimSpace(event.ToolName)
		if !hasError && strings.EqualFold(strings.TrimSpace(event.ToolName), toolUserActionStep) {
			state.Status = LiveActivityStatusNeedsApp
			state.Phase = LiveActivityPhaseWaitingUser
			state.CurrentAction = "request_user_input"
			state.CurrentTarget = ""
			state.RequiresApp = false
			state.ShowsProgress = false
			state.LastError = ""
			state.CurrentStep = truncateLiveActivityText(firstNonEmptyString([]string{
				liveActivityHumanHandoffStep(event.Content),
				liveActivityHumanHandoffStep(event.ToolInput),
				"Please take over on the phone",
			}), 120)
		} else if hasError {
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
	if state := m.pauseForHumanHandoff(requestID, output); state != nil {
		return state
	}
	return m.finishTask(requestID, LiveActivityStatusCompleted, firstNonEmptyString([]string{
		truncateLiveActivityText(output, 120),
		"Completed",
	}), "")
}

func (m *LiveActivityManager) pauseForHumanHandoff(requestID, output string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	m.mu.Lock()
	state, ok := m.states[requestID]
	if !ok || (state.Phase != LiveActivityPhaseWaitingUser && !strings.EqualFold(state.LastToolName, toolUserActionStep)) {
		m.mu.Unlock()
		return nil
	}
	state.Status = LiveActivityStatusNeedsApp
	state.Phase = LiveActivityPhaseWaitingUser
	state.CurrentAction = "request_user_input"
	state.CurrentTarget = ""
	state.CurrentStep = truncateLiveActivityText(firstNonEmptyString([]string{
		output,
		state.CurrentStep,
		"Please take over on the phone",
	}), 120)
	state.Progress = 0
	state.ShowsProgress = false
	state.CanStop = false
	state.RequiresApp = false
	state.UpdatedAt = time.Now()
	state.EndedAt = nil
	m.states[requestID] = state
	m.activeRequestID = requestID
	m.mu.Unlock()
	m.publish(requestID, false)
	return &state
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

func (m *LiveActivityManager) SnapshotActiveForPhone(phoneID string) *LiveActivityState {
	if m == nil {
		return nil
	}
	phoneID = strings.TrimSpace(phoneID)
	if phoneID == "" {
		return m.SnapshotActive()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeRequestID != "" {
		if state, ok := m.states[m.activeRequestID]; ok && liveActivityStateMatchesPhoneID(state, phoneID) {
			return &state
		}
	}
	var latest *LiveActivityState
	for _, state := range m.states {
		if !liveActivityStateMatchesPhoneID(state, phoneID) {
			continue
		}
		candidate := state
		if latest == nil || candidate.UpdatedAt.After(latest.UpdatedAt) {
			latest = &candidate
		}
	}
	return latest
}

func (m *LiveActivityManager) publish(requestID string, final bool) {
	if m == nil {
		return
	}
	if m.logger != nil {
		m.logger.Info("Publishing live activity locally: %s, %t", requestID, final)
	}
	m.enqueueLocalUpdate()
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
		if m.activeRequestID == requestID {
			m.activeRequestID = ""
		}
	})
}

func liveActivityStateMatchesPhoneID(state LiveActivityState, phoneID string) bool {
	phoneID = strings.TrimSpace(phoneID)
	if phoneID == "" {
		return true
	}
	statePhoneID := strings.TrimSpace(state.PhoneID)
	return statePhoneID == "" || statePhoneID == phoneID
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
	status      string
	phase       string
	action      string
	step        string
	app         string
	target      string
	requiresApp bool
}

func liveActivityPhaseFromRole(content string) string {
	if speech.ExtractText(content) != "" {
		return LiveActivityPhaseAnswering
	}
	return LiveActivityPhasePlanning
}

func liveActivityActionFromRole(content string) string {
	if speech.ExtractText(content) != "" {
		return "answer"
	}
	return "think"
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
	case toolOpenApp:
		status.action = "open_app"
		status.app = liveActivityAppFromToolCall(event)
		if status.step == "" {
			status.step = "Opening app"
		}
		if target != "" {
			status.step = "Opening " + target
		}
	case toolOpenURL:
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "open_url"
		status.requiresApp = true
		status.app = liveActivityOpenURLApp(target)
		status.step = liveActivityOpenURLCallStep(target)
	case toolBridgeClipboard:
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "clipboard"
		status.requiresApp = true
		status.step = liveActivityClipboardStep(event.ToolInput)
	case toolBridgeCalendar:
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "calendar"
		status.requiresApp = true
		status.step = liveActivityActionStep(event.ToolInput, map[string]string{
			"create": "Creating calendar event",
			"query":  "Checking calendar",
			"delete": "Deleting calendar event",
		}, "Updating calendar")
	case toolBridgeContacts:
		status.phase = LiveActivityPhasePhoneBridge
		status.action = "contacts"
		status.requiresApp = true
		status.step = liveActivityActionStep(event.ToolInput, map[string]string{
			"query":  "Checking contacts",
			"create": "Creating contact",
			"update": "Updating contact",
		}, "Checking contacts")
	case toolBridgeNotification:
		payload, _ := liveActivityJSONObject(event.ToolInput)
		if strings.EqualFold(liveActivityString(payload, "action"), "query") {
			status.phase = LiveActivityPhaseVerifying
			status.action = "notification"
			status.step = "Checking notifications"
		} else {
			status.phase = LiveActivityPhasePhoneBridge
			status.action = "notification"
			status.requiresApp = true
			status.step = "Sending notification"
		}
	case "request_user_action":
		status.status = LiveActivityStatusNeedsApp
		status.phase = LiveActivityPhaseWaitingUser
		status.action = "request_user_input"
		status.step = firstNonEmptyString([]string{
			liveActivityHumanHandoffStep(event.ToolInput),
			"Please take over on the phone",
		})
	case "touch_gesture", "quick_action":
		status.action = "control_phone"
	case "mouse_move":
		status.action = "move_pointer"
	case "mouse_scroll":
		status.action = "scroll"
	case "keyboard_text", "enter_text":
		status.action = "type_text"
	case "keyboard_tap":
		status.action = "press_keys"
	case "web_search", "wikipedia", "web_scraper":
		status.action = "search"
	case "weather":
		status.action = "check_information"
	}
	if status.step == "" {
		status.step = formatToolStep("Using", tool)
	}
	return status
}

func liveActivityToolResultPhase(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "screenshot", "wait_for_stable_screen":
		return LiveActivityPhaseVerifying
	case toolOpenURL, toolBridgeClipboard, toolBridgeCalendar, toolBridgeContacts, toolBridgeNotification:
		return LiveActivityPhasePhoneBridge
	case "request_user_action":
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
	if event.ToolError != nil || event.IsError {
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
	if event.ToolError != nil && strings.TrimSpace(event.ToolError.Message) != "" {
		return strings.TrimSpace(event.ToolError.Message)
	}
	payload, ok := liveActivityJSONObject(event.Content)
	if ok {
		if value, ok := payload["error"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(event.Content)
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
	case toolOpenURL, toolBridgeClipboard, toolBridgeCalendar, toolBridgeContacts, toolBridgeNotification:
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

func liveActivityActionStep(input string, labels map[string]string, fallback string) string {
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

func liveActivityHumanHandoffStep(input string) string {
	payload, ok := liveActivityJSONObject(input)
	if !ok {
		return ""
	}
	return firstNonEmptyString([]string{
		liveActivityString(payload, "suggested_action"),
		liveActivityString(payload, "details"),
		liveActivityString(payload, "message"),
	})
}

func liveActivityTargetFromToolCall(event RunEvent) string {
	payload, ok := liveActivityJSONObject(event.ToolInput)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(event.ToolName)) {
	case toolOpenApp:
		return firstNonEmptyString([]string{liveActivityString(payload, "app"), liveActivityString(payload, "name")})
	case toolOpenURL:
		return liveActivityString(payload, "url")
	case toolBridgeCalendar:
		return firstNonEmptyString([]string{
			liveActivityString(payload, "title"),
			liveActivityString(payload, "from"),
			liveActivityString(payload, "event_id"),
		})
	case toolBridgeContacts:
		return firstNonEmptyString([]string{
			liveActivityString(payload, "name"),
			liveActivityString(payload, "query"),
			liveActivityString(payload, "contact_id"),
		})
	case toolBridgeNotification:
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
		if step := liveActivityStepFromJSONRoleOutput(content); step != "" {
			return step
		}
	}
	switch role {
	case "agent":
		return "Thinking"
	default:
		if content != "" && !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
			return content
		}
		return "Thinking"
	}
}

func liveActivityStepFromJSONRoleOutput(content string) string {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	if _, ok := payload["final_answer"]; ok {
		return "Preparing answer"
	}
	for _, key := range []string{"current_step", "next_step", "summary", "reason"} {
		if value, ok := payload[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
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

func liveActivityOpenURLKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.HasPrefix(value, "http://"), strings.HasPrefix(value, "https://"):
		return "web"
	case strings.HasPrefix(value, "sms:"):
		return "sms"
	case strings.HasPrefix(value, "mailto:"):
		return "email"
	case strings.HasPrefix(value, "tel:"):
		return "phone"
	default:
		return "link"
	}
}

func liveActivityOpenURLApp(value string) string {
	switch liveActivityOpenURLKind(value) {
	case "web":
		return "Browser"
	case "sms":
		return "Messages"
	case "email":
		return "Mail"
	case "phone":
		return "Phone"
	default:
		return ""
	}
}

func liveActivityOpenURLCallStep(value string) string {
	switch liveActivityOpenURLKind(value) {
	case "web":
		if value = strings.TrimSpace(value); value != "" {
			return "Opening " + value
		}
		return "Opening webpage"
	case "sms":
		return "Opening message composer"
	case "email":
		return "Opening email composer"
	case "phone":
		return "Opening phone"
	default:
		return "Opening link"
	}
}

func liveActivityToolCallStep(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "screenshot":
		return "Checking the screen"
	case "wait_for_stable_screen":
		return "Waiting for the screen"
	case toolOpenApp:
		return "Opening app"
	case toolOpenURL:
		return "Opening link"
	case "touch_gesture", "quick_action":
		return "Controlling the phone"
	case "mouse_move":
		return "Moving pointer"
	case "mouse_scroll":
		return "Scrolling"
	case "keyboard_text", "enter_text":
		return "Typing text"
	case "keyboard_tap":
		return "Pressing keys"
	case toolBridgeClipboard:
		return "Using clipboard"
	case toolBridgeCalendar:
		return "Updating calendar"
	case toolBridgeContacts:
		return "Checking contacts"
	case toolBridgeNotification:
		return "Using notifications"
	case "web_search", "wikipedia", "web_scraper":
		return "Searching"
	case "audio_volume":
		return "Adjusting audio"
	case "weather":
		return "Checking information"
	case "recall_memory", "recall_session_chunks", "recall_device_memory", "inspect_episode":
		return "Recalling context"
	case "save_memory", "forget_memory":
		return "Updating memory"
	case "skill_list", "skill_read", "skill_manage", "skill_mark_used":
		return "Using skills"
	case "request_user_action":
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
	case toolOpenApp:
		return "App opened"
	case toolOpenURL:
		return "Link opened"
	case "touch_gesture", "quick_action", "mouse_move", "mouse_scroll", "keyboard_tap", "keyboard_text", "enter_text":
		return "Action sent; checking result"
	case "request_user_action":
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
	tool := strings.ToLower(strings.TrimSpace(event.ToolName))
	if tool != toolOpenApp && tool != toolOpenURL {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(event.ToolInput)), &payload); err != nil {
		return ""
	}
	if value, ok := payload["app"].(string); ok {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	if value, ok := payload["name"].(string); ok {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	if value, ok := payload["url"].(string); ok && strings.TrimSpace(value) != "" {
		return liveActivityOpenURLApp(value)
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

func isCancelableLiveActivityStatus(status string) bool {
	switch status {
	case LiveActivityStatusRunning, LiveActivityStatusNeedsApp:
		return true
	default:
		return false
	}
}
