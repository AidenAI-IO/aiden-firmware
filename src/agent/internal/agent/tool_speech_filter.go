package agent

import "strings"

func shouldSpeakToolCallContent(toolName string, content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	switch strings.TrimSpace(toolName) {
	case "recall_memory", "screenshot":
		return false
	default:
		return true
	}
}
