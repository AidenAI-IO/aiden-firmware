package agent

import (
	"context"
	"path/filepath"

	"aiden-agent/internal/agent/agentpath"
	"aiden-agent/internal/agent/contextmanager"
)

type leveledStorageCleaner struct {
	StorageCleaner
	minimumLevel StorageLevel
}

func (c leveledStorageCleaner) MinimumLevel() StorageLevel { return c.minimumLevel }

func (c leveledStorageCleaner) ForceClean(ctx context.Context) (uint64, error) {
	if cleaner, ok := c.StorageCleaner.(forceStorageCleaner); ok {
		return cleaner.ForceClean(ctx)
	}
	return c.StorageCleaner.Clean(ctx)
}

func withMinimumStorageLevel(cleaner StorageCleaner, level StorageLevel) StorageCleaner {
	return leveledStorageCleaner{StorageCleaner: cleaner, minimumLevel: level}
}

func newRuntimeStorageMonitor(cfg Config, logger *Logger, memories *MemoryManager) *StorageMonitor {
	storageConfig := cfg.Storage.MonitorConfig()
	cleaners := make([]StorageCleaner, 0)
	priority := 1
	if cfg.ConfigDir != "" {
		artifactCleaner := contextmanager.NewArtifactStoreCleaner(agentpath.ContextManagerSessionFolder(cfg.ConfigDir), priority)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(artifactCleaner, StorageLevelNormal))
	}

	currentSessionID := func() (string, error) {
		if memories == nil {
			return "", nil
		}
		return memories.ActiveSessionID()
	}
	logDir := ""
	sessionArchiveDir := ""
	if cfg.ConfigDir != "" {
		logDir = filepath.Join(cfg.ConfigDir, "log")
		sessionArchiveDir = filepath.Join(cfg.ConfigDir, "memory", "session_archive")
	}

	regularLLM, aggressiveLLMDays := splitStorageRetentionDays(storageConfig.Cleanup.LLMHTTPLogRetentionDays)
	regularAudio, aggressiveAudioDays := splitStorageRetentionDays(storageConfig.Cleanup.AudioArchiveRetentionDays)
	regularSessions, aggressiveSessionDays := splitStorageRetentionDays(storageConfig.Cleanup.SessionArchiveRetentionDays)

	for index, retentionDays := range regularLLM {
		cleaner := NewLLMHTTPLogCleanerWithCheckedSessionProvider(logDir, retentionDays, priority, currentSessionID)
		priority++
		minimumLevel := StorageLevelCritical
		if index == 0 {
			minimumLevel = StorageLevelNormal
		} else if index == 1 {
			minimumLevel = StorageLevelWarning
		}
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, minimumLevel))
	}

	for index, retentionDays := range regularAudio {
		minimumLevel := StorageLevelWarning
		maxFiles := 10
		if index > 0 {
			minimumLevel = StorageLevelCritical
			maxFiles = 3
		}
		cleaner := NewAudioArchiveCleaner(cfg.AudioArchive.StoragePathOrDefault(), retentionDays, maxFiles, priority)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, minimumLevel))
	}

	for _, retentionDays := range regularSessions {
		cleaner := NewSessionArchiveCleaner(sessionArchiveDir, retentionDays, 0, priority)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, StorageLevelWarning))
	}

	for range aggressiveLLMDays {
		cleaner := NewLLMHTTPLogCleanerWithCheckedSessionProvider(logDir, 0, priority, currentSessionID)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, StorageLevelEmergency))
	}
	for range aggressiveAudioDays {
		cleaner := NewAudioArchiveCleaner(cfg.AudioArchive.StoragePathOrDefault(), 0, 0, priority)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, StorageLevelEmergency))
	}
	for range aggressiveSessionDays {
		cleaner := NewSessionArchiveCleaner(sessionArchiveDir, 0, 0, priority)
		priority++
		cleaners = append(cleaners, withMinimumStorageLevel(cleaner, StorageLevelEmergency))
	}
	monitor := NewStorageMonitor(storageConfig, nil, logger, cleaners, nil)
	monitor.SetLevelStatePath("/run/agent/storage_level")
	if logger != nil {
		logger.SetStorageMonitor(monitor)
	}
	return monitor
}

func splitStorageRetentionDays(days []int) (regular []int, aggressive []int) {
	for _, day := range days {
		if day == 0 {
			aggressive = append(aggressive, day)
		} else {
			regular = append(regular, day)
		}
	}
	return regular, aggressive
}

// StartStorageMonitor runs the startup check and begins periodic monitoring.
func (r *Runtime) StartStorageMonitor() error {
	if r == nil || r.storageMonitor == nil {
		return nil
	}
	return r.storageMonitor.Start()
}

// StorageMonitor returns the runtime storage monitor for integration seams.
func (r *Runtime) StorageMonitor() *StorageMonitor {
	if r == nil {
		return nil
	}
	return r.storageMonitor
}

// SetVoiceNotificationSink connects the runtime monitor to the global notification manager.
func (r *Runtime) SetVoiceNotificationSink(sink VoiceNotificationSink) {
	if r != nil && r.storageMonitor != nil {
		r.storageMonitor.SetVoiceNotificationSink(sink)
	}
}
