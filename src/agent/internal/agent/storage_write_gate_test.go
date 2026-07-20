package agent

import (
	"context"
	"os"
	"testing"
)

func TestLLMRawHTTPLoggerHonorsStorageCapability(t *testing.T) {
	dir := t.TempDir()
	sampler := &sequenceStorageSampler{samples: []StorageSample{
		storageSampleWithAvailableMB(8),
		storageSampleWithAvailableMB(60),
	}}
	config := DefaultStorageConfig()
	config.Cleanup.Enabled = false
	monitor := NewStorageMonitor(config, sampler, nil, nil, nil)
	logger := newLLMRawHTTPLogger(dir, "session")
	logger.SetStorageMonitor(monitor)

	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("critical CheckAndRemediate() error = %v", err)
	}
	if err := logger.Log("model", "request", 200, `{}`); err != nil {
		t.Fatalf("Log() at critical error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("raw HTTP logger wrote %d files while capability unavailable", len(entries))
	}

	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("recovery CheckAndRemediate() error = %v", err)
	}
	if err := logger.Log("model", "request", 200, `{}`); err != nil {
		t.Fatalf("Log() after recovery error = %v", err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() after recovery error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("raw HTTP logger wrote %d files after recovery, want 1", len(entries))
	}
}

func TestMemoryManagerSkipsPersistenceAtCriticalAndResumesAfterRecovery(t *testing.T) {
	storageDir := t.TempDir()
	sampler := &sequenceStorageSampler{samples: []StorageSample{
		storageSampleWithAvailableMB(8),
		storageSampleWithAvailableMB(60),
	}}
	config := DefaultStorageConfig()
	config.Cleanup.Enabled = false
	monitor := NewStorageMonitor(config, sampler, nil, nil, nil)
	manager := NewMemoryManager(storageDir)
	manager.SetStorageMonitor(monitor)

	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("critical CheckAndRemediate() error = %v", err)
	}
	if err := manager.AppendMessages(context.Background(), "agent", []MessageRecord{{Role: "user", Content: "keep in memory"}}); err != nil {
		t.Fatalf("AppendMessages() at critical error = %v", err)
	}
	if _, err := os.Stat(manager.sessionEventsPath()); !os.IsNotExist(err) {
		t.Fatalf("session events were persisted at critical, stat error = %v", err)
	}

	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("recovery CheckAndRemediate() error = %v", err)
	}
	if err := manager.AppendMessages(context.Background(), "agent", []MessageRecord{{Role: "user", Content: "persist now"}}); err != nil {
		t.Fatalf("AppendMessages() after recovery error = %v", err)
	}
	if _, err := os.Stat(manager.sessionEventsPath()); err != nil {
		t.Fatalf("session events not persisted after recovery: %v", err)
	}
}

func TestMemoryManagerSkipsSessionArchiveAtCritical(t *testing.T) {
	storageDir := t.TempDir()
	sampler := &sequenceStorageSampler{samples: []StorageSample{storageSampleWithAvailableMB(8)}}
	config := DefaultStorageConfig()
	config.Cleanup.Enabled = false
	monitor := NewStorageMonitor(config, sampler, nil, nil, nil)
	manager := NewMemoryManager(storageDir)
	manager.SetStorageMonitor(monitor)
	if err := manager.AppendMessages(context.Background(), "agent", []MessageRecord{{Role: "user", Content: "current session"}}); err != nil {
		t.Fatalf("AppendMessages() before critical state error = %v", err)
	}
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("critical CheckAndRemediate() error = %v", err)
	}

	result, err := manager.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if result.ArchiveDir != "" {
		t.Fatalf("ArchiveDir = %q at critical, want archive skipped", result.ArchiveDir)
	}
	if _, err := os.Stat(manager.sessionEventsPath()); err != nil {
		t.Fatalf("active session was removed while archive capability unavailable: %v", err)
	}
}
