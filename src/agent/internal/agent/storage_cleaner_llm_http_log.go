package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LLMHTTPLogCleaner cleans up old LLM HTTP log files
type LLMHTTPLogCleaner struct {
	logDir           string
	retentionAge     time.Duration
	priority         int
	currentSessionID func() string
}

// NewLLMHTTPLogCleaner creates a new LLM HTTP log cleaner
func NewLLMHTTPLogCleaner(logDir string, retentionDays int, priority int) *LLMHTTPLogCleaner {
	return NewLLMHTTPLogCleanerWithSessionProvider(logDir, retentionDays, priority, nil)
}

// NewLLMHTTPLogCleanerWithSessionProvider protects the active session log from cleanup.
func NewLLMHTTPLogCleanerWithSessionProvider(logDir string, retentionDays int, priority int, currentSessionID func() string) *LLMHTTPLogCleaner {
	return &LLMHTTPLogCleaner{
		logDir:           logDir,
		retentionAge:     time.Duration(retentionDays) * 24 * time.Hour,
		priority:         priority,
		currentSessionID: currentSessionID,
	}
}

func (c *LLMHTTPLogCleaner) Name() string {
	return fmt.Sprintf("llm_http_log_%dd", int(c.retentionAge.Hours()/24))
}

func (c *LLMHTTPLogCleaner) Priority() int {
	return c.priority
}

func (c *LLMHTTPLogCleaner) EstimateReclaimable(context.Context) (uint64, error) {
	if c.logDir == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(c.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read log directory: %w", err)
	}

	cutoff := time.Now().Add(-c.retentionAge)
	var total uint64

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "llm-http-") {
			continue
		}
		if c.protected(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		logTime := logFileTime(entry.Name(), info.ModTime())
		if logTime.Before(cutoff) {
			total += uint64(info.Size())
		}
	}

	return total, nil
}

func (c *LLMHTTPLogCleaner) Clean(context.Context) (uint64, error) {
	if c.logDir == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(c.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read log directory: %w", err)
	}

	cutoff := time.Now().Add(-c.retentionAge)
	var totalFreed uint64
	var deletedCount int

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "llm-http-") {
			continue
		}
		if c.protected(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		logTime := logFileTime(entry.Name(), info.ModTime())
		if !logTime.Before(cutoff) {
			continue
		}

		path := filepath.Join(c.logDir, entry.Name())
		size := uint64(info.Size())

		if err := os.Remove(path); err != nil {
			return totalFreed, fmt.Errorf("remove %s: %w", entry.Name(), err)
		}

		totalFreed += size
		deletedCount++
	}

	if deletedCount > 0 {
		fmt.Fprintf(os.Stderr, "[storage_cleanup] deleted %d llm-http logs (retention: %dd), freed %d MB\n",
			deletedCount, int(c.retentionAge.Hours()/24), totalFreed/(1024*1024))
	}

	return totalFreed, nil
}

func (c *LLMHTTPLogCleaner) protected(name string) bool {
	if c.currentSessionID == nil {
		return false
	}
	sessionID := strings.TrimSpace(c.currentSessionID())
	return sessionID != "" && name == "llm-http-"+sessionID+".log"
}
