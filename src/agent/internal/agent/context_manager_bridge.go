package agent

import (
	"fmt"
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

func messageFromLLMContent(manager *context_manager.ContextManager, content llms.MessageContent) context_manager.Message {
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
			if manager == nil {
				continue
			}
			stored, err := manager.StoreAttachment(typed.MIMEType, typed.Data)
			if err != nil {
				message.Content = mergePromptText(message.Content, attachmentStorageFailureText(typed.MIMEType, err))
				continue
			}
			message.Attachments = append(message.Attachments, stored)
		case llms.ImageURLContent:
			if text := strings.TrimSpace(typed.URL); text != "" {
				message.Content = mergePromptText(message.Content, text)
			}
		}
	}
	return message
}

func userMessageFromInput(manager *context_manager.ContextManager, input string, attachments []InputAttachment) context_manager.Message {
	descriptions := make([]string, 0, len(attachments))
	message := context_manager.Message{
		Role:    context_manager.MessageRoleUser,
		Content: attachmentAwarePrompt(normalizeRunInput(input, attachments), attachments, descriptions),
	}
	if manager == nil {
		return message
	}
	for _, attachment := range attachments {
		if len(attachment.Data) == 0 {
			continue
		}
		mimeType := attachmentMIMETypeOrDefault(attachment)
		stored, err := manager.StoreAttachment(mimeType, attachment.Data)
		if err != nil {
			message.Content = mergePromptText(message.Content, attachmentStorageFailureText(mimeType, err))
			continue
		}
		message.Attachments = append(message.Attachments, stored)
	}
	return message
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

func visualFollowupMessageFromLLMContent(manager *context_manager.ContextManager, content llms.MessageContent) context_manager.Message {
	message := context_manager.Message{Role: messageRoleFromLLM(content.Role)}
	for _, part := range content.Parts {
		switch typed := part.(type) {
		case llms.TextContent:
			message.Content = mergePromptText(message.Content, typed.Text)
		case llms.ImageURLContent:
			mimeType, data, ok := telemetryDataURL(typed.URL)
			if ok && strings.HasPrefix(strings.ToLower(mimeType), "image/") && len(data) > 0 && manager != nil {
				stored, err := manager.StoreAttachment(mimeType, data)
				if err == nil {
					message.Attachments = append(message.Attachments, stored)
					continue
				}
				message.Content = mergePromptText(message.Content, attachmentStorageFailureText(mimeType, err))
				continue
			}
			if text := strings.TrimSpace(typed.URL); text != "" {
				message.Content = mergePromptText(message.Content, text)
			}
		case llms.BinaryContent:
			if manager == nil {
				continue
			}
			stored, err := manager.StoreAttachment(typed.MIMEType, typed.Data)
			if err != nil {
				message.Content = mergePromptText(message.Content, attachmentStorageFailureText(typed.MIMEType, err))
				continue
			}
			message.Attachments = append(message.Attachments, stored)
		default:
			fallback := messageFromLLMContent(manager, llms.MessageContent{
				Role:  content.Role,
				Parts: []llms.ContentPart{part},
			})
			message.Content = mergePromptText(message.Content, fallback.Content)
			message.ToolCalls = append(message.ToolCalls, fallback.ToolCalls...)
			message.ToolResults = append(message.ToolResults, fallback.ToolResults...)
			message.Attachments = append(message.Attachments, fallback.Attachments...)
		}
	}
	return message
}

func attachmentMIMETypeOrDefault(attachment InputAttachment) string {
	mimeType := strings.TrimSpace(attachment.MIMEType)
	if mimeType != "" {
		return mimeType
	}
	switch attachment.Kind {
	case AttachmentKindImage:
		return "image/png"
	case AttachmentKindAudio:
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}

func attachmentStorageFailureText(mimeType string, err error) string {
	label := strings.TrimSpace(mimeType)
	if label == "" {
		label = "attachment"
	}
	if err == nil {
		return fmt.Sprintf("[Attachment omitted: %s could not be stored.]", label)
	}
	return fmt.Sprintf("[Attachment omitted: %s could not be stored: %v]", label, err)
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

func freshNewContextManager(
	systemPrompt string,
	userInput string,
	attachments []InputAttachment,
	sessionFolder string,
) (*context_manager.ContextManager, error) {
	manager, isFresh, err := context_manager.NewContextManagerFromSessionID(sessionFolder, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create context manager: %w", err)
	}

	if isFresh && strings.TrimSpace(systemPrompt) != "" {
		if err := manager.AppendMessage(context_manager.Message{
			Role:    context_manager.MessageRoleSystem,
			Content: strings.TrimSpace(systemPrompt),
		}); err != nil {
			return nil, fmt.Errorf("failed to append system prompt: %w", err)
		}
	}

	if err := manager.AppendMessage(userMessageFromInput(manager, userInput, attachments)); err != nil {
		return nil, fmt.Errorf("failed to append user message: %w", err)
	}

	return manager, nil
}
