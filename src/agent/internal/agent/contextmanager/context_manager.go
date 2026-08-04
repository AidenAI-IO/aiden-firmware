package contextmanager

import (
	"encoding/json"
	"fmt"
	"log"
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
	Role                   MessageRole             `json:"role"`
	Content                string                  `json:"content"`
	ToolCalls              []ToolCall              `json:"tool_calls,omitempty"`
	ToolResults            []ToolResult            `json:"tool_results,omitempty"`
	RecoverableToolResults []RecoverableToolResult `json:"recoverable_tool_results,omitempty"`
	Attachments            []Attachment            `json:"attachments,omitempty"`
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
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Content    string          `json:"content"`
	Meta       *ToolResultMeta `json:"meta,omitempty"`
}

type ToolResultMeta struct {
	ArtifactPath        string `json:"artifact_path,omitempty"`
	OriginalBytes       int64  `json:"original_bytes,omitempty"`
	OriginalChars       int    `json:"original_chars,omitempty"`
	EstimatedTokens     int    `json:"estimated_tokens,omitempty"`
	Complete            bool   `json:"complete"`
	ArtifactComplete    bool   `json:"artifact_complete"`
	Reason              string `json:"reason,omitempty"`
	Summary             string `json:"summary,omitempty"`
	ActionCompleted     bool   `json:"action_completed,omitempty"`
	ObservationComplete bool   `json:"observation_complete,omitempty"`
	ProcessingErrorCode string `json:"processing_error_code,omitempty"`
	ArtifactStoreError  string `json:"artifact_store_error,omitempty"`
	legacyArtifactRef   string
}

// RecoverableToolResult carries trusted recovery metadata across compaction
// revisions. It is persisted with the context message but is not sent to the
// model separately from the bounded recovery block in Message.Content.
type RecoverableToolResult struct {
	ToolName         string `json:"tool_name"`
	ArtifactPath     string `json:"artifact_path"`
	ArtifactComplete bool   `json:"artifact_complete"`
	Summary          string `json:"summary,omitempty"`
}

