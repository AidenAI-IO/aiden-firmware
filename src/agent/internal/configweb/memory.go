package configweb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// handleMemoryReset exposes the product-level reset action from Config Web.
// The memory implementation lives in the Agent process, so Config Web proxies
// the request to the Agent's existing clear-all endpoint and returns its result
// without exposing the Agent service topology to the browser.
func (s *Server) handleMemoryReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	base, err := s.agentBaseURL()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	target, err := url.Parse(strings.TrimRight(base.String(), "/") + "/api/clear-all")
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "agent memory reset endpoint is invalid")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), nil)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "create agent memory reset request: "+err.Error())
		return
	}
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("agent memory reset request failed: %v", err))
		return
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxRequestBodySize+1))
	if readErr != nil || len(body) > maxRequestBodySize || !json.Valid(body) {
		writeJSONError(w, http.StatusBadGateway, "agent memory reset returned invalid JSON")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		message, _ := payload["error"].(string)
		if message == "" {
			message = fmt.Sprintf("agent memory reset failed (HTTP %d)", response.StatusCode)
		}
		writeJSONError(w, response.StatusCode, message)
		return
	}
	if err := s.scheduleAgentRestart(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "memory was reset, but Agent restart failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_restart_scheduled": true})
}
