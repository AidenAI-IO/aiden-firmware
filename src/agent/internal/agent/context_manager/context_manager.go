package context_manager

import (
	"fmt"
	"os"
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
	Role    MessageRole `json:"role"`
	Content string `json:"content"`
	Attachments []Attachment `json:"attachments"`
}

// Attachments are files that are attached to the message, they are not loaded into memory, they are only used to track the files that are attached to the message.
type Attachment struct {
	MIMEType string `json:"mime_type"`
	FileSize int64 `json:"file_size"`
	FilePath string `json:"file_path"`
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
	defer c.mu.Unlock()
	newMessageList := make([]Message, len(c.messageList))
	for i, msg := range c.messageList {
		newMessageList[i] = msg
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
	defer c.mu.Unlock()
	standardMessageList := make([]llms.MessageContent, len(c.messageList))
	for i, message := range c.messageList {
		newMessage := llms.MessageContent{
			Role: message.Role.ToStandardRole(),
			Parts: []llms.ContentPart{},
		}
		newMessage.Parts = append(newMessage.Parts, llms.TextPart(message.Content))
		for _, attachment := range message.Attachments {
			data, err := os.ReadFile(attachment.FilePath)
			if err != nil {
				fmt.Printf("Failed to read attachment file: %v", err)
				continue
			}
			newMessage.Parts = append(newMessage.Parts, llms.BinaryPart(attachment.MIMEType, data))
		}
		standardMessageList[i] = newMessage
	}
	return standardMessageList
}