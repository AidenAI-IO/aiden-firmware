package contextmanager

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"
)

type MessageRole string

const (
	MessageRoleUser       MessageRole = "user"
	MessageRoleAssistant  MessageRole = "assistant"
	MessageRoleToolCall   MessageRole = "tool_call"
	MessageRoleToolResult MessageRole = "tool_result"
	MessageRoleState      MessageRole = "state"
	MessageRoleSystem     MessageRole = "system"
	MessageRoleNotice     MessageRole = "notice"
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

type AppendMessageHook func(Message) AppendMessageHookResult

type AppendMessageHookResult struct {
	Before  []Message
	Message *Message
	After   []Message
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

// Attachment tracks file metadata for message attachments. Binary content is stored on disk
// and only loaded when ConvertToStandardMessageList is called.
type Attachment struct {
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
	Source   string `json:"source,omitempty"`
}

const AttachmentSourceScreenshotObservation = "screenshot_observation"

// ContextManager is a manager for the context of the agent, it is used to manage the context of the agent, it is used to append messages to the context and to fork the context.
// It is thread safe and can be used concurrently by multiple goroutines.
// SessionID is the id of the session, it is used to identify the session of the agent. Conversation in a same session are shared the same context.
type ContextManager struct {
	sessionID       string
	messageList     []Message
	appendHooks     []AppendMessageHook
	attachmentStore *attachmentStore
	mu              sync.RWMutex
	sessionFolder   string
}

// LoadContextManagerFromSessionID loads a context manager from the session folder
func LoadContextManagerFromSessionID(sessionFolder string, sessionID string) (*ContextManager, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is empty")
	}

	messageList, err := loadSession(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}

	attachmentStore, err := newAttachmentStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}

	return &ContextManager{
		sessionID:       sessionID,
		messageList:     messageList,
		mu:              sync.RWMutex{},
		sessionFolder:   sessionFolder,
		attachmentStore: attachmentStore,
	}, nil
}

func LoadContextManagerFromCurrentSession(sessionFolder string) (*ContextManager, error) {
	sessionID := fetchCurrentSession(sessionFolder)
	if sessionID == "" {
		return nil, fmt.Errorf("current session ID is empty")
	}
	return LoadContextManagerFromSessionID(sessionFolder, sessionID)
}

// NewContextManager creates a new context manager and saves the session ID to the session folder as current session.
func NewContextManager(sessionFolder string, systemPrompt string) (*ContextManager, error) {
	newSessionID := newSessionID()
	if err := saveCurrentSession(sessionFolder, newSessionID); err != nil {
		return nil, err
	}

	manager, err := LoadContextManagerFromSessionID(sessionFolder, newSessionID)
	if err != nil {
		return nil, err
	}

	// sessionID is new, so system prompt is necessary
	if err := manager.AppendMessage(Message{
		Role:    MessageRoleSystem,
		Content: systemPrompt,
	}); err != nil {
		return nil, err
	}

	return manager, nil
}

func NewContextManagerFromMessageList(sessionFolder string, messageList []Message) (*ContextManager, error) {
	newSessionID := newSessionID()
	attachmentStore, err := newAttachmentStore(sessionFolder, newSessionID)
	if err != nil {
		return nil, err
	}
	manager := &ContextManager{
		sessionFolder:   sessionFolder,
		sessionID:       newSessionID,
		messageList:     messageList,
		mu:              sync.RWMutex{},
		attachmentStore: attachmentStore,
	}
	if err := manager.flushFull(); err != nil {
		return nil, err
	}
	return manager, nil
}

func newSessionID() string {
	return "s_" + uuid.New().String()
}

func SwitchSession(sessionFolder string, sessionID string) error {
	// save session ID to current session file
	if err := saveCurrentSession(sessionFolder, sessionID); err != nil {
		return err
	}
	return nil
}

func (c *ContextManager) CloneMessageList() []Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneMessages(c.messageList)
}

func (c *ContextManager) GetSessionFolder() string {
	return c.sessionFolder
}

func (c *ContextManager) GetSessionID() string {
	return c.sessionID
}

func (c *ContextManager) appendToList(messages []Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages = repairToolCallTailBeforeAppend(c.messageList, messages)
	if len(messages) == 0 {
		return nil
	}

	if err := appendSession(c.sessionFolder, c.sessionID, messages); err != nil {
		log.Println("[CM] Failed to append messages to session", err)
		return err
	}

	c.messageList = append(c.messageList, messages...)

	return nil
}

func (c *ContextManager) flushFull() error {
	c.mu.RLock()
	messages := cloneMessages(c.messageList)
	c.mu.RUnlock()
	if len(messages) == 0 {
		return nil
	}
	return appendSession(c.sessionFolder, c.sessionID, messages)
}

func (c *ContextManager) AppendMessage(message Message) error {
	c.mu.RLock()
	hooks := append([]AppendMessageHook(nil), c.appendHooks...)
	c.mu.RUnlock()

	messages := []Message{cloneMessage(message)}
	for _, entry := range hooks {
		var next []Message
		for _, current := range messages {
			result := entry(cloneMessage(current))
			next = append(next, cloneMessages(result.Before)...)
			if result.Message != nil {
				next = append(next, cloneMessage(*result.Message))
			}
			next = append(next, cloneMessages(result.After)...)
		}
		messages = next
	}

	if len(messages) == 0 {
		return nil
	}

	return c.appendToList(messages)
}

func (c *ContextManager) AddAppendMessageHook(hook AppendMessageHook) {
	if hook == nil {
		return
	}
	c.mu.Lock()
	c.appendHooks = append(c.appendHooks, hook)
	c.mu.Unlock()
}

func (c *ContextManager) AddAppendMessageHooks(hooks []AppendMessageHook) {
	if len(hooks) == 0 {
		return
	}
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		c.mu.Lock()
		c.appendHooks = append(c.appendHooks, hook)
		c.mu.Unlock()
	}
}

func (c *ContextManager) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.messageList) == 0
}

type MessageListDump struct {
	SessionID string    `json:"session_id"`
	Messages  []Message `json:"messages"`
}

func (c *ContextManager) MessageListDump() MessageListDump {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return MessageListDump{
		SessionID: c.sessionID,
		Messages:  cloneMessages(c.messageList),
	}
}

// StoreAttachment persists attachment bytes on disk and returns metadata only.
func (c *ContextManager) StoreAttachment(mimeType string, data []byte) (Attachment, error) {
	if len(data) == 0 {
		return Attachment{}, fmt.Errorf("attachment data is empty")
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return c.attachmentStore.store(mimeType, data)
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]Message, len(messages))
	for i, msg := range messages {
		cloned[i] = cloneMessage(msg)
	}
	return cloned
}

func cloneMessage(msg Message) Message {
	cloned := msg
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
	}
	if len(msg.ToolResults) > 0 {
		cloned.ToolResults = append([]ToolResult(nil), msg.ToolResults...)
	}
	if len(msg.Attachments) > 0 {
		cloned.Attachments = append([]Attachment(nil), msg.Attachments...)
	}
	return cloned
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
