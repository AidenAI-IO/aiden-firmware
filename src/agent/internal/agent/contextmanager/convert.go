package contextmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

func ConvertMessageList(messages []Message) []llms.MessageContent {
	standardMessageList := make([]llms.MessageContent, len(messages))
	for i, message := range messages {
		newMessage := llms.MessageContent{
			Role:  message.Role.ToStandardRole(),
			Parts: []llms.ContentPart{},
		}
		if message.Role == MessageRoleToolResult {
			for resultIndex, result := range message.ToolResults {
				toolCallID := strings.TrimSpace(result.ToolCallID)
				if toolCallID == "" {
					toolCallID = toolCallIDOrFallback("", i, resultIndex)
				}
				newMessage.Parts = append(newMessage.Parts, llms.ToolCallResponse{
					ToolCallID: toolCallID,
					Name:       strings.TrimSpace(result.Name),
					Content:    result.Content,
				})
			}
			standardMessageList[i] = newMessage
			continue
		}
		if content := strings.TrimSpace(message.Content); content != "" {
			newMessage.Parts = append(newMessage.Parts, llms.TextPart(content))
		}
		for toolIndex, call := range message.ToolCalls {
			name := strings.TrimSpace(call.Name)
			if name == "" {
				continue
			}
			newMessage.Parts = append(newMessage.Parts, llms.ToolCall{
				ID:   toolCallIDOrFallback(call.ID, i, toolIndex),
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      name,
					Arguments: normalizeToolCallArguments(call.Arguments),
				},
			})
		}
		for _, attachment := range message.Attachments {
			filePath := strings.TrimSpace(attachment.FilePath)
			if filePath == "" {
				continue
			}
			data, err := os.ReadFile(filePath)
			if err != nil {
				newMessage.Parts = append(newMessage.Parts, llms.TextPart(attachmentOmittedMessage(attachment.MIMEType, err)))
				continue
			}
			if len(data) == 0 {
				continue
			}
			newMessage.Parts = append(newMessage.Parts, llms.BinaryPart(attachment.MIMEType, data))
		}
		standardMessageList[i] = newMessage
	}
	return standardMessageList
}

// ConvertChoiceToContextManagerMessage converts a content choice to a context manager message
func ConvertChoiceToContextManagerMessage(choice llms.ContentChoice) Message {
	role := MessageRoleAssistant
	if contentChoiceHasToolCalls(choice) {
		role = MessageRoleToolCall
	}
	return Message{
		Role:      role,
		Content:   contentChoiceText(choice),
		ToolCalls: toolCallsFromContentChoice(choice),
	}
}

func contentChoiceHasToolCalls(choice llms.ContentChoice) bool {
	if len(choice.ToolCalls) > 0 {
		return true
	}
	return choice.FuncCall != nil
}

func contentChoiceText(choice llms.ContentChoice) string {
	parts := make([]string, 0, 2)
	if reasoning := strings.TrimSpace(choice.ReasoningContent); reasoning != "" {
		parts = append(parts, reasoning)
	}
	if content := strings.TrimSpace(choice.Content); content != "" {
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n")
}

func toolCallsFromContentChoice(choice llms.ContentChoice) []ToolCall {
	toolCalls := choice.ToolCalls
	if len(toolCalls) == 0 && choice.FuncCall != nil {
		toolCalls = []llms.ToolCall{{
			Type:         "function",
			FunctionCall: choice.FuncCall,
		}}
	}
	result := make([]ToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		if call.FunctionCall == nil {
			continue
		}
		name := strings.TrimSpace(call.FunctionCall.Name)
		if name == "" {
			continue
		}
		result = append(result, ToolCall{
			ID:        strings.TrimSpace(call.ID),
			Name:      name,
			Arguments: normalizeToolCallArguments(call.FunctionCall.Arguments),
		})
	}
	return result
}

func normalizeToolCallArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	encoded, err := json.Marshal(map[string]string{"input": arguments})
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func toolCallIDOrFallback(id string, messageIndex, toolIndex int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("ctx_tool_call_%d_%d", messageIndex, toolIndex)
}
