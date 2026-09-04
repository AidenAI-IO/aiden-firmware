package configweb

import (
	"encoding/json"
	"fmt"
	"net/http"

	"aiden-agent/internal/agent"
)

type storageController interface {
	Status() agent.StorageStatus
	Reconfigure(agent.StorageConfig) error
	SafeEject() error
	StartFormat(fs, confirm string) error
	Stop()
}

func (s *Server) initializeStorageManager() error {
	cfg, err := agent.LoadRuntimeConfig(s.options.AgentConfigPath)
	if err != nil {
		return fmt.Errorf("load Agent config: %w", err)
	}
	manager := agent.NewStorageManagerWithStatePath(cfg.Storage, s.options.StorageStatePath, nil)
	s.storageMu.Lock()
	if s.storage != nil {
		s.storageMu.Unlock()
		return nil
	}
	manager.Start()
	s.storage = manager
	s.storageMu.Unlock()
	return nil
}

func (s *Server) currentStorage() storageController {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	return s.storage
}

func (s *Server) reconfigureStorage() error {
	cfg, err := agent.LoadRuntimeConfig(s.options.AgentConfigPath)
	if err != nil {
		return fmt.Errorf("load Agent config: %w", err)
	}
	storage := s.currentStorage()
	if storage == nil {
		return s.initializeStorageManager()
	}
	if err := storage.Reconfigure(cfg.Storage); err != nil {
		return fmt.Errorf("apply storage config: %w", err)
	}
	return nil
}

func (s *Server) handleStorageStatus(w http.ResponseWriter, _ *http.Request) {
	storage := s.currentStorage()
	if storage == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	writeJSON(w, http.StatusOK, storage.Status())
}

func (s *Server) handleStorageEject(w http.ResponseWriter, _ *http.Request) {
	storage := s.currentStorage()
	if storage == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	if err := storage.SafeEject(); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, storage.Status())
}

func (s *Server) handleStorageFormat(w http.ResponseWriter, r *http.Request) {
	storage := s.currentStorage()
	if storage == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "storage manager unavailable")
		return
	}
	var request struct {
		FS      string `json:"fs"`
		Confirm string `json:"confirm"`
	}
	if !readJSONBody(w, r, &request) {
		return
	}
	if err := storage.StartFormat(request.FS, request.Confirm); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, storage.Status())
}

func storageStatusMap(status agent.StorageStatus) map[string]any {
	data, err := json.Marshal(status)
	if err != nil {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return value
}
