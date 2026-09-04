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
	if r.Method != http.MethodPost {
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
	s.runtime.configReloadMu.Lock()
	defer s.runtime.configReloadMu.Unlock()
	current := s.runtime.ConfigSnapshot()
	var request configReloadRequest
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024))
		if err := decoder.Decode(&request); err != nil && err != io.EOF {
			writeAgentJSONError(w, http.StatusBadRequest, "invalid reload request")
			return
		}
	}
	configPath := filepath.Join(current.ConfigDir, "agent.toml")
	revision := configFileRevision(configPath)
	if revision == 0 {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "config file unavailable")
		return
	}
	if request.Revision != 0 && request.Revision != revision {
		writeAgentJSONError(w, http.StatusConflict, fmt.Sprintf("stale config revision %d (current %d)", request.Revision, revision))
		return
	}
	cfg, err := LoadRuntimeConfigFromDir(current.ConfigDir)
	if err != nil {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "reload config: "+err.Error())
		return
	}
	// Preserve command-line-only runtime fields before asking Runtime to apply the
	// snapshot. Runtime rejects changes once its provider/audio/storage
	// dependencies are initialized, so a failed apply leaves the old snapshot
	// active and tells Config Web that a restart is required.
	cfg.SkillMergeModel = current.SkillMergeModel
	cfg.EnvironmentBridge = current.EnvironmentBridge
	cfg.Benchmark = current.Benchmark
	cfg.ForceSimpleLoop = current.ForceSimpleLoop
	if err := s.runtime.ApplyConfigSnapshot(cfg); err != nil {
		writeAgentJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "applied": false, "restart_required": true,
			"persisted": true, "revision": revision,
			"error": "apply config: " + err.Error(),
		})
		return
	}
	writeAgentJSON(w, http.StatusOK, map[string]any{
		"ok": true, "applied": true, "persisted": true, "revision": revision,
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
