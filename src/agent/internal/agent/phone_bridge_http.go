package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// EnqueueCommandRequest is the request body for POST /api/phone-bridge/commands
type EnqueueCommandRequest struct {
	Command BridgeCommand `json:"command"`
}

// EnqueueCommandResponse is returned by POST /api/phone-bridge/commands
type EnqueueCommandResponse struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	QueuedAt  string `json:"queued_at"`
}

// PollCommandsResponse is returned by GET /api/phone-bridge/commands
type PollCommandsResponse struct {
	Commands   []BridgeCommand `json:"commands"`
	ServerTime string          `json:"server_time"`
}

// SubmitResultRequest is the request body for POST /api/phone-bridge/results
type SubmitResultRequest struct {
	BridgeCommandResponse
}

// SubmitResultResponse is returned by POST /api/phone-bridge/results
type SubmitResultResponse struct {
	Status string `json:"status"`
}

// GetResultResponse is returned by GET /api/phone-bridge/results/:command_id
type GetResultResponse struct {
	CommandID   string                 `json:"command_id"`
	Status      CommandStatus          `json:"status"`
	Result      *BridgeCommandResponse `json:"result,omitempty"`
	CompletedAt *string                `json:"completed_at,omitempty"`
}

// handleEnqueueCommand handles POST /api/phone-bridge/commands
// Enqueues a command from the agent to be picked up by the app.
// Returns 202 Accepted immediately, actual execution happens async.
func (pb *PhoneBridge) handleEnqueueCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req EnqueueCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: enqueue command decode failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, fmt.Sprintf(`{"error":"Invalid request: %v"}`, err), http.StatusBadRequest)
		return
	}

	if req.Command.ID == "" {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"command ID must not be empty"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Command.PhoneID) == "" {
		req.Command.PhoneID = pb.currentPhoneID()
	}
	status := pb.getStatus()
	if phoneBridgeUnsupportedBLEBackgroundCommand(status, req.Command.Type) {
		capabilityCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		bleWakeAvailable := pb.bleWakeAvailable(capabilityCtx)
		cancel()
		if toolErr := phoneBridgeUnsupportedBLEBackgroundError(status, req.Command.Type, bleWakeAvailable); toolErr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"error":      toolErr.Message,
				"tool_error": toolErr,
			})
			return
		}
	}

	if err := pb.queue.Enqueue(req.Command); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: enqueue command failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, ErrCommandExists) {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		}
		return
	}
	pb.notifyBLEWake(req.Command)

	cmd := pb.queue.Get(req.Command.ID)
	if cmd == nil {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"command not found after enqueue"}`, http.StatusInternalServerError)
		return
	}

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: command enqueued: id=%s type=%s", req.Command.ID, req.Command.Type)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(EnqueueCommandResponse{
		CommandID: req.Command.ID,
		Status:    "queued",
		QueuedAt:  cmd.QueuedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// handlePollCommands handles GET /api/phone-bridge/commands?platform=ios&limit=10
// Returns commands waiting to be executed by the app.
// Atomically marks returned commands as in_flight.
func (pb *PhoneBridge) handlePollCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	platform := r.URL.Query().Get("platform")
	if platform == "" {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"platform parameter required"}`, http.StatusBadRequest)
		return
	}

	limit := 10
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}

	phoneID := strings.TrimSpace(r.URL.Query().Get("phone_id"))
	pb.noteHTTPPollState(
		platform,
		phoneID,
		r.URL.Query().Get("app_state"),
		r.URL.Query().Get("pip_bridge_enabled"),
		r.URL.Query().Get("fgs_bridge_enabled"),
	)

	status := pb.getStatus()
	appState, appStateOK := normalizeAppState(r.URL.Query().Get("app_state"))
	if !appStateOK {
		appState = status.AppState
	}
	pipEnabled, pipEnabledOK := parseOptionalBoolQuery(r.URL.Query().Get("pip_bridge_enabled"))
	if !pipEnabledOK && status.PipBridgeEnabled != nil {
		pipEnabled = *status.PipBridgeEnabled
	}
	fgsEnabled, fgsEnabledOK := parseOptionalBoolQuery(r.URL.Query().Get("fgs_bridge_enabled"))
	if !fgsEnabledOK && status.FgsBridgeEnabled != nil {
		fgsEnabled = *status.FgsBridgeEnabled
	}

	var commands []BridgeCommand
	if !shouldSuppressHTTPCommandPoll(platform, appState, fgsEnabled) {
		commands = pb.queue.PollForPhoneMatching(platform, phoneID, limit, func(cmd BridgeCommand) bool {
			return phoneBridgeHTTPPollCommandAllowed(platform, appState, pipEnabled, fgsEnabled, cmd.Type)
		})
	}

	if pb.logger != nil && len(commands) > 0 {
		var cmdIDs []string
		for _, cmd := range commands {
			cmdIDs = append(cmdIDs, cmd.ID)
		}
		pb.logger.Info("phone-bridge: polled %d commands: %s", len(commands), strings.Join(cmdIDs, ", "))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PollCommandsResponse{
		Commands:   commands,
		ServerTime: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	})
}

