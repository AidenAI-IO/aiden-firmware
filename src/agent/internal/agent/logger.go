package agent

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const logRetention = 48 * time.Hour

// Logger provides structured logging to file and console
type Logger struct {
	file   *os.File
	logger *log.Logger
	mu     sync.Mutex
}

// NewLogger creates a new logger that writes to config/log/agent-YYYYMMDD.log
func NewLogger(configDir string) (*Logger, error) {
	logDir := filepath.Join(configDir, "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	// Create log file with date suffix
	logFile := filepath.Join(logDir, fmt.Sprintf("agent-%s.log", time.Now().Format("20060102")))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	// Write to both file and stderr
	multiWriter := io.MultiWriter(f, os.Stderr)
	logger := log.New(multiWriter, "", log.LstdFlags)
	log.SetOutput(multiWriter)
	log.SetFlags(log.LstdFlags)

	if err := cleanupOldLogFiles(logDir, time.Now()); err != nil {
		logger.Printf("[WARN] log cleanup failed: %v", err)
	}

	return &Logger{
		file:   f,
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
	for _, prefix := range []string{"agent-", "llm-raw-"} {
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		if len(date) != len("20060102") {
			continue
		}
		if parsed, err := time.ParseInLocation("20060102", date, time.Local); err == nil {
			return parsed
		}
	}
	return modTime
}

func (l *Logger) Close() error {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)
	if l.file != nil {
		return l.file.Close()
	}
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
