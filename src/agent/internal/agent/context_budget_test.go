package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
)

func TestHardContextGuardAllowsRequestWithinBudget(t *testing.T) {
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}
	guard := NewHardContextGuard(model.ModelSpec{ContextWindow: 4_000, MaxOutput: 500})
	got, err := guard.Apply(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(got) != len(messages) {
		t.Fatalf("Apply() messages = %d, want %d", len(got), len(messages))
	}
}

func TestHardContextGuardRejectsOverBudgetWithoutOmittingToolResults(t *testing.T) {
	rawResult := strings.Repeat("large-result ", 500)
	messages := contextBudgetToolMessages(rawResult)
	guard := NewHardContextGuard(model.ModelSpec{ContextWindow: 1_000, MaxOutput: 200})

	got, err := guard.Apply(context.Background(), messages, nil)
	if got != nil {
		t.Fatalf("Apply() messages = %#v, want nil on rejection", got)
	}
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Apply() error = %T %v, want ContextBudgetExceededError", err, err)
	}
	if budgetErr.EstimatedPromptTokens <= budgetErr.InputBudget {
		t.Fatalf("budget error = %#v, want estimated > budget", budgetErr)
	}
	if !strings.Contains(rawResult, "large-result") {
		t.Fatal("test setup mutated raw result")
	}
}

func TestHardContextGuardCountsToolSchemaTokens(t *testing.T) {
	messages := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, strings.Repeat("x", 1_800))}
	largeDescription := strings.Repeat("schema ", 300)
	options := []llms.CallOption{llms.WithTools([]llms.Tool{{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "large_tool",
			Description: largeDescription,
			Parameters:  map[string]any{"type": "object"},
		},
	}})}
	guard := NewHardContextGuard(model.ModelSpec{ContextWindow: 1_200, MaxOutput: 200})
	_, err := guard.Apply(context.Background(), messages, options)
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Apply() error = %T %v, want ContextBudgetExceededError", err, err)
	}
	if budgetErr.ToolSchemaTokens == 0 {
		t.Fatalf("Apply() tool schema tokens = 0")
	}
}

func contextBudgetToolMessages(outputs ...string) []llms.MessageContent {
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system prompt"),
		llms.TextParts(llms.ChatMessageTypeHuman, "summarize tool output"),
	}
	for i, output := range outputs {
		id := fmt.Sprintf("call_%d", i+1)
		messages = append(messages,
			llms.MessageContent{
				Role: llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.ToolCall{
					ID:   id,
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "dump",
						Arguments: "{}",
					},
				}},
			},
			llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{llms.ToolCallResponse{
					ToolCallID: id,
					Name:       "dump",
					Content:    output,
				}},
			},
		)
	}
	return messages
}

func toolResponseContents(t *testing.T, messages []llms.MessageContent) []string {
	t.Helper()
	var contents []string
	for _, message := range messages {
		for _, part := range message.Parts {
			if response, ok := part.(llms.ToolCallResponse); ok {
				contents = append(contents, response.Content)
			}
		}
	}
	if len(contents) == 0 {
		t.Fatalf("no tool responses found in messages: %#v", messages)
	}
	return contents
}
