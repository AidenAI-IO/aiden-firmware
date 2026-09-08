package configweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"aiden-agent/internal/agent"
)

func (s *Server) runAgentCLI(timeout time.Duration, input []byte, args ...string) commandResult {
	env, err := s.agentCommandEnvironment()
	if err != nil {
		return commandResult{ExitCode: 126, Output: []byte(err.Error())}
	}
	return runCommand(timeout, env, input, s.options.AgentBinary, args...)
}

// handleGetConfig returns only the persisted agent.toml projection. Device,
// Wi-Fi, firmware and environment state live behind the explicit snapshot
// resource so callers can evolve each resource independently.
func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	result := s.runAgentCLI(5*time.Second, nil, "config", "--config="+s.options.AgentConfigPath, "--format=json")
	if result.TimedOut {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config timed out")
		return
	}
	if result.ExitCode != 0 {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config unavailable")
		return
	}
	var config map[string]any
	if err := json.Unmarshal(result.Output, &config); err != nil || config == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config returned invalid JSON")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": config})
}

func (s *Server) handleGetDeviceSnapshot(w http.ResponseWriter, _ *http.Request) {
	result := s.runAgentCLI(5*time.Second, nil, "config", "--config="+s.options.AgentConfigPath, "--format=json")
	if result.TimedOut {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config timed out")
		return
	}
	if result.ExitCode != 0 {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config unavailable")
		return
	}
	var config map[string]any
	if err := json.Unmarshal(result.Output, &config); err != nil || config == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent config returned invalid JSON")
		return
	}
	wifi, wifiErr := loadWiFiConfig(s.options.WiFiConfigPath)
	systemEnv := ""
	if data, err := readFileLimited(s.options.SystemEnvPath, maxSystemEnvSize); err == nil {
		systemEnv = string(data)
	}
	response := map[string]any{
		"ok":           true,
		"config":       config,
		"wifi":         wifi.publicValue(),
		"wifi_status":  s.queryWiFiStatus(),
		"agent_status": s.queryAgentStatus(),
		"firmware":     s.firmwareInfo(),
		"system_env":   systemEnv,
		"storage":      s.storageStatusValue(),
		"paths": map[string]string{
			"agent_config":   s.options.AgentConfigPath,
			"wifi_config":    s.options.WiFiConfigPath,
			"wifi_interface": s.options.WiFiInterface,
			"system_env":     s.options.SystemEnvPath,
		},
	}
	if wifiErr != nil && !os.IsNotExist(wifiErr) {
		response["wifi_error"] = wifiErr.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleConfigMeta(w http.ResponseWriter, _ *http.Request) {
	result := s.runAgentCLI(5*time.Second, nil, "config-meta", "--format=json")
	if result.TimedOut || result.ExitCode != 0 || !json.Valid(result.Output) {
		message := "config metadata unavailable"
		if result.ExitCode == 127 {
			message = "agent config unavailable: agent binary not found"
		}
		writeJSONError(w, http.StatusServiceUnavailable, message)
		return
	}
	var metadata any
	if err := json.Unmarshal(result.Output, &metadata); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "config metadata unavailable")
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (s *Server) updateConfig(config json.RawMessage) (map[string]any, int, error) {
	body, err := json.Marshal(map[string]json.RawMessage{"config": config})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	result := s.runAgentCLI(10*time.Second, body, "config-update", "--config="+s.options.AgentConfigPath, "--stdin", "--format=json")
	if result.TimedOut {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("agent config update timed out")
	}
	var response map[string]any
	decoder := json.NewDecoder(bytes.NewReader(result.Output))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || response == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("agent config update returned invalid JSON")
	}
	ok, _ := response["ok"].(bool)
	if result.ExitCode != 0 || !ok {
		message, _ := response["error"].(string)
		if message == "" {
			message = "agent config update rejected"
		}
		status := http.StatusBadRequest
		if result.ExitCode == 127 {
			status = http.StatusServiceUnavailable
		} else if response["error_kind"] == "internal" {
			status = http.StatusInternalServerError
		}
		return nil, status, fmt.Errorf("%s", message)
	}
	return response, http.StatusOK, nil
}

