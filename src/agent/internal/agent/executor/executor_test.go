package executor

import (
	"context"
	"errors"
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	"github.com/tmc/langchaingo/llms"
)

type recordingModel struct {
	last []llms.MessageContent
}

type nilResponseModel struct{}

func (nilResponseModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	return nil, nil
}

func (nilResponseModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
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

type rejectingContentPolicy struct {
	err error
}

func (p rejectingContentPolicy) Apply(context.Context, []llms.MessageContent, []llms.CallOption) ([]llms.MessageContent, error) {
	return nil, p.err
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

func TestGenerateContentMarksNilModelResponseAsLLMCallError(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	exec := NewLLMExecutor(nilResponseModel{}, manager)

	response, err := exec.GenerateContent(context.Background())
	if response != nil {
		t.Fatalf("GenerateContent() response = %#v, want nil", response)
	}
	var llmErr *LLMCallError
	if !errors.As(err, &llmErr) {
		t.Fatalf("GenerateContent() error = %T %v, want LLMCallError", err, err)
	}
}

func TestGenerateContentReturnsLocalPolicyErrorWithoutCallingModel(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []contextmanager.Message{
		{Role: contextmanager.MessageRoleSystem, Content: "system"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	model := &recordingModel{}
	localErr := errors.New("context budget exceeded")
	exec := NewLLMExecutor(model, manager)
	exec.SetOutboundContentPolicy(rejectingContentPolicy{err: localErr})

	response, err := exec.GenerateContent(context.Background())
	if response != nil || !errors.Is(err, localErr) {
		t.Fatalf("GenerateContent() response=%#v error=%v", response, err)
	}
	if model.last != nil {
		t.Fatalf("model was called with messages: %#v", model.last)
	}
	var llmErr *LLMCallError
	if errors.As(err, &llmErr) {
		t.Fatalf("local policy error was wrapped as LLMCallError: %v", err)
	}
}
