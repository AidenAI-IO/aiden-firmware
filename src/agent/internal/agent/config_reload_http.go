package agent

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/url"
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

func isLoopbackOrConfigWeb(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("X-Aiden-Internal")) == "config-web" {
		return true
	}
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
	if !isLoopbackOrConfigWeb(r) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if s.runtime == nil {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "runtime unavailable")
		return
	}
	s.runtime.configReloadMu.Lock()
	defer s.runtime.configReloadMu.Unlock()
	var request configReloadRequest
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024))
		if err := decoder.Decode(&request); err != nil && !strings.Contains(err.Error(), "EOF") {
			writeAgentJSONError(w, http.StatusBadRequest, "invalid reload request")
			return
		}
	}
	configPath := filepath.Join(s.runtime.config.ConfigDir, "agent.toml")
	revision := configFileRevision(configPath)
	if revision == 0 {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "config file unavailable")
		return
	}
	if request.Revision != 0 && request.Revision != revision {
		writeAgentJSONError(w, http.StatusConflict, fmt.Sprintf("stale config revision %d (current %d)", request.Revision, revision))
		return
	}
	cfg, err := LoadRuntimeConfigFromDir(s.runtime.config.ConfigDir)
	if err != nil {
		writeAgentJSONError(w, http.StatusServiceUnavailable, "reload config: "+err.Error())
		return
	}
	// Skill merge models and other runtime-only dependencies are not persisted
	// in TOML; retain the initialized dependency while replacing config values.
	cfg.SkillMergeModel = s.runtime.config.SkillMergeModel
	s.runtime.config = cfg
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

// handleConfigWebCORS applies a narrow CORS policy for the page hosted by the
// separate Config Web process. It echoes only an explicitly configured origin
// or the same device host on the well-known local portal ports; no wildcard is
// ever emitted. It returns true when an OPTIONS preflight was answered.
func handleConfigWebCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	allowed := false
	for _, configured := range strings.Split(os.Getenv("AIDEN_CONFIG_WEB_ORIGINS"), ",") {
		if strings.TrimSpace(configured) == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		parsed, err := url.Parse(origin)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
			host := r.Host
			if value, _, err := net.SplitHostPort(host); err == nil {
				host = value
			} else {
				host = strings.Trim(host, "[]")
			}
			if parsed.Hostname() == host {
				port := parsed.Port()
				configuredPort := strings.TrimSpace(os.Getenv("AIDEN_CONFIG_WEB_PORT"))
				allowed = port == "" || port == "80" || port == "8000" || (configuredPort != "" && port == configuredPort)
			}
		}
	}
	if !allowed {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Add("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Aiden-Internal")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}
