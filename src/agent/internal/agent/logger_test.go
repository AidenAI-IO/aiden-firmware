package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupOldLogFilesRemovesLogsOlderThanTwoDays(t *testing.T) {
	logDir := t.TempDir()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.Local)

	// Only llm-http logs are cleaned up
	writeTestLogFile(t, logDir, "llm-http-202606160900-session1.log", now)
	writeTestLogFile(t, logDir, "llm-http-202606171030-session2.log", now.Add(-72*time.Hour))
	writeTestLogFile(t, logDir, "llm-http-202606181445-session3.log", now.Add(-72*time.Hour))

	// These should not be touched
	writeTestLogFile(t, logDir, "agent-20260616.log", now)
	writeTestLogFile(t, logDir, "custom.log", now.Add(-72*time.Hour))
	writeTestLogFile(t, logDir, "llm-http-notadate.log", now)

	if err := cleanupOldLogFiles(logDir, now); err != nil {
		t.Fatalf("cleanupOldLogFiles() error = %v", err)
	}

	// Old llm-http logs should be removed
	assertPathMissing(t, filepath.Join(logDir, "llm-http-202606160900-session1.log"))

	// Recent llm-http logs should remain
	assertPathExists(t, filepath.Join(logDir, "llm-http-202606171030-session2.log"))
	assertPathExists(t, filepath.Join(logDir, "llm-http-202606181445-session3.log"))

	// Non-llm-http logs should not be touched
	assertPathExists(t, filepath.Join(logDir, "agent-20260616.log"))
	assertPathExists(t, filepath.Join(logDir, "custom.log"))
	assertPathExists(t, filepath.Join(logDir, "llm-http-notadate.log"))
}

func TestNewLoggerCleansOldLogFilesOnStartup(t *testing.T) {
	configDir := t.TempDir()
	logDir := filepath.Join(configDir, "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	// Create old llm-http log
	oldLog := filepath.Join(logDir, "llm-http-200001010000-session1.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0644); err != nil {
		t.Fatalf("write old log: %v", err)
	}

	logger, err := NewLogger(configDir)
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Old llm-http log should be cleaned up
	assertPathMissing(t, oldLog)
}

func writeTestLogFile(t *testing.T, dir, name string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, err=%v", path, err)
	}
}
