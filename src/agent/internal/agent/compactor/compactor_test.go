package compactor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type testModel struct {
	llms.Model
	model.ModelSpec
}

func (m *testModel) CallOptions() []chains.ChainCallOption { return nil }

func (m *testModel) Spec() model.ModelSpec { return m.ModelSpec }

type promptCapturingModel struct {
	prompts []string
	reply   string
}

type failingSummaryModel struct {
	err error
}

func (m failingSummaryModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", m.err
}

func (m failingSummaryModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	return nil, m.err
}

func (m *promptCapturingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func (m *promptCapturingModel) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	var prompt strings.Builder
	for _, message := range messages {
		for _, part := range message.Parts {
			text, ok := part.(llms.TextContent)
			if !ok {
				continue
			}
			prompt.WriteString(text.Text)
		}
	}
	m.prompts = append(m.prompts, prompt.String())
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: m.reply,
		}},
	}, nil
}

func TestEstimateTokenUsageCountsToolPayloads(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := contextmanager.NewContextManagerFromMessageList(sessionFolder, []contextmanager.Message{
		{
			Role: contextmanager.MessageRoleToolCall,
			ToolCalls: []contextmanager.ToolCall{{
				ID:        "call_1",
				Name:      "echo",
				Arguments: `{"input":"hello world"}`,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_1",
				Name:       "echo",
				Content:    `{"output":"done"}`,
			}},
		},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}

	compactor := NewCompactor(DefaultProtectRule, &testModel{})
	want := tokencounter.EstimateTextTokens(`{"input":"hello world"}`) + tokencounter.EstimateTextTokens(`{"output":"done"}`)
	if got := compactor.EstimateTokenUsage(manager); got != want {
		t.Fatalf("EstimateTokenUsage() = %d, want %d", got, want)
	}
}

func TestGenerateSummaryIncludesToolPayloads(t *testing.T) {
	model := &promptCapturingModel{reply: "summary"}
	compactor := NewCompactor(DefaultProtectRule, &testModel{Model: model})

	got, err := compactor.generateSummary(context.Background(), []contextmanager.Message{
		{
			Role: contextmanager.MessageRoleToolCall,
			ToolCalls: []contextmanager.ToolCall{{
				ID:        "call_1",
				Name:      "echo",
				Arguments: `{"input":"hello"}`,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_1",
				Name:       "echo",
				Content:    `{"output":"world"}`,
			}},
		},
	})
	if err != nil {
		t.Fatalf("generateSummary() error = %v", err)
	}
	if got != "summary" {
		t.Fatalf("generateSummary() = %q, want summary", got)
	}
	if len(model.prompts) != 1 {
		t.Fatalf("summary prompts = %d, want 1", len(model.prompts))
	}
	prompt := model.prompts[0]
	for _, want := range []string{
		"tool_call_name: echo",
		`tool_call_arguments: {"input":"hello"}`,
		`tool_call_result: {"output":"world"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("summary prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCompactPreservesLLMFailureSource(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []contextmanager.Message{
		{Role: contextmanager.MessageRoleUser, Content: "one"},
		{Role: contextmanager.MessageRoleAssistant, Content: "two"},
		{Role: contextmanager.MessageRoleUser, Content: "three"},
		{Role: contextmanager.MessageRoleAssistant, Content: "four"},
		{Role: contextmanager.MessageRoleUser, Content: "five"},
		{Role: contextmanager.MessageRoleAssistant, Content: "six"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	compactor := NewCompactor(DefaultProtectRule, &testModel{
		Model: failingSummaryModel{err: errors.New("API error 429: insufficient_quota")},
	})

	_, _, err = compactor.Compact(context.Background(), manager)
	if err == nil {
		t.Fatal("Compact() error = nil, want LLM failure")
	}
	var llmErr *executor.LLMCallError
	if !errors.As(err, &llmErr) {
		t.Fatalf("Compact() error = %T %v, want LLMCallError", err, err)
	}
}