func (m *ToolResultMeta) UnmarshalJSON(data []byte) error {
	type alias ToolResultMeta
	decoded := struct {
		*alias
		ArtifactRef string `json:"artifact_ref,omitempty"`
	}{alias: (*alias)(m)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	m.legacyArtifactRef = decoded.ArtifactRef
	return nil
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
	artifactScopeID string
	messageList     []Message
	appendHooks     []AppendMessageHook
	attachmentStore *attachmentStore
	artifactStore   *artifactStore
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
	metadata, found, err := loadSessionMetadata(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(metadata.ArtifactScopeID) == "" {
		metadata.ArtifactScopeID = sessionID
		if err := saveSessionMetadata(sessionFolder, sessionID, metadata); err != nil {
			return nil, err
		}
	}

	attachmentStore, err := newAttachmentStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	artifactStore, err := newArtifactStore(sessionFolder, metadata.ArtifactScopeID)
	if err != nil {
		return nil, err
	}
	// Keep legacy reference migration in memory here. Session JSONL files are
	// append-only, so flushFull would duplicate every loaded message instead of
	// rewriting them. A later session revision persists the migrated messages.
	migrateLegacyArtifactRefs(messageList, artifactStore)

	return &ContextManager{
		sessionID:       sessionID,
		artifactScopeID: metadata.ArtifactScopeID,
		messageList:     messageList,
		mu:              sync.RWMutex{},
		sessionFolder:   sessionFolder,
		attachmentStore: attachmentStore,
		artifactStore:   artifactStore,
	}, nil
}

func migrateLegacyArtifactRefs(messageList []Message, store *artifactStore) {
	if store == nil {
		return
	}
	const legacyPrefix = "artifact://"
	const shellGuidance = "Use shell commands such as grep, sed, dd, jq, or fq to read only the needed ranges or fields."
	for messageIndex := range messageList {
		for resultIndex := range messageList[messageIndex].ToolResults {
			result := &messageList[messageIndex].ToolResults[resultIndex]
			if result.Meta == nil || result.Meta.ArtifactPath != "" {
				continue
			}
			ref := strings.TrimSpace(result.Meta.legacyArtifactRef)
			if !strings.HasPrefix(ref, legacyPrefix) {
				continue
			}
			id := strings.TrimPrefix(ref, legacyPrefix)
			if !strings.HasPrefix(id, "tr_") {
				continue
			}
			if _, err := uuid.Parse(strings.TrimPrefix(id, "tr_")); err != nil {
				continue
			}

			artifactPath := filepath.Join(store.root, id+".data")
			if _, err := os.Stat(artifactPath); err != nil {
				continue
			}
			result.Meta.ArtifactPath = artifactPath
			result.Meta.legacyArtifactRef = ""
			result.Content = strings.ReplaceAll(result.Content, "Full result: "+ref, "Full result file: "+result.Meta.ArtifactPath)
			result.Content = strings.ReplaceAll(result.Content, "Saved partial result: "+ref, "Saved partial result file: "+result.Meta.ArtifactPath)
			result.Content = strings.ReplaceAll(result.Content, ref, result.Meta.ArtifactPath)
			if !strings.Contains(result.Content, shellGuidance) {
				if strings.TrimSpace(result.Content) != "" {
					result.Content = strings.TrimSpace(result.Content) + "\n"
				}
				result.Content += shellGuidance
			}
		}
	}
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
	return newContextManagerFromMessageList(sessionFolder, newSessionID, newSessionID, messageList)
}

func NewContextManagerRevisionFromMessageList(parent *ContextManager, messageList []Message) (*ContextManager, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent context manager is nil")
	}
	return newContextManagerFromMessageList(
		parent.GetSessionFolder(),
		newSessionID(),
		parent.GetArtifactScopeID(),
		messageList,
	)
}

func newContextManagerFromMessageList(sessionFolder, sessionID, artifactScopeID string, messageList []Message) (*ContextManager, error) {
	if strings.TrimSpace(artifactScopeID) == "" {
		artifactScopeID = sessionID
	}
	if err := saveSessionMetadata(sessionFolder, sessionID, sessionMetadata{ArtifactScopeID: artifactScopeID}); err != nil {
		return nil, err
	}
	attachmentStore, err := newAttachmentStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	artifactStore, err := newArtifactStore(sessionFolder, artifactScopeID)
	if err != nil {
		return nil, err
	}
	manager := &ContextManager{
		sessionFolder:   sessionFolder,
		sessionID:       sessionID,
		artifactScopeID: artifactScopeID,
		messageList:     cloneMessages(messageList),
		mu:              sync.RWMutex{},
		attachmentStore: attachmentStore,
		artifactStore:   artifactStore,
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

func (c *ContextManager) GetArtifactScopeID() string {
	if c == nil {
		return ""
	}
	return c.artifactScopeID
}

func (c *ContextManager) StoreArtifact(mimeType string, data []byte, metadata ArtifactMetadata) (ArtifactFile, error) {
	if c == nil || c.artifactStore == nil {
		return ArtifactFile{}, fmt.Errorf("artifact store is unavailable")
	}
	return c.artifactStore.store(mimeType, data, metadata)
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

// ReadScreenshotAttachment returns a screenshot attachment registered in the
// active context. The caller supplies only the opaque attachment filename that
// was shown to the model; arbitrary paths and non-screenshot attachments are
// rejected.
func (c *ContextManager) ReadScreenshotAttachment(attachmentID string) ([]byte, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" || filepath.Base(attachmentID) != attachmentID || strings.ContainsAny(attachmentID, `/\\`) {
		return nil, fmt.Errorf("invalid screenshot attachment ID")
	}

	c.mu.RLock()
	filePath := ""
	for _, message := range c.messageList {
		for _, attachment := range message.Attachments {
			if attachment.Source != AttachmentSourceScreenshotObservation {
				continue
			}
			candidate := strings.TrimSpace(attachment.FilePath)
			if candidate != "" && filepath.Base(candidate) == attachmentID {
				filePath = candidate
				break
			}
		}
		if filePath != "" {
			break
		}
	}
	sessionFolder := c.sessionFolder
	c.mu.RUnlock()

	if filePath == "" {
		return nil, fmt.Errorf("attachment is not present in the active context")
	}
	root, err := filepath.Abs(sessionFolder)
	if err != nil {
		return nil, fmt.Errorf("resolve session folder: %w", err)
	}
	candidate, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment path: %w", err)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("attachment path is outside the session folder")
	}

	data, err := os.ReadFile(candidate)
	if err != nil {
		return nil, fmt.Errorf("read screenshot attachment: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("screenshot attachment is empty")
	}
	return data, nil
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
		for i := range cloned.ToolResults {
			if msg.ToolResults[i].Meta == nil {
				continue
			}
			meta := *msg.ToolResults[i].Meta
			cloned.ToolResults[i].Meta = &meta
		}
	}
	if len(msg.RecoverableToolResults) > 0 {
		cloned.RecoverableToolResults = append([]RecoverableToolResult(nil), msg.RecoverableToolResults...)
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
