package executor

import (
	"context"
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	"github.com/tmc/langchaingo/llms"
)

type recordingModel struct {
	last []llms.MessageContent
}

func (m *recordingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.last = messages
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "ok"}}}, nil
}

func (m *recordingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return "ok", nil
}

type dropUserTransform struct{}

func (dropUserTransform) Transform(messages []contextmanager.Message) []contextmanager.Message {
	out := make([]contextmanager.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == contextmanager.MessageRoleUser {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func TestGenerateContentAppliesOutboundTransformsWithoutMutatingStore(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := contextmanager.NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleSystem, Content: "sys"}); err != nil {
		t.Fatalf("append system: %v", err)
	}
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "user"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	model := &recordingModel{}
	exec := NewLLMExecutor(model, manager, dropUserTransform{})
	if _, err := exec.GenerateContent(context.Background()); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if len(model.last) != 1 || model.last[0].Role != llms.ChatMessageTypeSystem {
		t.Fatalf("model messages = %#v, want only system", model.last)
	}
	dump := manager.MessageListDump()
	if len(dump.Messages) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(dump.Messages))
	}
}
