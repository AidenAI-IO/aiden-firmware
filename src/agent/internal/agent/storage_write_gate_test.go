package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if !manager.PersistencePending() {
		t.Fatal("PersistencePending() = false after skipped critical write")
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
	if manager.PersistencePending() {
		t.Fatal("PersistencePending() = true after successful recovery write")
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

func TestMemoryManagerSkipsBackgroundMaintenanceAtCritical(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	manager.SetLastPromptTokens(600)

	storageConfig := DefaultStorageConfig()
	storageConfig.Cleanup.Enabled = false
	monitor := NewStorageMonitor(storageConfig, &sequenceStorageSampler{
		samples: []StorageSample{storageSampleWithAvailableMB(8)},
	}, nil, nil, nil)
	manager.SetStorageMonitor(monitor)
	if _, err := monitor.CheckAndRemediate(ctx, StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("critical CheckAndRemediate() error = %v", err)
	}

	manager.RequestMaintenance()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := manager.WaitMaintenance(waitCtx); err != nil {
		t.Fatalf("WaitMaintenance() error = %v", err)
	}
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("background maintenance wrote %d chunks at critical", len(chunks))
	}
	if !manager.PersistencePending() {
		t.Fatal("PersistencePending() = false after skipped background maintenance")
	}
}
