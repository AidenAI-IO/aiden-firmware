package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/langchaingo/schema"
)

const (
	maxPlannerChatHistoryMessages     = 20
	maxPlannerChatHistoryContentRunes = 1200
	maxPlannerChatHistoryRunes        = 7000
)

const hotWindowHistoryLabel = "Current session recent history (earlier history is compressed into the session summary):"

// hotWindowContextMemory labels the rendered hot-window history when earlier
// session events have been compressed. The label is a prompt-only hint and is
// never persisted into ChatMessageHistory or session events.
type hotWindowContextMemory struct {
	inner        schema.Memory
	compressedFn func() bool
}

func newHotWindowContextMemory(inner schema.Memory, compressedFn func() bool) schema.Memory {
	if inner == nil || compressedFn == nil {
		return inner
	}
	return &hotWindowContextMemory{inner: inner, compressedFn: compressedFn}
}

func (m *hotWindowContextMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *hotWindowContextMemory) MemoryVariables(ctx context.Context) []string {
	return m.inner.MemoryVariables(ctx)
}

func (m *hotWindowContextMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if !m.compressedFn() {
		return values, nil
	}
	key := m.inner.GetMemoryKey(ctx)
	existingValue, ok := values[key]
	if !ok {
		return values, nil
	}
	existing, ok := existingValue.(string)
	if !ok {
		return values, nil
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return values, nil
	}
	values[key] = hotWindowHistoryLabel + "\n" + existing
	return values, nil
}

func (m *hotWindowContextMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *hotWindowContextMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

type chatHistoryPlannerMemory struct {
	inner schema.Memory
	store *ChatHistoryStore
}

func newChatHistoryPlannerMemory(inner schema.Memory, store *ChatHistoryStore) schema.Memory {
	if inner == nil || store == nil {
		return inner
	}
	return &chatHistoryPlannerMemory{inner: inner, store: store}
}

func (m *chatHistoryPlannerMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *chatHistoryPlannerMemory) MemoryVariables(ctx context.Context) []string {
	return m.inner.MemoryVariables(ctx)
}

func (m *chatHistoryPlannerMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	messages, err := m.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	extra := formatChatHistoryForPlanner(messages, currentInputFromMemoryInputs(inputs))
	if strings.TrimSpace(extra) == "" {
		return values, nil
	}
	if values == nil {
		values = map[string]any{}
	}
	key := m.inner.GetMemoryKey(ctx)
	existingValue, hasExisting := values[key]
	existing, ok := existingValue.(string)
	if hasExisting && !ok {
		return values, nil
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		values[key] = "Recent persisted chat history:\n" + extra
	} else {
		values[key] = existing + "\n\nRecent persisted chat history:\n" + extra
	}
	return values, nil
}

func (m *chatHistoryPlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *chatHistoryPlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

func chatHistoryStoreForConfigDir(configDir string) *ChatHistoryStore {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return nil
	}
	return NewChatHistoryStore(filepath.Join(configDir, "memory", "chat_history"))
}

func formatChatHistoryForPlanner(messages []Message, currentInput string) string {
	if len(messages) == 0 {
		return ""
	}
	currentInput = strings.TrimSpace(currentInput)
	currentUserIndex := latestMatchingUserMessageIndex(messages, currentInput)

	completedEpisodes := map[string]bool{}
	for _, message := range messages {
		if message.Type != "assistant" {
			continue
		}
		if episodeID := strings.TrimSpace(message.EpisodeID); episodeID != "" {
			completedEpisodes[episodeID] = true
		}
	}

	var lines []string
	for i, message := range messages {
		if !chatHistoryMessageUsefulForPlanner(message, completedEpisodes) {
			continue
		}
		if i == currentUserIndex {
			continue
		}
		line := formatChatHistoryMessageLine(message)
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > maxPlannerChatHistoryMessages {
		lines = lines[len(lines)-maxPlannerChatHistoryMessages:]
	}
	return truncateChatHistoryRunes(strings.Join(lines, "\n"), maxPlannerChatHistoryRunes)
}

func latestMatchingUserMessageIndex(messages []Message, currentInput string) int {
	if currentInput == "" {
		return -1
	}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Type == "user" && strings.TrimSpace(message.Content) == currentInput {
			return i
		}
	}
	return -1
}

func chatHistoryMessageUsefulForPlanner(message Message, completedEpisodes map[string]bool) bool {
	episodeID := strings.TrimSpace(message.EpisodeID)
	switch message.Type {
	case "episode_status":
		return !strings.EqualFold(strings.TrimSpace(message.Status), "completed")
	case "user", "role_output", "tool_call":
		return episodeID != "" && !completedEpisodes[episodeID]
	default:
		return false
	}
}

func formatChatHistoryMessageLine(message Message) string {
	episodeSuffix := ""
	if episodeID := strings.TrimSpace(message.EpisodeID); episodeID != "" {
		episodeSuffix = " (episode " + episodeID + ")"
	}
	content := truncateChatHistoryRunes(strings.TrimSpace(message.Content), maxPlannerChatHistoryContentRunes)
	switch message.Type {
	case "episode_status":
		status := strings.TrimSpace(message.Status)
		if status == "" {
			status = "unknown"
		}
		if content == "" {
			content = "status=" + status
		}
		return "Task episode" + episodeSuffix + " [" + status + "]: " + singleLineHistoryText(content)
	case "user":
		if content == "" {
			return ""
		}
		return "User" + episodeSuffix + ": " + singleLineHistoryText(content)
	case "role_output":
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "role"
		}
		if content == "" {
			return ""
		}
		return titleHistoryLabel(role) + episodeSuffix + ": " + singleLineHistoryText(content)
	case "tool_call":
		toolName := strings.TrimSpace(message.ToolName)
		if toolName == "" {
			toolName = "tool"
		}
		description := firstNonEmptyString([]string{message.Description, content, message.ToolInput})
		if strings.TrimSpace(description) == "" {
			return "Tool call" + episodeSuffix + ": " + toolName
		}
		return "Tool call" + episodeSuffix + ": " + toolName + " - " + singleLineHistoryText(truncateChatHistoryRunes(description, maxPlannerChatHistoryContentRunes))
	default:
		return ""
	}
}

