package agent

import (
	"encoding/json"
	"net/http"
	"time"
)

type storageCleanupResultHTTP struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	Name      string    `json:"name"`
	FreedMB   uint64    `json:"freed_mb"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

type storageStatusHTTPResponse struct {
	Path                    string                     `json:"path"`
	TotalMB                 uint64                     `json:"total_mb"`
	UsedMB                  uint64                     `json:"used_mb"`
	AvailableMB             uint64                     `json:"available_mb"`
	PercentUsed             float64                    `json:"percent_used"`
	AlertLevel              StorageLevel               `json:"alert_level"`
	DegradedMode            bool                       `json:"degraded_mode"`
	UnavailableCapabilities []StorageCapability        `json:"unavailable_capabilities"`
	StatusRevision          uint64                     `json:"status_revision"`
	LastCleanup             *time.Time                 `json:"last_cleanup,omitempty"`
	LastCleanupFreedMB      uint64                     `json:"last_cleanup_freed_mb"`
	CleanupHistory          []storageCleanupResultHTTP `json:"cleanup_history,omitempty"`
}

func storageStatusResponse(status StorageStatus) storageStatusHTTPResponse {
	response := storageStatusHTTPResponse{
		Path:                    status.Path,
		TotalMB:                 status.TotalBytes / storageMegabyte,
		UsedMB:                  status.UsedBytes / storageMegabyte,
		AvailableMB:             status.AvailableBytes / storageMegabyte,
		PercentUsed:             status.PercentUsed,
		AlertLevel:              status.Level,
		DegradedMode:            len(status.UnavailableCapabilities) > 0,
		UnavailableCapabilities: append([]StorageCapability(nil), status.UnavailableCapabilities...),
		StatusRevision:          status.Revision,
		LastCleanupFreedMB:      status.LastCleanupFreedBytes / storageMegabyte,
		CleanupHistory:          storageCleanupResultsHTTP(status.CleanupHistory),
	}
	if !status.LastCleanupAt.IsZero() {
		lastCleanup := status.LastCleanupAt
		response.LastCleanup = &lastCleanup
	}
	return response
}

func (s *Server) handleStorageStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.storageMonitor == nil {
		http.Error(w, "storage monitor unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(storageStatusResponse(s.storageMonitor.Status()))
}

type storageCleanupHTTPRequest struct {
	Force   bool     `json:"force"`
	Targets []string `json:"targets"`
}

type storageCleanupHTTPResponse struct {
	Success         bool                       `json:"success"`
	FreedMB         uint64                     `json:"freed_mb"`
	FinalAlertLevel StorageLevel               `json:"final_alert_level"`
	AvailableMB     uint64                     `json:"available_mb"`
	CleanersRun     []storageCleanupResultHTTP `json:"cleaners_run,omitempty"`
}

func (s *Server) handleStorageCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.storageMonitor == nil {
		http.Error(w, "storage monitor unavailable", http.StatusServiceUnavailable)
		return
	}
	var request storageCleanupHTTPRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid storage cleanup request", http.StatusBadRequest)
		return
	}
	status, err := s.storageMonitor.CheckAndRemediate(r.Context(), StorageCheckRequest{
		Reason:  CheckReasonManual,
		Force:   request.Force,
		Targets: request.Targets,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := storageCleanupHTTPResponse{
		Success:         true,
		FreedMB:         status.CurrentCleanupFreedBytes / storageMegabyte,
		FinalAlertLevel: status.Level,
		AvailableMB:     status.AvailableBytes / storageMegabyte,
		CleanersRun:     storageCleanupResultsHTTP(status.LastCleanupResults),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func storageCleanupResultsHTTP(results []StorageCleanupResult) []storageCleanupResultHTTP {
	converted := make([]storageCleanupResultHTTP, 0, len(results))
	for _, result := range results {
		converted = append(converted, storageCleanupResultHTTP{
			Timestamp: result.Timestamp,
			Name:      result.Cleaner,
			FreedMB:   result.FreedBytes / storageMegabyte,
			Status:    result.Status,
			Error:     result.Error,
		})
	}
	return converted
}
