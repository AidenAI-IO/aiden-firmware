package agent

import (
	"context"
	"strings"

	"github.com/tmc/langchaingo/llms"
	langmemory "github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type conversationMessagePlannerMemory struct {
	inner schema.Memory
}

func newConversationMessagePlannerMemory(inner schema.Memory) schema.Memory {
	if inner == nil {
		return inner
	}
	return &conversationMessagePlannerMemory{inner: inner}
}

func (m *conversationMessagePlannerMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *conversationMessagePlannerMemory) MemoryVariables(ctx context.Context) []string {
	return m.inner.MemoryVariables(ctx)
}

func (m *conversationMessagePlannerMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if values == nil {
		return values, nil
	}
	if key := m.inner.GetMemoryKey(ctx); key != "" {
		if _, ok := values[key]; ok {
			values[key] = ""
		}
	}
	return values, nil
}

func (m *conversationMessagePlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *conversationMessagePlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

type conversationHistorySelectionEntry struct {
	message   llms.MessageContent
	tokens    int
	protected bool
}

func runtimeConversationHistoryMessageContents(ctx context.Context, history *langmemory.ChatMessageHistory, manager *MemoryManager, currentRunID string, maxTokens int) ([]llms.MessageContent, error) {
	if manager != nil && strings.TrimSpace(manager.storageDir) != "" {
		events, err := manager.LoadActiveSessionEvents(ctx, 0)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			if activeTokens := sumSessionEventTokensExcludingRun(events, currentRunID); maxTokens > 0 && activeTokens > maxTokens {
				manager.RaisePromptTokenFloor(activeTokens)
			}
			return conversationHistoryMessageContentsFromEvents(events, currentRunID, maxTokens), nil
		}
	}
	messages, err := conversationHistoryMessageContents(ctx, history)
	if err != nil {
		return nil, err
	}
	return selectConversationHistoryWithinBudget(messageSelectionEntries(messages), maxTokens), nil
}

func conversationHistoryMessageContents(ctx context.Context, history *langmemory.ChatMessageHistory) ([]llms.MessageContent, error) {
	if history == nil {
		return nil, nil
	}
	messages, err := history.Messages(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]llms.MessageContent, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.GetContent())
		if content == "" {
			continue
		}
		if converted, ok := messageContentFromChatMessage(message.GetType(), content); ok {
			result = append(result, converted)
		}
	}
	return result, nil
}

func conversationHistoryMessageContentsFromEvents(events []SessionEvent, currentRunID string, maxTokens int) []llms.MessageContent {
	entries := make([]conversationHistorySelectionEntry, 0, len(events))
	firstUserInputIndex := firstSelectableUserInputIndex(events, currentRunID)
	for i, event := range events {
		if eventBelongsToRun(event, currentRunID) {
			continue
		}
		record, ok := messageRecordFromSessionEvent(event)
		if !ok {
			continue
		}
		content := strings.TrimSpace(record.Content)
		if content == "" {
			continue
		}
		message, ok := messageContentFromChatMessage(llms.ChatMessageType(record.Role), content)
		if !ok {
			continue
		}
		entries = append(entries, conversationHistorySelectionEntry{
			message:   message,
			tokens:    estimateMessageTokens(message),
			protected: isProtectedConversationHistoryEvent(event, i, firstUserInputIndex),
		})
	}
	return selectConversationHistoryWithinBudget(entries, maxTokens)
}

func firstSelectableUserInputIndex(events []SessionEvent, currentRunID string) int {
	for i, event := range events {
		if eventBelongsToRun(event, currentRunID) {
			continue
		}
		if event.Type == "user_input" && strings.TrimSpace(event.Content) != "" {
			return i
		}
	}
	return -1
}

func eventBelongsToRun(event SessionEvent, runID string) bool {
	return strings.TrimSpace(runID) != "" && event.RunID == runID
}

func sumSessionEventTokensExcludingRun(events []SessionEvent, runID string) int {
	total := 0
	for _, event := range events {
		if eventBelongsToRun(event, runID) {
			continue
		}
		total += estimateSessionEventTokens(event)
	}
	return total
}

func isProtectedConversationHistoryEvent(event SessionEvent, index int, firstUserInputIndex int) bool {
	switch event.Source {
	case EventSourcePinnedRoot, EventSourceCompactionPrefix:
		return true
	}
	return index == firstUserInputIndex
}

