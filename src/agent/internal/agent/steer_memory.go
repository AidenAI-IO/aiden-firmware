package agent

import (
	"strings"
	"sync"
)

type steerConversationRecorder interface {
	RecordSteer(steer RunSteerMessage) error
}

type steerConversationStatus interface {
	HasSteerMessages() bool
	SteerMessages() []RunSteerMessage
}

// steerConversationTracker records out-of-band steering metadata for the
// session event stream. The steer's model-facing content is persisted
// separately by ContextManager, which is the sole conversation context.
type steerConversationTracker struct {
	mu     sync.Mutex
	steers []RunSteerMessage
}

func newSteerConversationTracker() *steerConversationTracker {
	return &steerConversationTracker{}
}

func (t *steerConversationTracker) RecordSteer(steer RunSteerMessage) error {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		content = "(empty steering message)"
	}
	steer.Content = content

	t.mu.Lock()
	t.steers = append(t.steers, steer)
	t.mu.Unlock()
	return nil
}

func (t *steerConversationTracker) HasSteerMessages() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.steers) > 0
}

func (t *steerConversationTracker) SteerMessages() []RunSteerMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RunSteerMessage(nil), t.steers...)
}
