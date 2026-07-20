package agent

import "path/filepath"

func newRuntimeStorageMonitor(cfg Config, logger *Logger, memories *MemoryManager) *StorageMonitor {
	storageConfig := cfg.Storage
	cleaners := make([]StorageCleaner, 0)
	priority := 1

	currentSessionID := func() string {
		if memories == nil {
			return ""
		}
		sessionID, err := memories.ActiveSessionID()
		if err != nil {
			return ""
		}
		return sessionID
	}
	logDir := ""
	sessionArchiveDir := ""
	if cfg.ConfigDir != "" {
		logDir = filepath.Join(cfg.ConfigDir, "log")
		sessionArchiveDir = filepath.Join(cfg.ConfigDir, "memory", "session_archive")
	}
	for _, retentionDays := range storageConfig.Cleanup.LLMHTTPLogRetentionDays {
		cleaners = append(cleaners, NewLLMHTTPLogCleanerWithSessionProvider(logDir, retentionDays, priority, currentSessionID))
		priority++
	}
	for _, retentionDays := range storageConfig.Cleanup.AudioArchiveRetentionDays {
		cleaners = append(cleaners, NewAudioArchiveCleaner(cfg.AudioArchive.StoragePathOrDefault(), retentionDays, 0, priority))
		priority++
	}
	for _, retentionDays := range storageConfig.Cleanup.SessionArchiveRetentionDays {
		cleaners = append(cleaners, NewSessionArchiveCleaner(sessionArchiveDir, retentionDays, 0, priority))
		priority++
	}
	return NewStorageMonitor(storageConfig, nil, logger, cleaners, nil)
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
