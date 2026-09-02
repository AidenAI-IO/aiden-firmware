package contextmanager

import (
	"aiden-agent/internal/agent/messages"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type AppendMessageHook func(messages.Message) AppendMessageHookResult

type AppendMessageHookResult struct {
	Before  []messages.Message
	Message *messages.Message
	After   []messages.Message
}

// ContextManager is a manager for the context of the agent, it is used to manage the context of the agent, it is used to append messages to the context and to fork the context.
// It is thread safe and can be used concurrently by multiple goroutines.
// SessionID is the id of the session, it is used to identify the session of the agent. Conversation in a same session are shared the same context.
// ParentSessionID records the session this one was derived from, for example the
// pre-compaction session of a compaction revision. It is empty for root sessions.
type ContextManager struct {
	sessionID       string
	parentSessionID string
	messageList     []messages.Message
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

	attachmentStore, err := newAttachmentStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	artifactStore, err := newArtifactStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}

	// A missing or unreadable sidecar leaves lineage unknown rather than failing
	// the load: sessions written before the sidecar existed remain usable.
	parentSessionID := ""
	metadata, found, err := loadSessionMetadata(sessionFolder, sessionID)
	switch {
	case err != nil:
		log.Printf("[CM] Failed to load session metadata for %s: %v\n", sessionID, err)
	case found:
		parentSessionID = strings.TrimSpace(metadata.ParentSessionID)
	}

	return &ContextManager{
		sessionID:       sessionID,
		parentSessionID: parentSessionID,
		messageList:     messageList,
		mu:              sync.RWMutex{},
		sessionFolder:   sessionFolder,
		attachmentStore: attachmentStore,
		artifactStore:   artifactStore,
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
	if err := saveSessionMetadata(sessionFolder, newSessionID, sessionMetadata{
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}

	manager, err := LoadContextManagerFromSessionID(sessionFolder, newSessionID)
	if err != nil {
		return nil, err
	}

	// sessionID is new, so system prompt is necessary
	if err := manager.AppendMessage(messages.Message{
		Role:    messages.MessageRoleSystem,
		Content: systemPrompt,
	}); err != nil {
		return nil, err
	}

	return manager, nil
}

func NewContextManagerFromMessageList(sessionFolder string, messageList []messages.Message) (*ContextManager, error) {
	return newContextManagerFromMessageList(sessionFolder, newSessionID(), "", messageList)
}

// NewContextManagerRevisionFromMessageList creates a new session that continues
// parent's conversation, for example after compaction. The revision records
// parent's session ID in its metadata sidecar so the lineage stays traceable.
func NewContextManagerRevisionFromMessageList(parent *ContextManager, messageList []messages.Message) (*ContextManager, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent context manager is nil")
	}
	return newContextManagerFromMessageList(parent.GetSessionFolder(), newSessionID(), parent.GetSessionID(), messageList)
}

func newContextManagerFromMessageList(sessionFolder, sessionID, parentSessionID string, messageList []messages.Message) (*ContextManager, error) {
	attachmentStore, err := newAttachmentStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	artifactStore, err := newArtifactStore(sessionFolder, sessionID)
	if err != nil {
		return nil, err
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	now := time.Now().UTC()
	if err := saveSessionMetadata(sessionFolder, sessionID, sessionMetadata{
		ParentSessionID: parentSessionID,
		CreatedAt:       now,
	}); err != nil {
		return nil, err
	}
	manager := &ContextManager{
		sessionFolder:   sessionFolder,
		sessionID:       sessionID,
		parentSessionID: parentSessionID,
		messageList:     cloneMessages(messageList),
		mu:              sync.RWMutex{},
		attachmentStore: attachmentStore,
		artifactStore:   artifactStore,
	}
	for i := range manager.messageList {
		if manager.messageList[i].Timestamp.IsZero() {
			manager.messageList[i].Timestamp = now
		}
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

func (c *ContextManager) CloneMessageList() []messages.Message {
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

// GetParentSessionID returns the session this context was derived from, or an
// empty string for a root session or a session with no recorded lineage.
func (c *ContextManager) GetParentSessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.parentSessionID
}

func (c *ContextManager) StoreArtifact(mimeType string, data []byte, metadata ArtifactMetadata) (ArtifactFile, error) {
	if c == nil || c.artifactStore == nil {
		return ArtifactFile{}, fmt.Errorf("artifact store is unavailable")
	}
	return c.artifactStore.store(mimeType, data, metadata)
}

func (c *ContextManager) appendToList(messages []messages.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages = repairToolCallTailBeforeAppend(c.messageList, messages)
	if len(messages) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range messages {
		if messages[i].Timestamp.IsZero() {
			messages[i].Timestamp = now
		}
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
	return appendSession(c.sessionFolder, c.sessionID, messages)
}

func (c *ContextManager) AppendMessage(message messages.Message) error {
	return c.AppendMessages([]messages.Message{message})
}

// AppendMessages applies append hooks to a batch and persists the resulting
// messages in one context-manager append operation. This keeps related
// protocol messages, such as a tool call and its result, together.
func (c *ContextManager) AppendMessages(messagesToAppend []messages.Message) error {
	c.mu.RLock()
	hooks := append([]AppendMessageHook(nil), c.appendHooks...)
	c.mu.RUnlock()

	messageList := cloneMessages(messagesToAppend)
	for _, entry := range hooks {
		var next []messages.Message
		for _, current := range messageList {
			result := entry(current.Clone())
			next = append(next, cloneMessages(result.Before)...)
			if result.Message != nil {
				next = append(next, result.Message.Clone())
			}
			next = append(next, cloneMessages(result.After)...)
		}
		messageList = next
	}

	if len(messageList) == 0 {
		return nil
	}
	return c.appendToList(messageList)
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
	SessionID       string             `json:"session_id"`
	ParentSessionID string             `json:"parent_session_id,omitempty"`
	Messages        []messages.Message `json:"messages"`
}

func (c *ContextManager) MessageListDump() MessageListDump {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return MessageListDump{
		SessionID:       c.sessionID,
		ParentSessionID: c.parentSessionID,
		Messages:        cloneMessages(c.messageList),
	}
}

// StoreAttachment persists attachment bytes on disk and returns metadata only.
func (c *ContextManager) StoreAttachment(mimeType string, data []byte) (messages.Attachment, error) {
	if len(data) == 0 {
		return messages.Attachment{}, fmt.Errorf("attachment data is empty")
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return c.attachmentStore.store(mimeType, data)
}

// ReadAttachment returns a registered attachment from the active context. The
// caller supplies only the opaque attachment filename; arbitrary paths are
// rejected.
func (c *ContextManager) ReadAttachment(attachmentID string) ([]byte, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" || filepath.Base(attachmentID) != attachmentID || strings.ContainsAny(attachmentID, `/\\`) {
		return nil, fmt.Errorf("invalid attachment ID")
	}

	c.mu.RLock()
	filePath := ""
	for _, message := range c.messageList {
		for _, attachment := range message.Attachments {
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
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("attachment is empty")
	}
	return data, nil
}

func cloneMessages(messageList []messages.Message) []messages.Message {
	if len(messageList) == 0 {
		return nil
	}
	cloned := make([]messages.Message, len(messageList))
	for i, msg := range messageList {
		cloned[i] = msg.Clone()
	}
	return cloned
}