func messageSelectionEntries(messages []llms.MessageContent) []conversationHistorySelectionEntry {
	entries := make([]conversationHistorySelectionEntry, 0, len(messages))
	firstUserIndex := -1
	for i, message := range messages {
		if firstUserIndex < 0 && message.Role == llms.ChatMessageTypeHuman {
			firstUserIndex = i
		}
	}
	for i, message := range messages {
		entries = append(entries, conversationHistorySelectionEntry{
			message:   message,
			tokens:    estimateMessageTokens(message),
			protected: i == firstUserIndex || message.Role == llms.ChatMessageTypeSystem,
		})
	}
	return entries
}

func selectConversationHistoryWithinBudget(entries []conversationHistorySelectionEntry, maxTokens int) []llms.MessageContent {
	if len(entries) == 0 {
		return nil
	}
	if maxTokens <= 0 {
		result := make([]llms.MessageContent, 0, len(entries))
		for _, entry := range entries {
			result = append(result, entry.message)
		}
		return result
	}
	total := 0
	for _, entry := range entries {
		total += entry.tokens
	}
	if total <= maxTokens {
		result := make([]llms.MessageContent, 0, len(entries))
		for _, entry := range entries {
			result = append(result, entry.message)
		}
		return result
	}

	selected := make(map[int]llms.MessageContent)
	usedTokens := 0
	for i, entry := range entries {
		if !entry.protected {
			continue
		}
		message, tokens, ok := fitConversationHistoryMessage(entry.message, entry.tokens, maxTokens-usedTokens)
		if !ok {
			continue
		}
		selected[i] = message
		usedTokens += tokens
	}

	trimmedRecent := false
	for i := len(entries) - 1; i >= 0; i-- {
		if _, ok := selected[i]; ok {
			continue
		}
		remaining := maxTokens - usedTokens
		if remaining <= 0 {
			break
		}
		entry := entries[i]
		if entry.tokens <= remaining {
			selected[i] = entry.message
			usedTokens += entry.tokens
			continue
		}
		if trimmedRecent {
			continue
		}
		message, tokens, ok := fitConversationHistoryMessage(entry.message, entry.tokens, remaining)
		if !ok {
			continue
		}
		selected[i] = message
		usedTokens += tokens
		trimmedRecent = true
	}

	result := make([]llms.MessageContent, 0, len(selected))
	for i, entry := range entries {
		message, ok := selected[i]
		if !ok {
			continue
		}
		if len(message.Parts) == 0 {
			message = entry.message
		}
		result = append(result, message)
	}
	return result
}

func fitConversationHistoryMessage(message llms.MessageContent, tokens int, budget int) (llms.MessageContent, int, bool) {
	if budget <= 0 {
		return llms.MessageContent{}, 0, false
	}
	if tokens <= budget {
		return message, tokens, true
	}
	trimmed, ok := trimTextMessageContentToTokenBudget(message, budget)
	if !ok {
		return llms.MessageContent{}, 0, false
	}
	return trimmed, estimateMessageTokens(trimmed), true
}

func trimTextMessageContentToTokenBudget(message llms.MessageContent, budget int) (llms.MessageContent, bool) {
	if len(message.Parts) != 1 {
		return llms.MessageContent{}, false
	}
	text, ok := message.Parts[0].(llms.TextContent)
	if !ok {
		return llms.MessageContent{}, false
	}
	roleOverhead := 4 + estimateTextTokens(string(message.Role))
	available := budget - roleOverhead
	if available <= 0 {
		return llms.MessageContent{}, false
	}
	trimmed := trimTextToTokenBudget(text.Text, available)
	if strings.TrimSpace(trimmed) == "" {
		return llms.MessageContent{}, false
	}
	return llms.TextParts(message.Role, trimmed), true
}

func trimTextToTokenBudget(text string, budget int) string {
	text = strings.TrimSpace(text)
	if budget <= 0 || text == "" {
		return ""
	}
	if estimateTextTokens(text) <= budget {
		return text
	}
	const marker = "\n[history truncated to fit context budget]"
	markerTokens := estimateTextTokens(marker)
	if budget <= markerTokens {
		return ""
	}
	limit := budget - markerTokens
	runes := []rune(text)
	lo, hi := 0, len(runes)
	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if estimateTextTokens(string(runes[:mid])) <= limit {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best <= 0 {
		return ""
	}
	return strings.TrimSpace(string(runes[:best])) + marker
}

func messageContentFromChatMessage(role llms.ChatMessageType, content string) (llms.MessageContent, bool) {
	switch role {
	case llms.ChatMessageTypeHuman, llms.ChatMessageTypeAI, llms.ChatMessageTypeSystem, llms.ChatMessageTypeGeneric:
		return llms.TextParts(role, content), true
	default:
		return llms.MessageContent{}, false
	}
}
