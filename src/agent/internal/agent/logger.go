package agent

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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

	return &Logger{
		file:   f,
		logger: logger,
	}, nil
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
