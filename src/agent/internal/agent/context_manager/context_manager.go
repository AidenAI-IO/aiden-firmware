package context_manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Role           MessageRole     `json:"role"`
	Content        string          `json:"content"`
	PromptSections []PromptSection `json:"prompt_sections,omitempty"`
	ToolCalls      []ToolCall      `json:"tool_calls,omitempty"`
	ToolResults    []ToolResult    `json:"tool_results,omitempty"`
	Attachments    []Attachment    `json:"attachments,omitempty"`
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
}

// ContextManager is a manager for the context of the agent, it is used to manage the context of the agent, it is used to append messages to the context and to fork the context.
// It is thread safe and can be used concurrently by multiple goroutines.
// SessionID is the id of the session, it is used to identify the session of the agent. Conversation in a same session are shared the same context.
type ContextManager struct {
	sessionID       string
	messageList     []Message
	attachmentStore *attachmentStore
	mu              sync.RWMutex
}

type attachmentStore struct {
	mu   sync.Mutex
	root string
	next int
	refs int
}

func NewContextManager() *ContextManager {
	sessionID := "session_" + uuid.New().String()
	return &ContextManager{
		sessionID:   sessionID,
		messageList: []Message{},
		mu:          sync.RWMutex{},
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
	messages := make([]Message, len(c.messageList))
	for i, msg := range c.messageList {
		messages[i] = msg
		if len(msg.ToolCalls) > 0 {
			messages[i].ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ToolResults) > 0 {
			messages[i].ToolResults = append([]ToolResult(nil), msg.ToolResults...)
		}
		if len(msg.Attachments) > 0 {
			messages[i].Attachments = append([]Attachment(nil), msg.Attachments...)
		}
		if len(msg.PromptSections) > 0 {
			messages[i].PromptSections = append([]PromptSection(nil), msg.PromptSections...)
		}
	}
	return MessageListDump{
		SessionID: c.sessionID,
		Messages:  messages,
	}
}

func (c *ContextManager) Reset() {
	c.mu.Lock()
	store := c.detachAttachmentStoreLocked()
	c.messageList = nil
	c.sessionID = "session_" + uuid.New().String()
	c.mu.Unlock()

	releaseAttachmentStore(store)
}

// Close releases resources owned by this context manager. Forked managers
// should be closed when discarded so shared attachment stores can be removed
// once all owners have released them.
func (c *ContextManager) Close() error {
	c.mu.Lock()
	store := c.detachAttachmentStoreLocked()
	c.mu.Unlock()

	releaseAttachmentStore(store)
	return nil
}

func (c *ContextManager) detachAttachmentStoreLocked() *attachmentStore {
	store := c.attachmentStore
	c.attachmentStore = nil
	return store
}

func releaseAttachmentStore(store *attachmentStore) {
	if store != nil {
		store.release()
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

	c.mu.Lock()
	store, err := c.ensureAttachmentStoreLocked()
	if err == nil {
		store.retain()
	}
	c.mu.Unlock()
	if err != nil {
		return Attachment{}, err
	}
	defer store.release()

	return store.store(mimeType, data)
}

func (c *ContextManager) ensureAttachmentStoreLocked() (*attachmentStore, error) {
	if c.attachmentStore != nil {
		return c.attachmentStore, nil
	}
	store, err := newAttachmentStore()
	if err != nil {
		return nil, err
	}
	c.attachmentStore = store
	return store, nil
}

func newAttachmentStore() (*attachmentStore, error) {
	root, err := os.MkdirTemp("", "aiden-agent-ctx-attachments-")
	if err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}
	return &attachmentStore{
		root: root,
		refs: 1,
	}, nil
}

func (s *attachmentStore) retain() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs++
}

func (s *attachmentStore) release() {
	if s == nil {
		return
	}
	var root string
	s.mu.Lock()
	if s.refs > 0 {
		s.refs--
	}
	if s.refs == 0 && s.root != "" {
		root = s.root
		s.root = ""
		s.next = 0
	}
	s.mu.Unlock()

	if root != "" {
		_ = os.RemoveAll(root)
	}
}

func (s *attachmentStore) store(mimeType string, data []byte) (Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" {
		return Attachment{}, fmt.Errorf("attachment store is closed")
	}
	for {
		s.next++
		name := fmt.Sprintf("attachment_%06d%s", s.next, attachmentExtension(mimeType))
		path := filepath.Join(s.root, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return Attachment{}, fmt.Errorf("create attachment file: %w", err)
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return Attachment{}, fmt.Errorf("write attachment file: %w", writeErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return Attachment{}, fmt.Errorf("close attachment file: %w", closeErr)
		}
		return Attachment{
			MIMEType: mimeType,
			FileSize: int64(len(data)),
			FilePath: path,
		}, nil
	}
}

func attachmentExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "audio/wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	default:
		return ".bin"
	}
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
		if len(msg.PromptSections) > 0 {
			newMessageList[i].PromptSections = append([]PromptSection(nil), msg.PromptSections...)
		}
	}
	newSessionID := "session_" + uuid.New().String()
	if c.attachmentStore != nil {
		c.attachmentStore.retain()
	}
	return &ContextManager{
		sessionID:       newSessionID,
		messageList:     newMessageList,
		attachmentStore: c.attachmentStore,
		mu:              sync.RWMutex{},
	}
}

func (c *ContextManager) ConvertToStandardMessageList() []llms.MessageContent {
	messages, _ := c.convertToStandardMessageList()
	return messages
}

func (c *ContextManager) ConvertToStandardMessageListWithCacheHints() ([]llms.MessageContent, PromptCacheHints) {
	return c.convertToStandardMessageList()
}

func (c *ContextManager) convertToStandardMessageList() ([]llms.MessageContent, PromptCacheHints) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	standardMessageList := make([]llms.MessageContent, len(c.messageList))
	hints := PromptCacheHints{}
	for i, message := range c.messageList {
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
		if message.Role == MessageRoleSystem && len(message.PromptSections) > 0 {
			partIndex := 0
			for _, section := range message.PromptSections {
				if text := strings.TrimSpace(section.Text); text != "" {
					newMessage.Parts = append(newMessage.Parts, llms.TextPart(text))
					if section.CacheEphemeral {
						hints.EphemeralParts = append(hints.EphemeralParts, PromptCachePartHint{
							MessageIndex: i,
							PartIndex:    partIndex,
						})
					}
					partIndex++
				}
			}
		} else if content := strings.TrimSpace(message.Content); content != "" {
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
	return standardMessageList, hints
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
