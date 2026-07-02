package agent

import (
	"strings"

	"aiden-agent/internal/agent/context_manager"

	"github.com/tmc/langchaingo/llms"
)

func messageRoleFromLLM(role llms.ChatMessageType) context_manager.MessageRole {
	switch role {
	case llms.ChatMessageTypeSystem:
		return context_manager.MessageRoleSystem
	case llms.ChatMessageTypeHuman, llms.ChatMessageTypeGeneric:
		return context_manager.MessageRoleUser
	case llms.ChatMessageTypeAI:
		return context_manager.MessageRoleAssistant
	case llms.ChatMessageTypeTool, llms.ChatMessageTypeFunction:
		return context_manager.MessageRoleToolResult
	default:
		return context_manager.MessageRoleUser
	}
}

func messageFromLLMContent(content llms.MessageContent) context_manager.Message {
	message := context_manager.Message{Role: messageRoleFromLLM(content.Role)}
	for _, part := range content.Parts {
		switch typed := part.(type) {
		case llms.TextContent:
			message.Content = mergePromptText(message.Content, typed.Text)
		case llms.ToolCall:
			if typed.FunctionCall == nil {
				continue
			}
			message.Role = context_manager.MessageRoleToolCall
			message.ToolCalls = append(message.ToolCalls, context_manager.ToolCall{
				ID:        strings.TrimSpace(typed.ID),
				Name:      strings.TrimSpace(typed.FunctionCall.Name),
				Arguments: strings.TrimSpace(typed.FunctionCall.Arguments),
			})
		case llms.ToolCallResponse:
			message.Role = context_manager.MessageRoleToolResult
			message.ToolResults = append(message.ToolResults, context_manager.ToolResult{
				ToolCallID: strings.TrimSpace(typed.ToolCallID),
				Name:       strings.TrimSpace(typed.Name),
				Content:    typed.Content,
			})
		case llms.BinaryContent:
			message.Attachments = append(message.Attachments, context_manager.Attachment{
				MIMEType: typed.MIMEType,
				FileSize: int64(len(typed.Data)),
				Data:     append([]byte(nil), typed.Data...),
			})
		case llms.ImageURLContent:
			if text := strings.TrimSpace(typed.URL); text != "" {
				message.Content = mergePromptText(message.Content, text)
			}
		}
	}
	return message
}

func userMessageFromInput(input string, attachments []InputAttachment) context_manager.Message {
	parts := buildUserMessageParts(input, attachments)
	return messageFromLLMContent(llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: parts,
	})
}

func noticeMessage(content string) context_manager.Message {
	return context_manager.Message{
		Role:    context_manager.MessageRoleNotice,
		Content: strings.TrimSpace(content),
	}
}

func toolResultMessage(toolCallID, toolName, content string) context_manager.Message {
	return context_manager.Message{
		Role: context_manager.MessageRoleToolResult,
		ToolResults: []context_manager.ToolResult{{
			ToolCallID: strings.TrimSpace(toolCallID),
			Name:       strings.TrimSpace(toolName),
			Content:    content,
		}},
	}
}

func mergePromptText(existing, addition string) string {
	existing = strings.TrimSpace(existing)
	addition = strings.TrimSpace(addition)
	switch {
	case existing == "":
		return addition
	case addition == "":
		return existing
	default:
		return existing + "\n" + addition
	}
}

func seedContextManager(
	manager *context_manager.ContextManager,
	systemPrompt string,
	history []llms.MessageContent,
	userInput string,
	attachments []InputAttachment,
	notice string,
) {
	if strings.TrimSpace(systemPrompt) != "" {
		manager.AppendMessage(context_manager.Message{
			Role:    context_manager.MessageRoleSystem,
			Content: strings.TrimSpace(systemPrompt),
		})
	}
	for _, item := range history {
		manager.AppendMessage(messageFromLLMContent(item))
	}
	if strings.TrimSpace(notice) != "" {
		manager.AppendMessage(noticeMessage(notice))
	}
	manager.AppendMessage(userMessageFromInput(userInput, attachments))
}
