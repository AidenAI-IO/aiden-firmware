package agent

import (
	"encoding/json"
	"net/http"
)

func (s *Server) storageManager() *StorageManager {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.Storage()
}

func writeStorageError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleStorageStatus serves GET /api/storage/status.
func (s *Server) handleStorageStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeStorageError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sm := s.storageManager()
	if sm == nil {
		writeStorageError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sm.Status())
}

// handleStorageEject serves POST /api/storage/eject.
func (s *Server) handleStorageEject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeStorageError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sm := s.storageManager()
	if sm == nil {
		writeStorageError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	if err := sm.SafeEject(); err != nil {
		writeStorageError(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sm.Status())
}

// handleStorageFormat serves POST /api/storage/format
// {"fs": "fat32"|"ext4", "confirm": "format-sd-card"}. The format runs as an
// asynchronous job; poll /api/storage/status (format_job) for the outcome.
func (s *Server) handleStorageFormat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeStorageError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sm := s.storageManager()
	if sm == nil {
		writeStorageError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	var req struct {
		FS      string `json:"fs"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStorageError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := sm.StartFormat(req.FS, req.Confirm); err != nil {
		writeStorageError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(sm.Status())
}