func (s *Server) handlePostConfig(w http.ResponseWriter, r *http.Request) {
	var request map[string]json.RawMessage
	if !readJSONBody(w, r, &request) {
		return
	}
	if request == nil {
		writeJSONError(w, http.StatusBadRequest, "request body must be an object")
		return
	}
	for key := range request {
		if key != "config" && key != "wifi" && key != "apply_wifi" {
			writeJSONError(w, http.StatusBadRequest, "only the 'config' field is accepted")
			return
		}
	}
	if _, exists := request["wifi"]; exists {
		writeJSONError(w, http.StatusBadRequest, "wifi updates are not supported by /api/config; use /api/network/wifi/connection")
		return
	}
	if raw, exists := request["apply_wifi"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		writeJSONError(w, http.StatusBadRequest, "wifi updates are not supported by /api/config; use /api/network/wifi/connection")
		return
	}
	config := request["config"]
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(config, &object); err != nil || object == nil {
		writeJSONError(w, http.StatusBadRequest, "config patch must be an object")
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	update, status, err := s.updateConfig(config)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}

	changed := stringSlice(update["changed_paths"])
	rebootRequired, _ := update["reboot_required"].(bool)
	revision := uint64Value(update["revision"])
	persisted, persistedField := update["persisted"].(bool)
	if !persistedField {
		// The current config-update contract must report persistence explicitly.
		writeJSONError(w, http.StatusServiceUnavailable, "agent config update omitted persisted state")
		return
	}
	frameServiceChanged := containsString(changed, "frame_service.keep_streamon")
	if frameServiceChanged {
		s.frameApplyPending = true
	}
	_, storageRequested := object["storage"]
	if hasConfigPathPrefix(changed, "storage") || storageRequested {
		s.storageApplyPending = true
	}
	if err := s.applyConfigServices(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "config": update["config"], "persisted": true, "applied": false, "state": "failed", "error": err.Error(), "reboot_required": rebootRequired, "agent_restart_scheduled": false})
		return
	}
	applied, pending := false, false
	payload, reloadErr := s.reloadAgentConfig(r.Context(), revision)
	if reloadErr != nil {
		s.configApplyError = reloadErr.Error()
		// Attribute the failure to the Agent process that was expected to
		// apply it; a restarted Agent has already booted the persisted config.
		s.configApplyErrorAgent = s.agentRuntimeID
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "config": update["config"], "persisted": persisted, "applied": false,
			"revision": revision, "changed_paths": changed, "reboot_required": rebootRequired,
			"restart_required": rebootRequired, "agent_restart_scheduled": false,
			"state": "failed", "error": reloadErr.Error(),
		})
		return
	}
	if id, _ := payload["runtime_id"].(string); id != "" {
		s.agentRuntimeID = id
	}
	applied, _ = payload["applied"].(bool)
	pending, _ = payload["pending"].(bool)
	if required, ok := payload["reboot_required"].(bool); ok {
		rebootRequired = required
	}
	message := "config saved"
	if rebootRequired {
		message = "config saved; USB HID configuration changed; reboot required"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                              true,
		"config":                          update["config"],
		"persisted":                       persisted,
		"applied":                         applied,
		"pending":                         pending,
		"state":                           payload["state"],
		"revision":                        revision,
		"changed_paths":                   changed,
		"reboot_required":                 rebootRequired,
		"restart_required":                rebootRequired,
		"restart_reasons":                 update["restart_reasons"],
		"usbhid_restart_required":         rebootRequired,
		"agent_restart_scheduled":         false,
		"ota_restart_scheduled":           false,
		"usb_reenumeration_scheduled":     false,
		"frame_service_restart_scheduled": frameServiceChanged,
		"message":                         message,
	})
}

