package agent

import (
	"fmt"
	"strings"
	"time"
)

// chat_history_memory.go: Helper functions for chat_history formatting.
//
// HISTORICAL NOTE: This file previously contained chatHistoryPlannerMemory and
// newChatHistoryPlannerMemory for injecting chat_history into planner context.
// That injection mechanism was disabled because:
// - Session system already provides comprehensive history management
// - Always-on injection created duplicate, uncompressed context
// - No compaction mechanism led to unbounded growth
//
// The wrapper type and injection functions have been removed. Only utility functions
// still used by other modules are preserved below.

// singleLineHistoryText converts multi-line text to a single line for compact display.
// Used by role_executor.go for prompt formatting.
func singleLineHistoryText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", " "), "\n", " ")
}

// titleHistoryLabel capitalizes the first letter of a label.
// Used internally by interruptedEpisodeStatusMessage.
func titleHistoryLabel(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] = runes[0] - ('a' - 'A')
	}
	return string(runes)
}

// interruptedEpisodeStatusMessage creates a chat_history message for an interrupted episode.
// Used by runtime.go when persisting interrupted episodes on startup.
func interruptedEpisodeStatusMessage(episode TaskEpisode) Message {
	return Message{
		Type:      "episode_status",
		EpisodeID: episode.ID,
		Status:    "interrupted",
		Content:   interruptedEpisodeHistoryContent(episode),
		IsError:   true,
		Timestamp: time.Now(),
	}
}

// interruptedEpisodeHistoryContent generates the content text for an interrupted episode message.
func interruptedEpisodeHistoryContent(episode TaskEpisode) string {
	parts := []string{
		fmt.Sprintf("Task episode %s was interrupted before completion; agent restarted before the task episode completed.", episode.ID),
	}
	if goal := strings.TrimSpace(episode.UserGoal); goal != "" {
		parts = append(parts, "Goal: "+goal)
	}
	if summary := summarizeLastEpisodeEventForHistory(episode.Events); summary != "" {
		parts = append(parts, "Last recorded step: "+summary)
	}
	return strings.Join(parts, "\n")
}

// summarizeLastEpisodeEventForHistory returns a summary of the last meaningful event in an episode.
func summarizeLastEpisodeEventForHistory(events []TaskEpisodeEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		switch event.Type {
		case "planner_decision":
			if nextStep := strings.TrimSpace(event.NextStep); nextStep != "" {
				return fmt.Sprintf("planner_decision next_step=%s", nextStep)
			}
		case "tool_call":
			if toolName := strings.TrimSpace(event.ToolName); toolName != "" {
				return fmt.Sprintf("tool_call tool=%s", toolName)
			}
		}
	}
	return ""
}