func shouldSuppressHTTPCommandPoll(platform, appState string, fgsBridgeEnabled bool) bool {
	if strings.TrimSpace(strings.ToLower(platform)) != "android" {
		return false
	}
	return strings.TrimSpace(strings.ToLower(appState)) == "active" && !fgsBridgeEnabled
}

func (pb *PhoneBridge) noteHTTPPollState(platform, phoneID, appState, pipBridgeEnabled, fgsBridgeEnabled string) {
	platform = strings.TrimSpace(platform)
	phoneID = strings.TrimSpace(phoneID)
	appState, appStateOK := normalizeAppState(appState)
	enabled, enabledOK := parseOptionalBoolQuery(pipBridgeEnabled)
	fgsEnabled, fgsEnabledOK := parseOptionalBoolQuery(fgsBridgeEnabled)
	now := time.Now()

	pb.refreshHIDConnectionNow()
	pb.mu.Lock()
	pb.updateHIDConnectionPhoneLocked(phoneID)
	if platform != "" {
		pb.platform = platform
	}
	if phoneID != "" || !pb.connected {
		pb.phoneID = phoneID
	}
	if appStateOK {
		pb.appState = appState
		pb.appStateAt = now
	}
	if enabledOK {
		pb.pipBridgeEnabled = enabled
		pb.pipBridgeSeen = true
	}
	if fgsEnabledOK {
		pb.fgsBridgeEnabled = fgsEnabled
		pb.fgsBridgeSeen = true
		pb.fgsBridgeAt = now
	}
	pb.mu.Unlock()
	pb.notifyEnvironmentObserver()
}

func parseOptionalBoolQuery(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	default:
		return false, false
	}
}

// handleSubmitResult handles POST /api/phone-bridge/results
// Receives execution result from the app and stores it for agent retrieval.
func (pb *PhoneBridge) handleSubmitResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req SubmitResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: submit result decode failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, fmt.Sprintf(`{"error":"Invalid request: %v"}`, err), http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"result ID must not be empty"}`, http.StatusBadRequest)
		return
	}

	if err := pb.queue.SubmitResult(req.BridgeCommandResponse); err != nil {
		if pb.logger != nil {
			pb.logger.Error("phone-bridge: submit result failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		}
		return
	}

	if pb.logger != nil {
		ok := req.Error == nil
		pb.logger.Info("phone-bridge: result received: id=%s ok=%t", req.ID, ok)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SubmitResultResponse{
		Status: "acknowledged",
	})
}

// handleQueryResult handles GET /api/phone-bridge/results/:command_id
// Returns the current status and result (if completed) for a command.
func (pb *PhoneBridge) handleQueryResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Extract command_id from path: /api/phone-bridge/results/:command_id
	path := strings.TrimPrefix(r.URL.Path, "/api/phone-bridge/results/")
	commandID := strings.TrimSpace(path)
	if commandID == "" {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"command_id required"}`, http.StatusBadRequest)
		return
	}

	result, status := pb.queue.QueryResult(commandID)

	resp := GetResultResponse{
		CommandID: commandID,
		Status:    status,
	}

	if result != nil {
		resp.Result = &result.Response
		completedAt := result.CompletedAt.UTC().Format("2006-01-02T15:04:05Z")
		resp.CompletedAt = &completedAt
	}

	if pb.logger != nil {
		pb.logger.Info("phone-bridge: query result: id=%s status=%s", commandID, status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