func (s *Server) handlePutLocale(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Locale *string `json:"locale"`
	}
	if !readJSONBody(w, r, &request) {
		return
	}
	if request.Locale == nil {
		writeJSONError(w, http.StatusBadRequest, "missing locale string")
		return
	}
	if *request.Locale != "zh-CN" && *request.Locale != "en-US" {
		writeJSONError(w, http.StatusBadRequest, "unsupported locale; expected zh-CN or en-US")
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config, _ := json.Marshal(map[string]any{"agent": map[string]string{"locale": *request.Locale}})
	update, status, err := s.updateConfig(config)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	if err := s.applyConfigServices(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "persisted": true, "applied": false, "state": "failed", "locale": *request.Locale, "error": err.Error()})
		return
	}
	revision := uint64Value(update["revision"])
	payload, err := s.reloadAgentConfig(r.Context(), revision)
	if err != nil {
		s.configApplyError = err.Error()
		s.configApplyErrorAgent = s.agentRuntimeID
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "persisted": true, "applied": false, "locale": *request.Locale,
			"agent_restart_scheduled": false, "revision": revision, "error": err.Error(),
		})
		return
	}
	if id, _ := payload["runtime_id"].(string); id != "" {
		s.agentRuntimeID = id
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "locale": *request.Locale, "persisted": true, "applied": payload["applied"],
		"pending": payload["pending"], "state": payload["state"], "reboot_required": payload["reboot_required"], "revision": revision, "message": "locale saved",
	})
}

// applyConfigServices runs under configMu and retains failed work for an
// unchanged retry, including a save made through the locale endpoint.
func (s *Server) applyConfigServices() error {
	var failures []string
	if s.frameApplyPending {
		if err := s.restartFrameService(); err != nil {
			failures = append(failures, err.Error())
		} else {
			s.frameApplyPending = false
		}
	}
	if s.storageApplyPending {
		if err := s.reconfigureStorage(); err != nil {
			failures = append(failures, err.Error())
		} else {
			s.storageApplyPending = false
		}
	}
	s.configApplyError = strings.Join(failures, "; ")
	s.configApplyErrorAgent = ""
	if s.configApplyError != "" {
		return fmt.Errorf("%s", s.configApplyError)
	}
	return nil
}

func (s *Server) restartFrameService() error {
	path := strings.TrimSpace(s.options.FrameServiceInitScript)
	if path == "" {
		return fmt.Errorf("frame service init script path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "restart")
	cmd.Env = append(os.Environ(), "AGENT_CONFIG="+s.options.AgentConfigPath)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("frame service restart: %w", ctx.Err())
		}
		if _, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("frame service restart: %w", err)
		}
		cmd = exec.CommandContext(ctx, "/bin/sh", path, "restart")
		cmd.Env = append(os.Environ(), "AGENT_CONFIG="+s.options.AgentConfigPath)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("frame service restart: %w", err)
		}
	}
	cfg, err := agent.LoadRuntimeConfig(s.options.AgentConfigPath)
	if err != nil {
		return fmt.Errorf("read frame service config: %w", err)
	}
	return waitForFrameService(ctx, cfg.HID.FrameSocketOrDefault())
}

func waitForFrameService(ctx context.Context, socket string) error {
	// The init script launches a watchdog before the HDMI service starts.
	// TC358743 EDID negotiation alone takes several seconds.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "unix", socket)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("frame service not ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func uint64Value(value any) uint64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := strconv.ParseUint(string(typed), 10, 64)
		return parsed
	case float64:
		if typed < 0 || typed > float64(^uint64(0)) {
			return 0
		}
		return uint64(typed)
	case uint64:
		return typed
	case string:
		parsed, _ := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func hasConfigPathPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if value == prefix || strings.HasPrefix(value, prefix+".") {
			return true
		}
	}
	return false
}
