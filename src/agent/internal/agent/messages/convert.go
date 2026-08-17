package messages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

const ScreenshotCoordinateGuidance = "This screenshot uses the fixed normalized coordinate plane for pointer and touch tools: (0,0) is the top-left corner, (1000,1000) is the bottom-right corner, and (500,500) is the center. Use visual proportions of the full image: a target one quarter from the left and three quarters from the top is (250,750), a target at the center is (500,500), a small control near the top-right is around (950,100), and a small control near the bottom-right is around (950,875). For small controls, tap the visible center of the control itself, not the larger nearby panel; choose a point inside its visible bounds rather than on an edge or border. For close or dismiss requests, locate the visible X/close control itself even when it is tiny; do not tap the center of the surrounding banner. In a bottom action sheet with stacked rows, tap the center of the requested row's label, not the center of the whole sheet; centered options should stay near x=500 and inside the row between its visible separators. In a multi-column grid, the first image is the top-left cell; its selection control is at that cell's top-right corner, not at the screen's top-right corner and not at the image center. In media grids, the selection control is the larger hollow circle inset inside the thumbnail; small stars, favorite markers, and other badges on the cell border are not selection controls. Tap the circle's center. Normalize x and y independently: never mix a normalized axis with a raw screenshot-pixel axis. If the task is explicitly a single-frame static perception task, use the first image, perform one target gesture, and stop; do not probe or retry because the static fixture may remain unchanged. Send x/y on this normalized plane, never raw screenshot pixels or displayed-image pixels."

func ConvertMessageList(messageList []Message) []llms.MessageContent {
	standardMessageList := make([]llms.MessageContent, len(messageList))
	for i, message := range messageList {
		newMessage := llms.MessageContent{
			Role:  message.Role.ToStandardRole(),
			Parts: []llms.ContentPart{},
		}
		if message.Role == MessageRoleToolResult {
			for resultIndex, result := range message.ToolResults {
				toolCallID := strings.TrimSpace(result.ToolCallID)
				if toolCallID == "" {
					toolCallID = ToolCallIDOrFallback("", i, resultIndex)
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
				ID:   ToolCallIDOrFallback(call.ID, i, toolIndex),
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
			if attachment.Source == AttachmentSourceScreenshotObservation {
				attachmentID := filepath.Base(filePath)
				if attachmentID != "." && attachmentID != "" {
					newMessage.Parts = append(newMessage.Parts, llms.TextPart(fmt.Sprintf("[screenshot_attachment_id=%s]", attachmentID)))
				}
				newMessage.Parts = append(newMessage.Parts, llms.TextPart(ScreenshotCoordinateGuidance))
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
	return strings.TrimSpace(choice.Content)
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

func attachmentOmittedMessage(mimeType string, err error) string {
	label := strings.TrimSpace(mimeType)
	if label == "" {
		label = "attachment"
	}
	if err == nil {
		return fmt.Sprintf("[Attachment omitted: %s could not be loaded.]", label)
	}
	return fmt.Sprintf("[Attachment omitted: %s could not be loaded: %v]", label, err)
}

func ToolCallIDOrFallback(id string, messageIndex, toolIndex int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("ctx_tool_call_%d_%d", messageIndex, toolIndex)
}
