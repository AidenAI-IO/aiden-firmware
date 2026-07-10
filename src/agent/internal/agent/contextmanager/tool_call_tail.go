package contextmanager

import "strings"

const interruptedToolResultContent = "Tool execution was interrupted before a result was recorded."

// repairToolCallTailBeforeAppend enforces the tool-call protocol only at the
// append boundary. If the last persisted message is a tool call, the first
// incoming message must be its result. Otherwise an interrupted result is
// persisted before the incoming batch closes the tool call.
func repairToolCallTailBeforeAppend(existing, incoming []Message) []Message {
	if len(existing) == 0 || len(incoming) == 0 {
		return incoming
	}

	lastIndex := len(existing) - 1
	last := existing[lastIndex]
	if last.Role != MessageRoleToolCall || len(last.ToolCalls) == 0 {
		return incoming
	}

	call, ok := lastPendingToolCall(last, lastIndex)
	if !ok {
		return incoming
	}
	if normalized, ok := normalizeMatchingToolResult(incoming[0], call); ok {
		result := cloneMessages(incoming)
		result[0] = normalized
		return result
	}

	result := make([]Message, 0, len(incoming)+1)
	result = append(result, interruptedToolResult(call))
	result = append(result, incoming...)
	return result
}

type pendingToolCall struct {
	id                string
	name              string
	originalIDMissing bool
}

func lastPendingToolCall(message Message, messageIndex int) (pendingToolCall, bool) {
	for toolIndex, call := range message.ToolCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		originalID := strings.TrimSpace(call.ID)
		return pendingToolCall{
			id:                toolCallIDOrFallback(originalID, messageIndex, toolIndex),
			name:              name,
			originalIDMissing: originalID == "",
		}, true
	}
	return pendingToolCall{}, false
}

func normalizeMatchingToolResult(message Message, call pendingToolCall) (Message, bool) {
	if message.Role != MessageRoleToolResult || len(message.ToolResults) != 1 {
		return Message{}, false
	}

	toolResult := message.ToolResults[0]
	resultID := strings.TrimSpace(toolResult.ToolCallID)
	resultName := strings.TrimSpace(toolResult.Name)
	idMatches := resultID == call.id
	legacyMatches := call.originalIDMissing && (resultName == "" || resultName == call.name)
	if !idMatches && !legacyMatches {
		return Message{}, false
	}

	toolResult.ToolCallID = call.id
	if resultName == "" {
		toolResult.Name = call.name
	}
	message.ToolResults = []ToolResult{toolResult}
	return message, true
}

func interruptedToolResult(call pendingToolCall) Message {
	return Message{
		Role: MessageRoleToolResult,
		ToolResults: []ToolResult{{
			ToolCallID: call.id,
			Name:       call.name,
			Content:    interruptedToolResultContent,
		}},
	}
}
