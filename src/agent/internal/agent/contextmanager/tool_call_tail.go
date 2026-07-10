package contextmanager

import "strings"

const interruptedToolResultContent = "Tool execution was interrupted before a result was recorded."

type pendingToolCall struct {
	id                string
	name              string
	originalIDMissing bool
	matched           bool
}

// repairToolCallTailBeforeAppend enforces the tool-call protocol at the point
// where a new message would close the current tail. A matching tool result is
// normalized and appended normally. If a user, state, assistant, or new tool
// call arrives while the previous tool-call group is incomplete, synthetic
// interrupted results are persisted before the new message.
//
// Only the contiguous tool-call group at the tail is inspected. Normal appends
// therefore remain constant-time for the common single-tool case, and repaired
// sessions stay valid on disk instead of being filtered on every model request.
func repairToolCallTailBeforeAppend(existing, incoming []Message) []Message {
	if len(incoming) == 0 {
		return nil
	}
	pending := pendingToolCallsAtTail(existing)
	result := make([]Message, 0, len(incoming)+len(pending))

	for _, message := range incoming {
		if message.Role == MessageRoleToolResult {
			result = append(result, matchedToolResultMessages(message, pending)...)
			continue
		}

		result = append(result, interruptedResultsForPendingCalls(pending)...)
		for i := range pending {
			pending[i].matched = true
		}
		result = append(result, message)
	}
	return result
}

func pendingToolCallsAtTail(messages []Message) []pendingToolCall {
	if len(messages) == 0 {
		return nil
	}

	callMessageIndex := len(messages) - 1
	for callMessageIndex >= 0 && messages[callMessageIndex].Role == MessageRoleToolResult {
		callMessageIndex--
	}
	if callMessageIndex < 0 || messages[callMessageIndex].Role != MessageRoleToolCall {
		return nil
	}

	callMessage := messages[callMessageIndex]
	pending := make([]pendingToolCall, 0, len(callMessage.ToolCalls))
	for toolIndex, call := range callMessage.ToolCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		originalID := strings.TrimSpace(call.ID)
		pending = append(pending, pendingToolCall{
			id:                toolCallIDOrFallback(originalID, callMessageIndex, toolIndex),
			name:              name,
			originalIDMissing: originalID == "",
		})
	}
	if len(pending) == 0 {
		return nil
	}

	for messageIndex := callMessageIndex + 1; messageIndex < len(messages); messageIndex++ {
		for _, toolResult := range messages[messageIndex].ToolResults {
			markExistingToolResult(pending, toolResult)
		}
	}
	return pending
}

func markExistingToolResult(pending []pendingToolCall, result ToolResult) {
	resultID := strings.TrimSpace(result.ToolCallID)
	if resultID == "" {
		return
	}
	for i := range pending {
		if !pending[i].matched && pending[i].id == resultID {
			pending[i].matched = true
			return
		}
	}
}

func matchedToolResultMessages(message Message, pending []pendingToolCall) []Message {
	if len(pending) == 0 || len(message.ToolResults) == 0 {
		return nil
	}
	matched := make([]Message, 0, len(message.ToolResults))
	for _, toolResult := range message.ToolResults {
		pendingIndex := matchingPendingToolCall(pending, toolResult)
		if pendingIndex < 0 {
			continue
		}
		pending[pendingIndex].matched = true
		toolResult.ToolCallID = pending[pendingIndex].id
		if strings.TrimSpace(toolResult.Name) == "" {
			toolResult.Name = pending[pendingIndex].name
		}
		matched = append(matched, Message{
			Role:        MessageRoleToolResult,
			ToolResults: []ToolResult{toolResult},
		})
	}
	return matched
}

func matchingPendingToolCall(pending []pendingToolCall, result ToolResult) int {
	resultID := strings.TrimSpace(result.ToolCallID)
	if resultID != "" {
		for i := range pending {
			if !pending[i].matched && pending[i].id == resultID {
				return i
			}
		}
	}

	resultName := strings.TrimSpace(result.Name)
	for i := range pending {
		if pending[i].matched || (!pending[i].originalIDMissing && resultID != "") {
			continue
		}
		if resultName == "" || resultName == pending[i].name {
			return i
		}
	}
	return -1
}

func interruptedResultsForPendingCalls(pending []pendingToolCall) []Message {
	result := make([]Message, 0, len(pending))
	for _, call := range pending {
		if call.matched {
			continue
		}
		result = append(result, Message{
			Role: MessageRoleToolResult,
			ToolResults: []ToolResult{{
				ToolCallID: call.id,
				Name:       call.name,
				Content:    interruptedToolResultContent,
			}},
		})
	}
	return result
}
