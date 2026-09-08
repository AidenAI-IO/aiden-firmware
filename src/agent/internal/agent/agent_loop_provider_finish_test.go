package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type providerFinishThenSuccessModel struct {
	calls    int
	failures int
}

func (m *providerFinishThenSuccessModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	if m.calls <= m.failures {
		return nil, &ProviderFinishError{FinishReason: "error", Model: "provider-finish-test", ResponseID: "resp_1"}
	}
	return contentResponse("Recovered"), nil
}

func (m *providerFinishThenSuccessModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("unexpected Call invocation")
}

func (m *providerFinishThenSuccessModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: "fake", Name: "provider-finish-test", ContextWindow: 8_192}
}

func (m *providerFinishThenSuccessModel) CallOptions() []chains.ChainCallOption { return nil }

func TestAgentLoopRetriesProviderFinishErrorOnce(t *testing.T) {
	llmModel := &providerFinishThenSuccessModel{failures: 1}
	manager, err := freshNewContextManager("system", "continue", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		llmModel,
		RoleProfile{},
		3,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)

	output, err := loop.Run(context.Background(), "continue")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "Recovered" {
		t.Fatalf("output = %q, want Recovered", output)
	}
	if llmModel.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (failed request plus one retry)", llmModel.calls)
	}
}

func TestAgentLoopFailsWhenProviderFinishErrorPersists(t *testing.T) {
	llmModel := &providerFinishThenSuccessModel{failures: 2}
	manager, err := freshNewContextManager("system", "continue", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		llmModel,
		RoleProfile{},
		3,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)

	_, err = loop.Run(context.Background(), "continue")
	if err == nil {
		t.Fatal("Run() error = nil, want provider finish error")
	}
	var finishErr *ProviderFinishError
	if !errors.As(err, &finishErr) {
		t.Fatalf("Run() error = %v, want *ProviderFinishError", err)
	}
	if finishErr.FinishReason != "error" || finishErr.ResponseID != "resp_1" {
		t.Fatalf("ProviderFinishError = %#v", finishErr)
	}
	if llmModel.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (single retry before failing)", llmModel.calls)
	}
}

type emptyResponseModel struct {
	calls int
}

func (m *emptyResponseModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content:    "",
			StopReason: "stop",
		}},
	}, nil
}

func (m *emptyResponseModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("unexpected Call invocation")
}

func (m *emptyResponseModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: "fake", Name: "empty-response-test", ContextWindow: 8_192}
}

func (m *emptyResponseModel) CallOptions() []chains.ChainCallOption { return nil }

func TestAgentLoopNamesResponseDetailWhenAgentHasNoReturn(t *testing.T) {
	llmModel := &emptyResponseModel{}
	manager, err := freshNewContextManager("system", "continue", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		llmModel,
		RoleProfile{},
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)

	_, err = loop.Run(context.Background(), "continue")
	if err == nil {
		t.Fatal("Run() error = nil, want ErrAgentNoReturn")
	}
	if !errors.Is(err, agents.ErrAgentNoReturn) {
		t.Fatalf("Run() error = %v, want agents.ErrAgentNoReturn", err)
	}
	for _, want := range []string{`finish_reason="stop"`, "content_chars=0", "tool_calls=0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Run() error = %v, want detail containing %q", err, want)
		}
	}
}
