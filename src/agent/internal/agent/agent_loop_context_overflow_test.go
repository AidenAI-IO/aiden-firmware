package agent

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type contextOverflowThenSuccessModel struct {
	calls int
}

func (m *contextOverflowThenSuccessModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	if m.calls == 1 {
		return nil, newProviderHTTPError(http.StatusBadRequest, []byte(
			`{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`,
		))
	}
	return contentResponse("Recovered"), nil
}

func (m *contextOverflowThenSuccessModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("unexpected Call invocation")
}

func (m *contextOverflowThenSuccessModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: "fake", Name: "context-overflow-test", ContextWindow: 8_192}
}

func (m *contextOverflowThenSuccessModel) CallOptions() []chains.ChainCallOption { return nil }

func TestAgentLoopCompactsAndRetriesProviderContextExceededError(t *testing.T) {
	model := &contextOverflowThenSuccessModel{}
	manager, err := freshNewContextManager("system", "continue", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	replacement, err := contextmanager.NewContextManagerRevisionFromMessageList(manager, manager.CloneMessageList())
	if err != nil {
		t.Fatalf("NewContextManagerRevisionFromMessageList() error = %v", err)
	}

	loop := NewAgentLoop(
		model,
		RoleProfile{},
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	recoveryCalls := 0
	loop.ContextOverflowRecovery = func(_ context.Context, current *contextmanager.ContextManager) (*contextmanager.ContextManager, bool, error) {
		recoveryCalls++
		if current != manager {
			t.Fatalf("recovery current manager = %p, want %p", current, manager)
		}
		return replacement, true, nil
	}

	output, err := loop.Run(context.Background(), "continue")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "Recovered" {
		t.Fatalf("output = %q, want Recovered", output)
	}
	if model.calls != 2 || recoveryCalls != 1 {
		t.Fatalf("calls = model:%d recovery:%d, want 2/1", model.calls, recoveryCalls)
	}
	if loop.contextManager != replacement {
		t.Fatal("agent loop did not switch to the compacted context manager")
	}
}
