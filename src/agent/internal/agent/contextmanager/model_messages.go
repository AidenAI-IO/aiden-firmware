package contextmanager

import (
	"strings"

	"github.com/tmc/langchaingo/llms"
)

type modelToolCall struct {
	partIndex      int
	call           llms.ToolCall
	responseIndex  int
	resolvedCallID string
}

type modelToolResponse struct {
	response  llms.ToolCallResponse
	callIndex int
}

// ModelMessages returns provider-bound messages with a valid tool-call sequence.
// Context sessions are append-only, so cancellation, process termination, or an
// interrupted disk write can leave an assistant tool call without its required
// tool response. Providers reject the entire request in that state. This method
// keeps immediately paired calls/results, removes orphan tool protocol parts,
// and preserves any ordinary assistant text that shared an interrupted call.
func (c *ContextManager) ModelMessages() []llms.MessageContent {
	return repairToolMessageSequence(c.ConvertToStandardMessageList())
}

func repairToolMessageSequence(messages []llms.MessageContent) []llms.MessageContent {
	if len(messages) == 0 {
		return nil
	}

	result := make([]llms.MessageContent, 0, len(messages))
	for i := 0; i < len(messages); {
		message := messages[i]
		calls := toolCallsInModelMessage(message)
		if len(calls) == 0 {
			if !isModelToolResultRole(message.Role) && len(message.Parts) > 0 {
				result = append(result, message)
			}
			i++
			continue
		}

		j := i + 1
		responses := make([]modelToolResponse, 0)
		for j < len(messages) && isModelToolResultRole(messages[j].Role) {
			for _, part := range messages[j].Parts {
				if response, ok := part.(llms.ToolCallResponse); ok {
					responses = append(responses, modelToolResponse{response: response, callIndex: -1})
				}
			}
			j++
		}

		matchModelToolCalls(calls, responses)
		if repaired, ok := repairedAssistantToolMessage(message, calls); ok {
			result = append(result, repaired)
		}
		for responseIndex := range responses {
			response := responses[responseIndex]
			if response.callIndex < 0 {
				continue
			}
			response.response.ToolCallID = calls[response.callIndex].resolvedCallID
			result = append(result, llms.MessageContent{
				Role:  llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{response.response},
			})
		}
		i = j
	}
	return result
}

func toolCallsInModelMessage(message llms.MessageContent) []modelToolCall {
	if message.Role != llms.ChatMessageTypeAI {
		return nil
	}
	calls := make([]modelToolCall, 0)
	for partIndex, part := range message.Parts {
		call, ok := part.(llms.ToolCall)
		if !ok || call.FunctionCall == nil || strings.TrimSpace(call.FunctionCall.Name) == "" {
			continue
		}
		calls = append(calls, modelToolCall{
			partIndex:     partIndex,
			call:          call,
			responseIndex: -1,
		})
	}
	return calls
}

func matchModelToolCalls(calls []modelToolCall, responses []modelToolResponse) {
	for callIndex := range calls {
		callID := strings.TrimSpace(calls[callIndex].call.ID)
		if callID == "" {
			continue
		}
		for responseIndex := range responses {
			if responses[responseIndex].callIndex >= 0 || strings.TrimSpace(responses[responseIndex].response.ToolCallID) != callID {
				continue
			}
			bindModelToolPair(calls, responses, callIndex, responseIndex, callID)
			break
		}
	}

	for callIndex := range calls {
		if calls[callIndex].responseIndex >= 0 {
			continue
		}
		for responseIndex := range responses {
			if responses[responseIndex].callIndex >= 0 || !canRecoverModelToolPair(calls[callIndex].call, responses[responseIndex].response) {
				continue
			}
			resolvedID := recoveredModelToolCallID(calls[callIndex].call.ID, responses[responseIndex].response.ToolCallID)
			bindModelToolPair(calls, responses, callIndex, responseIndex, resolvedID)
			break
		}
	}
}

func bindModelToolPair(calls []modelToolCall, responses []modelToolResponse, callIndex, responseIndex int, resolvedID string) {
	calls[callIndex].responseIndex = responseIndex
	calls[callIndex].resolvedCallID = strings.TrimSpace(resolvedID)
	responses[responseIndex].callIndex = callIndex
}

func canRecoverModelToolPair(call llms.ToolCall, response llms.ToolCallResponse) bool {
	callID := strings.TrimSpace(call.ID)
	responseID := strings.TrimSpace(response.ToolCallID)
	if !isGeneratedContextToolCallID(callID) && !isGeneratedContextToolCallID(responseID) {
		return false
	}
	callName := ""
	if call.FunctionCall != nil {
		callName = strings.TrimSpace(call.FunctionCall.Name)
	}
	responseName := strings.TrimSpace(response.Name)
	return callName == responseName || callName == "" || responseName == ""
}

func recoveredModelToolCallID(callID, responseID string) string {
	callID = strings.TrimSpace(callID)
	responseID = strings.TrimSpace(responseID)
	switch {
	case responseID != "" && !isGeneratedContextToolCallID(responseID):
		return responseID
	case callID != "":
		return callID
	default:
		return responseID
	}
}

func isGeneratedContextToolCallID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), "ctx_tool_call_")
}

func repairedAssistantToolMessage(message llms.MessageContent, calls []modelToolCall) (llms.MessageContent, bool) {
	matchedByPart := make(map[int]modelToolCall, len(calls))
	for _, call := range calls {
		if call.responseIndex >= 0 {
			matchedByPart[call.partIndex] = call
		}
	}

	repaired := message
	repaired.Parts = make([]llms.ContentPart, 0, len(message.Parts))
	for partIndex, part := range message.Parts {
		call, isToolCall := part.(llms.ToolCall)
		if !isToolCall {
			repaired.Parts = append(repaired.Parts, part)
			continue
		}
		matched, ok := matchedByPart[partIndex]
		if !ok {
			continue
		}
		call.ID = matched.resolvedCallID
		repaired.Parts = append(repaired.Parts, call)
	}
	return repaired, len(repaired.Parts) > 0
}

func isModelToolResultRole(role llms.ChatMessageType) bool {
	return role == llms.ChatMessageTypeTool || role == llms.ChatMessageTypeFunction
}
