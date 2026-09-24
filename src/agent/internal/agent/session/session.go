package session

import (
	"aiden-agent/internal/agent/messages"
	"sync"
)

type Session struct {
	sessionID       string
	parentSessionID string
	messageList     []messages.Message
	attachmentStore *attachmentStore
	artifactStore   *artifactStore
	mu              sync.RWMutex
}

func (s *Session) SessionID() string {
	return s.sessionID
}