func currentInputFromMemoryInputs(inputs map[string]any) string {
	if inputs == nil {
		return ""
	}
	value, ok := inputs["input"]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func singleLineHistoryText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func titleHistoryLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func interruptedEpisodeStatusMessage(episode TaskEpisode) Message {
	timestamp := time.Now()
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(episode.EndedAt)); err == nil {
		timestamp = parsed
	}
	return Message{
		Type:      "episode_status",
		EpisodeID: episode.ID,
		Status:    "interrupted",
		Content:   interruptedEpisodeHistoryContent(episode),
		Timestamp: timestamp,
		IsError:   true,
	}
}

func interruptedEpisodeHistoryContent(episode TaskEpisode) string {
	var lines []string
	episodeID := strings.TrimSpace(episode.ID)
	if episodeID != "" {
		lines = append(lines, "Task episode "+episodeID+" was interrupted before completion.")
	} else {
		lines = append(lines, "Task episode was interrupted before completion.")
	}
	if goal := strings.TrimSpace(episode.UserGoal); goal != "" {
		lines = append(lines, "Goal: "+truncateChatHistoryRunes(goal, maxPlannerChatHistoryContentRunes))
	}
	if last := summarizeLastEpisodeEventForHistory(episode.Events); last != "" {
		lines = append(lines, "Last recorded step: "+last)
	}
	reason := firstNonEmptyString([]string{episode.Outcome.FailureReason, firstNonEmptyString(episode.FailureCauses)})
	if reason != "" {
		lines = append(lines, "Reason: "+truncateChatHistoryRunes(reason, maxPlannerChatHistoryContentRunes))
	}
	if startedAt := strings.TrimSpace(episode.StartedAt); startedAt != "" {
		lines = append(lines, "Started at: "+startedAt)
	}
	return strings.Join(lines, "\n")
}

func summarizeLastEpisodeEventForHistory(events []TaskEpisodeEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if next := strings.TrimSpace(event.NextStep); next != "" {
			return event.Type + " next_step=" + truncateChatHistoryRunes(next, maxPlannerChatHistoryContentRunes)
		}
		if toolName := strings.TrimSpace(event.ToolName); toolName != "" {
			description := firstNonEmptyString([]string{event.ToolDescription, event.ToolInput, event.Observation})
			if description == "" {
				return event.Type + " tool=" + toolName
			}
			return event.Type + " tool=" + toolName + " detail=" + truncateChatHistoryRunes(description, maxPlannerChatHistoryContentRunes)
		}
		if content := strings.TrimSpace(event.Content); content != "" {
			return event.Type + " content=" + truncateChatHistoryRunes(content, maxPlannerChatHistoryContentRunes)
		}
		if reason := strings.TrimSpace(event.Reason); reason != "" {
			return event.Type + " reason=" + truncateChatHistoryRunes(reason, maxPlannerChatHistoryContentRunes)
		}
	}
	return ""
}
