package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestGuardMessagesWithinContextWindowFallbackOmitsCombinedToolResults(t *testing.T) {
	messages := contextBudgetToolMessages(
		strings.Repeat("alpha ", 240),
		strings.Repeat("bravo ", 240),
	)
	inputBudget := maxSingleToolResponsePromptTokens(t, messages) + 12
	if got := estimateMessagesTokens(messages); got <= inputBudget {
		t.Fatalf("test setup total tokens = %d, want > %d", got, inputBudget)
	}

	executor := &roleCollaborativeExecutor{
		Model: contextBudgetWindowModel{window: inputBudget},
	}

	sanitized := executor.guardMessagesWithinContextWindow(messages, nil)
	if got := estimateMessagesTokens(sanitized); got > inputBudget {
		t.Fatalf("sanitized tokens = %d, want <= %d", got, inputBudget)
	}
	contents := toolResponseContents(t, sanitized)
	if !strings.Contains(contents[0], "tool result omitted") || !strings.Contains(contents[1], "tool result omitted") {
		t.Fatalf("combined tool responses should be omitted by fallback pass: %#v", contents)
	}
}

func TestGuardMessagesWithinContextWindowDoesNotTreatRawOmissionTextAsAlreadyOmitted(t *testing.T) {
	oversizedOutput := strings.Repeat("oversized ", 720)
	rawOmissionTextOutput := "raw diagnostic says tool result omitted by the remote process\n" + strings.Repeat("secondary ", 160)
	messages := contextBudgetToolMessages(oversizedOutput, rawOmissionTextOutput)
	candidates := collectToolResponseBudgetCandidates(messages)
	if len(candidates) != 2 {
		t.Fatalf("test setup candidates = %d, want 2", len(candidates))
	}
	inputBudget := estimateSingleToolResponsePromptTokens(messages, candidates[1]) + 12
	if got := estimateSingleToolResponsePromptTokens(messages, candidates[0]); got <= inputBudget {
		t.Fatalf("oversized single prompt tokens = %d, want > %d", got, inputBudget)
	}
	if got := estimateMessagesTokens(messages); got <= inputBudget {
		t.Fatalf("test setup total tokens = %d, want > %d", got, inputBudget)
	}

	executor := &roleCollaborativeExecutor{
		Model: contextBudgetWindowModel{window: inputBudget},
	}

	sanitized := executor.guardMessagesWithinContextWindow(messages, nil)
	contents := toolResponseContents(t, sanitized)
	if contents[1] == rawOmissionTextOutput {
		t.Fatalf("raw tool output containing omission text should not be treated as already omitted")
	}
	if !strings.Contains(contents[1], "context_window=") {
		t.Fatalf("second tool response should be replaced with omission metadata, got: %q", contents[1])
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

func maxSingleToolResponsePromptTokens(t *testing.T, messages []llms.MessageContent) int {
	t.Helper()
	candidates := collectToolResponseBudgetCandidates(messages)
	if len(candidates) == 0 {
		t.Fatal("no tool response candidates")
	}
	maxTokens := 0
	for _, candidate := range candidates {
		tokens := estimateSingleToolResponsePromptTokens(messages, candidate)
		if tokens > maxTokens {
			maxTokens = tokens
		}
	}
	return maxTokens
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

type contextBudgetWindowModel struct {
	window int
}

func (m contextBudgetWindowModel) contextWindow() int {
	return m.window
}

func (m contextBudgetWindowModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	panic("unexpected GenerateContent invocation")
}

func (m contextBudgetWindowModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}
