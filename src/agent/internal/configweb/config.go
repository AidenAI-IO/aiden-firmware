package configweb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func (s *Server) runAgentCLI(timeout time.Duration, input []byte, args ...string) commandResult {
	env, err := s.agentCommandEnvironment()
	if err != nil {
		return commandResult{ExitCode: 126, Output: []byte(err.Error())}
	}
	return runCommand(timeout, env, input, s.options.AgentBinary, args...)
}

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
	if err := json.Unmarshal(result.Output, &response); err != nil || response == nil {
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
	if _, exists := request["wifi"]; exists {
		writeJSONError(w, http.StatusBadRequest, "wifi updates are not supported by /api/config; use /api/wifi/connect or /api/wifi/forget")
		return
	}
	if raw, exists := request["apply_wifi"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
		writeJSONError(w, http.StatusBadRequest, "wifi updates are not supported by /api/config; use /api/wifi/connect or /api/wifi/forget")
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
	update, status, err := s.updateConfig(config)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}

	changed := stringSlice(update["changed_paths"])
	rebootRequired, _ := update["reboot_required"].(bool)
	frameServiceChanged := containsString(changed, "frame_service.keep_streamon")
	if frameServiceChanged {
		if err := s.restartFrameService(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(changed) > 0 && !rebootRequired {
		s.scheduleAgentRestart()
	}
	message := "config saved"
	if rebootRequired {
		message = "config saved; USB HID configuration changed; reboot required"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                              true,
		"config":                          update["config"],
		"changed_paths":                   changed,
		"reboot_required":                 rebootRequired,
		"usbhid_restart_required":         rebootRequired,
		"agent_restart_scheduled":         len(changed) > 0 && !rebootRequired,
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
	config, _ := json.Marshal(map[string]any{"agent": map[string]string{"locale": *request.Locale}})
	if _, status, err := s.updateConfig(config); err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	s.scheduleAgentRestart()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                      true,
		"locale":                  *request.Locale,
		"message":                 "locale saved; agent restarting",
		"agent_restart_scheduled": true,
	})
}

func (s *Server) restartFrameService() error {
	path := strings.TrimSpace(s.options.FrameServiceInitScript)
	if path == "" {
		return fmt.Errorf("frame service init script path is empty")
	}
	cmd := exec.Command(path, "restart")
	cmd.Env = append(os.Environ(), "AGENT_CONFIG="+s.options.AgentConfigPath)
	if err := cmd.Start(); err != nil {
		cmd = exec.Command("/bin/sh", path, "restart")
		cmd.Env = append(os.Environ(), "AGENT_CONFIG="+s.options.AgentConfigPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("launch frame service restart: %w", err)
		}
	}
	go func() { _ = cmd.Wait() }()
	return nil
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
