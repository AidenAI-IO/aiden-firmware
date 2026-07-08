package context_manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fetchCurrentSession fetches the current session ID from the session folder, should be called when initializing the context manager.
func fetchCurrentSession(sessionFolder string) string {
	// sessionFolder/.current_session content is the session ID
	sessionID := ""
	sessionIDFile := filepath.Join(sessionFolder, ".current_session")
	if _, err := os.Stat(sessionIDFile); err == nil {
		content, err := os.ReadFile(sessionIDFile)
		if err == nil {
			sessionID = strings.TrimSpace(string(content))
		}
	}
	return sessionID
}

func saveCurrentSession(sessionFolder string, sessionID string) error {
	sessionIDFile := filepath.Join(sessionFolder, ".current_session")
	return os.WriteFile(sessionIDFile, []byte(sessionID), 0o644)
}

func appendSession(sessionFolder string, sessionID string, messages []Message) error {
	// append to JSONL file, each message is a new line
	sessionFile := filepath.Join(sessionFolder, sessionID + ".jsonl")
	file, err := os.OpenFile(sessionFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open session file %s: %w", sessionFile, err)
	}
	defer file.Close()
	// write each message as a new line
	for _, message := range messages {
		json, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
		if _, err := file.WriteString(string(json) + "\n"); err != nil {
			return fmt.Errorf("failed to write message to session file %s: %w", sessionFile, err)
		}
		// sync after each write
		if err := file.Sync(); err != nil {
			return fmt.Errorf("failed to sync session file %s: %w", sessionFile, err)
		}
	}
	return nil
}

func loadSession(sessionFolder string, sessionID string) ([]Message, error) {
	sessionFile := filepath.Join(sessionFolder, sessionID + ".jsonl")
	if _, err := os.Stat(sessionFile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat session file %s: %w", sessionFile, err)
	}
	content, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file %s: %w", sessionFile, err)
	}
	lines := strings.Split(string(content), "\n")
	messageList := make([]Message, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var message Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message from session file %s: %w", sessionFile, err)
		}
		messageList = append(messageList, message)
	}
	return messageList, nil
}