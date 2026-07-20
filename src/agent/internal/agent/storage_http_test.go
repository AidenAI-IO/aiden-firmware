package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStorageStatusHTTPReturnsConsistentSnapshot(t *testing.T) {
	sampler := &sequenceStorageSampler{samples: []StorageSample{storageSampleWithAvailableMB(40)}}
	config := DefaultStorageConfig()
	config.Cleanup.Enabled = false
	monitor := NewStorageMonitor(config, sampler, nil, nil, nil)
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("CheckAndRemediate() error = %v", err)
	}
	server := &Server{storageMonitor: monitor}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/storage/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Path           string       `json:"path"`
		AvailableMB    uint64       `json:"available_mb"`
		AlertLevel     StorageLevel `json:"alert_level"`
		StatusRevision uint64       `json:"status_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if response.Path != "/userdata" || response.AvailableMB != 40 || response.AlertLevel != StorageLevelWarning || response.StatusRevision != 1 {
		t.Fatalf("GET response = %+v", response)
	}
}

func TestStorageStatusHTTPCleanupHistoryUsesCleanerField(t *testing.T) {
	sampler := &sequenceStorageSampler{samples: []StorageSample{
		storageSampleWithAvailableMB(40),
		storageSampleWithAvailableMB(60),
	}}
	cleaner := &recordingStorageCleaner{name: "llm_http_log_7d", priority: 1, freed: storageMegabyte}
	monitor := NewStorageMonitor(DefaultStorageConfig(), sampler, nil, []StorageCleaner{cleaner}, nil)
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonManual}); err != nil {
		t.Fatalf("manual cleanup error = %v", err)
	}
	server := &Server{storageMonitor: monitor}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/storage/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		CleanupHistory []map[string]any `json:"cleanup_history"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if len(response.CleanupHistory) != 1 {
		t.Fatalf("cleanup_history = %+v, want one result", response.CleanupHistory)
	}
	if got := response.CleanupHistory[0]["cleaner"]; got != "llm_http_log_7d" {
		t.Fatalf("cleanup_history cleaner = %#v", got)
	}
	if _, exists := response.CleanupHistory[0]["name"]; exists {
		t.Fatalf("cleanup_history unexpectedly contains name: %+v", response.CleanupHistory[0])
	}
}

func TestStorageCleanupHTTPUsesMonitorRemediationFlow(t *testing.T) {
	sampler := &sequenceStorageSampler{samples: []StorageSample{
		storageSampleWithAvailableMB(40),
		storageSampleWithAvailableMB(60),
	}}
	cleaner := &recordingStorageCleaner{name: "llm_http_log_7d", priority: 1, freed: 7 * 1024 * 1024}
	config := DefaultStorageConfig()
	monitor := NewStorageMonitor(config, sampler, nil, []StorageCleaner{cleaner}, nil)
	server := &Server{storageMonitor: monitor}

	body := bytes.NewBufferString(`{"force":true,"targets":["llm_http_log"]}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/storage/cleanup", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST cleanup code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success         bool             `json:"success"`
		FreedMB         uint64           `json:"freed_mb"`
		FinalAlertLevel StorageLevel     `json:"final_alert_level"`
		AvailableMB     uint64           `json:"available_mb"`
		CleanersRun     []map[string]any `json:"cleaners_run"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if !response.Success || response.FreedMB != 7 || response.FinalAlertLevel != StorageLevelNormal || response.AvailableMB != 60 {
		t.Fatalf("POST response = %+v", response)
	}
	if len(response.CleanersRun) != 1 || response.CleanersRun[0]["name"] != "llm_http_log_7d" {
		t.Fatalf("cleaners_run = %+v", response.CleanersRun)
	}
	if _, exists := response.CleanersRun[0]["cleaner"]; exists {
		t.Fatalf("cleaners_run unexpectedly contains cleaner: %+v", response.CleanersRun[0])
	}
	if cleaner.calls != 1 {
		t.Fatalf("cleaner calls = %d, want 1", cleaner.calls)
	}
}

func TestStorageCleanupHTTPReportsOnlyCurrentOperationFreedSpace(t *testing.T) {
	sampler := &sequenceStorageSampler{samples: []StorageSample{
		storageSampleWithAvailableMB(40),
		storageSampleWithAvailableMB(60),
		storageSampleWithAvailableMB(60),
	}}
	cleaner := &recordingStorageCleaner{name: "llm_http_log_7d", priority: 1, freed: 7 * storageMegabyte}
	noOpCleaner := &recordingStorageCleaner{name: "audio_archive_30d", priority: 2, freed: 0}
	config := DefaultStorageConfig()
	monitor := NewStorageMonitor(config, sampler, nil, []StorageCleaner{cleaner, noOpCleaner}, nil)
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonManual}); err != nil {
		t.Fatalf("initial cleanup error = %v", err)
	}
	server := &Server{storageMonitor: monitor}

	body := bytes.NewBufferString(`{"targets":["audio_archive"]}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/storage/cleanup", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST cleanup code = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		FreedMB uint64 `json:"freed_mb"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.FreedMB != 0 {
		t.Fatalf("freed_mb = %d, want 0 for current no-op cleanup", response.FreedMB)
	}
}

func TestStorageCleanupHTTPRejectsTrailingJSON(t *testing.T) {
	monitor := NewStorageMonitor(DefaultStorageConfig(), &sequenceStorageSampler{
		samples: []StorageSample{storageSampleWithAvailableMB(60)},
	}, nil, nil, nil)
	server := &Server{storageMonitor: monitor}
	body := bytes.NewBufferString(`{"force":false}{"force":true}`)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/storage/cleanup", body))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST cleanup code = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestStorageCleanupHTTPRejectsUnknownTarget(t *testing.T) {
	cleaner := &recordingStorageCleaner{name: "llm_http_log_7d", priority: 1, freed: storageMegabyte}
	monitor := NewStorageMonitor(DefaultStorageConfig(), &sequenceStorageSampler{
		samples: []StorageSample{storageSampleWithAvailableMB(60)},
	}, nil, []StorageCleaner{cleaner}, nil)
	server := &Server{storageMonitor: monitor}
	body := bytes.NewBufferString(`{"targets":["unknown_cleanup"]}`)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/storage/cleanup", body))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST cleanup code = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if cleaner.calls != 0 {
		t.Fatalf("cleaner calls = %d after invalid target, want 0", cleaner.calls)
	}
}
