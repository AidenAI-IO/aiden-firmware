package agent

import (
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
	err := checkHardContextBudget(model.ModelSpec{ContextWindow: 4_000, MaxOutput: 500}, messages, nil, nil)
	if err != nil {
		t.Fatalf("checkHardContextBudget() error = %v", err)
	}
}

func TestHardContextGuardRejectsOverBudgetWithoutOmittingToolResults(t *testing.T) {
	rawResult := strings.Repeat("large-result ", 500)
	messages := contextBudgetToolMessages(rawResult)
	err := checkHardContextBudget(model.ModelSpec{ContextWindow: 1_000, MaxOutput: 200}, messages, nil, nil)
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
	err := checkHardContextBudget(model.ModelSpec{ContextWindow: 1_200, MaxOutput: 200}, messages, options, nil)
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Apply() error = %T %v, want ContextBudgetExceededError", err, err)
	}
	if budgetErr.ToolSchemaTokens == 0 {
		t.Fatalf("Apply() tool schema tokens = 0")
	}
}

func TestHardContextGuardReportsBudgetTelemetry(t *testing.T) {
	var events []ContextBudgetTelemetry
	err := checkHardContextBudget(
		model.ModelSpec{ContextWindow: 1_000, MaxOutput: 200},
		contextBudgetToolMessages(strings.Repeat("large-result ", 500)),
		nil,
		func(event ContextBudgetTelemetry) { events = append(events, event) },
	)
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Apply() error = %T %v, want ContextBudgetExceededError", err, err)
	}
	if len(events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(events))
	}
	event := events[0]
	if !event.HardGuardRejected || event.EstimatedPromptTokens != budgetErr.EstimatedPromptTokens || event.EstimatedInputBudget != budgetErr.InputBudget {
		t.Fatalf("telemetry event = %#v, budget error = %#v", event, budgetErr)
	}
}

func TestProviderContextLengthErrorClassification(t *testing.T) {
	for _, message := range []string{
		"maximum context length exceeded",
		"context_length_exceeded",
		"context_window_exceeded",
		"prompt is too long for this model",
	} {
		if !isProviderContextLengthError(errors.New(message)) {
			t.Fatalf("isProviderContextLengthError(%q) = false", message)
		}
	}
	if isProviderContextLengthError(errors.New("provider connection reset")) {
		t.Fatal("connection error classified as context length error")
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
