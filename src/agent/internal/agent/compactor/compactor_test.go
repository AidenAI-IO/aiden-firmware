package compactor

import (
	"context"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type testModelResolver struct {
	model llms.Model
	spec  model.ModelSpec
}

func (r *testModelResolver) Get() (llms.Model, error) { return r.model, nil }

func (r *testModelResolver) CallOptions() []chains.ChainCallOption { return nil }

func (r *testModelResolver) Spec() model.ModelSpec { return r.spec }

type promptCapturingModel struct {
	prompts []string
	reply   string
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

	compactor := NewCompactor(DefaultProtectRule, &testModelResolver{})
	want := tokencounter.EstimateTextTokens(`{"input":"hello world"}`) + tokencounter.EstimateTextTokens(`{"output":"done"}`)
	if got := compactor.EstimateTokenUsage(manager); got != want {
		t.Fatalf("EstimateTokenUsage() = %d, want %d", got, want)
	}
}

func TestGenerateSummaryIncludesToolPayloads(t *testing.T) {
	model := &promptCapturingModel{reply: "summary"}
	compactor := NewCompactor(DefaultProtectRule, &testModelResolver{model: model})

	got := compactor.generateSummary(context.Background(), []contextmanager.Message{
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
