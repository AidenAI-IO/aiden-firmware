package agent

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const logRetention = 48 * time.Hour

// Logger provides structured logging to stdout/stderr
// Output is captured by the init script and written to /var/log/agent/agent.log
type Logger struct {
	logger *log.Logger
	mu     sync.Mutex
}

// NewLogger creates a new logger that writes to stdout/stderr
// The init script redirects output to /var/log/agent/agent.log
func NewLogger(configDir string) (*Logger, error) {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	// Cleanup old llm-http logs in configDir if set
	if configDir != "" {
		llmLogDir := filepath.Join(configDir, "log")
		if err := cleanupOldLogFiles(llmLogDir, time.Now()); err != nil {
			logger.Printf("[WARN] log cleanup failed: %v", err)
		}
	}

	return &Logger{
		logger: logger,
	}, nil
}

func cleanupOldLogFiles(logDir string, now time.Time) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("read log directory: %w", err)
	}
	cutoff := now.Add(-logRetention)
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		// Only clean up llm-http logs (not agent.log)
		if !strings.HasPrefix(entry.Name(), "llm-http-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("stat log file %q: %w", entry.Name(), err))
			continue
		}
		logTime := logFileTime(entry.Name(), info.ModTime())
		if !logTime.Before(cutoff) {
			continue
		}
		path := filepath.Join(logDir, entry.Name())
		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("remove old log file %q: %w", entry.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func logFileTime(name string, modTime time.Time) time.Time {
	// Only parse llm-http logs
	if !strings.HasPrefix(name, "llm-http-") || !strings.HasSuffix(name, ".log") {
		return modTime
	}
	// Extract datetime from llm-http-YYYYMMDDHHMM-*.log
	parts := strings.TrimPrefix(name, "llm-http-")
	parts = strings.TrimSuffix(parts, ".log")
	if len(parts) < 12 {
		return modTime
	}
	dateTimeStr := parts[:12]
	if parsed, err := time.ParseInLocation("200601021504", dateTimeStr, time.Local); err == nil {
		return parsed.Add(24 * time.Hour)
	}
	return modTime
}

func (l *Logger) Close() error {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)
	return nil
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf("[INFO] "+format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf("[ERROR] "+format, args...)
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf("[DEBUG] "+format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf("[WARN] "+format, args...)
}
