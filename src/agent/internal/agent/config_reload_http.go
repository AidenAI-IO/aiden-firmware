package agent

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type configReloadRequest struct {
	Revision uint64 `json:"revision"`
}

func configFileRevision(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(data)
	return h.Sum64()
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleInternalConfigReload is intentionally not part of the public Agent
// API. Config Web persists agent.toml and calls this loopback-only endpoint to
// make the new revision visible to the running runtime.
func (s *Server) handleInternalConfigReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if s.runtime == nil {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "runtime unavailable")
		return
	}
	if r.Method == http.MethodGet {
		writeAgentJSON(w, http.StatusOK, s.runtime.ConfigApplyStatus())
		return
	}
	current := s.runtime.ConfigSnapshot()
	var request configReloadRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Revision == 0 {
		writeAgentJSONError(w, http.StatusBadRequest, "reload request requires a nonzero revision")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAgentJSONError(w, http.StatusBadRequest, "invalid reload request")
		return
	}
	configPath := filepath.Join(current.ConfigDir, "agent.toml")
	revision := configFileRevision(configPath)
	if revision == 0 {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "config file unavailable")
		return
	}
	if request.Revision != revision {
		writeAgentJSONError(w, http.StatusConflict, fmt.Sprintf("stale config revision %d (current %d)", request.Revision, revision))
		return
	}
	cfg, err := LoadRuntimeConfigFromDir(current.ConfigDir)
	if err != nil {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "reload config: "+err.Error())
		return
	}
	if configFileRevision(configPath) != revision {
		writeAgentJSONError(w, http.StatusConflict, "config changed while loading; retry latest revision")
		return
	}
	// Preserve process-only fields; the persisted config cannot overwrite CLI overrides.
	cfg.SkillMergeModel = current.SkillMergeModel
	cfg.EnvironmentBridge = current.EnvironmentBridge
	cfg.Benchmark = current.Benchmark
	cfg.ForceSimpleLoop = current.ForceSimpleLoop
	if current.DeviceTypeOverride != "" {
		_ = cfg.OverrideDeviceType(current.DeviceTypeOverride)
	}
	status := s.runtime.QueueConfig(cfg, revision)
	writeAgentJSON(w, http.StatusAccepted, map[string]any{
		"ok": status.Error == "", "applied": false, "pending": status.Pending,
		"persisted": true, "revision": revision, "state": status.State, "error": status.Error,
		"reboot_required": status.RebootRequired,
		"runtime_id":      s.runtime.ConfigApplyStatus().RuntimeID,
	})
}

func writeAgentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAgentJSONError(w http.ResponseWriter, status int, message string) {
	writeAgentJSON(w, status, map[string]any{"ok": false, "applied": false, "error": message})
}
