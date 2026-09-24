package session

import (
	"aiden-agent/internal/agent/messages"
	"errors"
	"sync"
)

// Session is the durable conversation state owned by a context manager.
//
// A Session has immutable identity and lineage. Its message list is protected
// by its own lock so callers can keep a session value while the context
// manager changes which session is active.
type Session struct {
	sessionID       string
	parentSessionID string
	messageList     []messages.Message
	attachmentStore AttachmentStore
	artifactStore   ArtifactStore
	persist         func([]messages.Message) error
	version         uint64
	mu              sync.RWMutex
}

// New creates an in-memory session. The message list is deep-cloned so the
// caller can safely reuse or mutate its input after construction.
func New(sessionID, parentSessionID string, messageList []messages.Message) *Session {
	return NewWithStores(sessionID, parentSessionID, messageList, nil, nil)
}

// NewWithStores creates a session and associates its session-scoped resource
// stores with it. Stores are optional so the value remains useful for pure
// in-memory callers and tests.
func NewWithStores(sessionID, parentSessionID string, messageList []messages.Message, attachmentStore AttachmentStore, artifactStore ArtifactStore) *Session {
	return NewWithStoresAndPersistence(sessionID, parentSessionID, messageList, attachmentStore, artifactStore, nil)
}

// NewWithStoresAndPersistence creates a session with optional resource stores
// and an append callback. The callback is invoked before the in-memory message
// list changes, preserving the durable-session write ordering.
func NewWithStoresAndPersistence(sessionID, parentSessionID string, messageList []messages.Message, attachmentStore AttachmentStore, artifactStore ArtifactStore, persist func([]messages.Message) error) *Session {
	return &Session{
		sessionID:       sessionID,
		parentSessionID: parentSessionID,
		messageList:     cloneMessages(messageList),
		attachmentStore: attachmentStore,
		artifactStore:   artifactStore,
		persist:         persist,
		version:         uint64(len(messageList)),
	}
}

// ID returns the immutable identifier of the session.
func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return s.sessionID
}

func (s *Session) SessionID() string {
	return s.ID()
}

// ParentID returns the identifier of the session from which this session was
// derived. Root sessions return an empty string.
func (s *Session) ParentID() string {
	if s == nil {
		return ""
	}
	return s.parentSessionID
}

// ParentSessionID is kept as a descriptive alias for callers that use the
// context-manager terminology.
func (s *Session) ParentSessionID() string {
	return s.ParentID()
}

// CloneMessageList returns a deep copy of the messages currently in the
// session.
func (s *Session) CloneMessageList() []messages.Message {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messageList)
}

// Snapshot returns a deep copy of the messages and the generation from the
// same lock acquisition.
func (s *Session) Snapshot() ([]messages.Message, uint64) {
	if s == nil {
		return nil, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messageList), s.version
}

// IsEmpty reports whether the session contains no messages.
func (s *Session) IsEmpty() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messageList) == 0
}

// Version is a monotonically increasing message generation. It lets a
// context manager reject activation of a revision prepared from a stale
// snapshot.
func (s *Session) Version() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// AppendMessages commits messages to the session. When the session was created
// with a persistence callback, that callback completes before the in-memory
// list changes. This keeps direct session users subject to the same
// write-before-memory-update invariant as context-manager appends.
func (s *Session) AppendMessages(messageList []messages.Message) error {
	if s == nil || len(messageList) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	messageList = cloneMessages(messageList)
	if s.persist != nil {
		if err := s.persist(cloneMessages(messageList)); err != nil {
			return err
		}
	}
	s.messageList = append(s.messageList, messageList...)
	s.version += uint64(len(messageList))
	return nil
}

// StoreAttachment writes bytes to this session's attachment store.
func (s *Session) StoreAttachment(mimeType string, data []byte) (messages.Attachment, error) {
	if s == nil || s.attachmentStore == nil {
		return messages.Attachment{}, errors.New("attachment store is unavailable")
	}
	return s.attachmentStore.StoreAttachment(mimeType, data)
}

// StoreArtifact writes a tool-result artifact to this session's artifact store.
func (s *Session) StoreArtifact(mimeType string, data []byte, metadata ArtifactMetadata) (ArtifactFile, error) {
	if s == nil || s.artifactStore == nil {
		return ArtifactFile{}, errors.New("artifact store is unavailable")
	}
	return s.artifactStore.StoreArtifact(mimeType, data, metadata)
}

// ArtifactStoreRoot exposes the store root for maintenance code and diagnostics
// without exposing the concrete storage implementation.
func (s *Session) ArtifactStoreRoot() string {
	if s == nil || s.artifactStore == nil {
		return ""
	}
	if rooted, ok := s.artifactStore.(interface{ Root() string }); ok {
		return rooted.Root()
	}
	return ""
}

func cloneMessages(messageList []messages.Message) []messages.Message {
	if len(messageList) == 0 {
		return nil
	}
	cloned := make([]messages.Message, len(messageList))
	for i, message := range messageList {
		cloned[i] = message.Clone()
	}
	return cloned
}
