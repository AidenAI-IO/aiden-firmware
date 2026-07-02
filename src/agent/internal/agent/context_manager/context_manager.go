package context_manager

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"
)

type MessageRole string

const (
	MessageRoleUser MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleToolCall MessageRole = "tool_call"
	MessageRoleToolResult MessageRole = "tool_result"
	MessageRoleState MessageRole = "state"
	MessageRoleSystem MessageRole = "system"
	MessageRoleNotice MessageRole = "notice"
)

func (r MessageRole) ToStandardRole() llms.ChatMessageType {
	switch r {
	case MessageRoleUser:
		return llms.ChatMessageTypeHuman
	case MessageRoleAssistant:
		return llms.ChatMessageTypeAI
	case MessageRoleToolCall:
		return llms.ChatMessageTypeAI
	case MessageRoleToolResult:
		return llms.ChatMessageTypeTool
	case MessageRoleState:
		return llms.ChatMessageTypeHuman
	case MessageRoleSystem:
		return llms.ChatMessageTypeSystem
	case MessageRoleNotice:
		return llms.ChatMessageTypeHuman
	default:
		return llms.ChatMessageTypeHuman
	}
}

type Message struct {
	Role        MessageRole  `json:"role"`
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
}

// Attachments are files that are attached to the message, they are not loaded into memory, they are only used to track the files that are attached to the message.
type Attachment struct {
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
	Data     []byte `json:"-"`
}

// ContextManager is a manager for the context of the agent, it is used to manage the context of the agent, it is used to append messages to the context and to fork the context.
// It is thread safe and can be used concurrently by multiple goroutines.
// SessionID is the id of the session, it is used to identify the session of the agent. Conversation in a same session are shared the same context.
type ContextManager struct {
	sessionID string
	messageList []Message
	mu sync.RWMutex
}

func NewContextManager() *ContextManager {
	sessionID := "session_" + uuid.New().String()
	return &ContextManager{
		sessionID: sessionID,
		messageList: []Message{},
		mu: sync.RWMutex{},
	}
}

func (c *ContextManager) GetSessionID() string {
	return c.sessionID
}

func (c *ContextManager) AppendMessage(message Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageList = append(c.messageList, message)
}

// Fork creates a new MessageList that is a copy of the current MessageList
func (c *ContextManager) Fork() *ContextManager {
	c.mu.RLock()
	defer c.mu.RUnlock()
	newMessageList := make([]Message, len(c.messageList))
	for i, msg := range c.messageList {
		newMessageList[i] = msg
		if len(msg.ToolCalls) > 0 {
			newMessageList[i].ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ToolResults) > 0 {
			newMessageList[i].ToolResults = append([]ToolResult(nil), msg.ToolResults...)
		}
		if len(msg.Attachments) > 0 {
			newMessageList[i].Attachments = append([]Attachment(nil), msg.Attachments...)
		}
	}
	newSessionID := "session_" + uuid.New().String()
	return &ContextManager{
		sessionID: newSessionID,
		messageList: newMessageList,
		mu: sync.RWMutex{},
	}
}

func (c *ContextManager) ConvertToStandardMessageList() []llms.MessageContent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	standardMessageList := make([]llms.MessageContent, len(c.messageList))
	for i, message := range c.messageList {
		newMessage := llms.MessageContent{
			Role: message.Role.ToStandardRole(),
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
					Arguments: call.Arguments,
				},
			})
		}
		for _, attachment := range message.Attachments {
			data := attachment.Data
			if len(data) == 0 && strings.TrimSpace(attachment.FilePath) != "" {
				var err error
				data, err = os.ReadFile(attachment.FilePath)
				if err != nil {
					fmt.Printf("Failed to read attachment file: %v", err)
					continue
				}
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
			Arguments: strings.TrimSpace(call.FunctionCall.Arguments),
		})
	}
	return result
}

func toolCallIDOrFallback(id string, messageIndex, toolIndex int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("ctx_tool_call_%d_%d", messageIndex, toolIndex)
}