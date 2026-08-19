package compactor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"

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

func TestGenerateSummaryIncludesToolPayloads(t *testing.T) {
	model := &promptCapturingModel{reply: "summary"}
	compactor := NewCompactor(DefaultProtectRule, &testModel{Model: model})

	got, err := compactor.generateSummary(context.Background(), []messages.Message{
		{
			Role: messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{{
				ID:        "call_1",
				Name:      "echo",
				Arguments: `{"input":"hello"}`,
			}},
		},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{
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
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []messages.Message{
		{Role: messages.MessageRoleUser, Content: "one"},
		{Role: messages.MessageRoleAssistant, Content: "two"},
		{Role: messages.MessageRoleUser, Content: "three"},
		{Role: messages.MessageRoleAssistant, Content: "four"},
		{Role: messages.MessageRoleUser, Content: "five"},
		{Role: messages.MessageRoleAssistant, Content: "six"},
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

func TestCompactSummarizesHistoricalToolResults(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := contextmanager.NewContextManagerFromMessageList(sessionFolder, []messages.Message{
		{Role: messages.MessageRoleSystem, Content: "system"},
		{Role: messages.MessageRoleUser, Content: "old request"},
		{
			Role:                messages.MessageRoleToolCall,
			ResponsesResponseID: "resp_old_tool",
			ResponsesReasoningItems: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"rs_old"}`),
			},
			ResponsesOutputItems: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","call_id":"old_call","name":"shell","arguments":"{}"}`),
			},
			ResponsesAssistantPhase: "commentary",
			ToolCalls: []messages.ToolCall{{
				ID:        "old_call",
				Name:      "shell",
				Arguments: `{"command":"go test ./..."}`,
			}},
		},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{
				ToolCallID: "old_call",
				Name:       "shell",
				Content:    strings.Repeat("large historical output ", 1_000),
				Meta: &messages.ToolResultMeta{
					ArtifactPath:     "/tmp/tool-results/tr_old",
					OriginalBytes:    24_000,
					OriginalChars:    24_000,
					Complete:         false,
					ArtifactComplete: true,
					Summary:          "128 passed, 2 failed",
				},
			}},
		},
		{Role: messages.MessageRoleAssistant, Content: "old answer", ResponsesResponseID: "resp_old_answer"},
		{Role: messages.MessageRoleUser, Content: "new request"},
		{
			Role:                messages.MessageRoleToolCall,
			ToolCalls:           []messages.ToolCall{{ID: "new_call", Name: "shell", Arguments: `{"command":"pwd"}`}},
			ResponsesResponseID: "resp_new_tool",
			ResponsesReasoningItems: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"rs_new"}`),
			},
			ResponsesOutputItems: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","call_id":"new_call","name":"shell","arguments":"{}"}`),
			},
			ResponsesAssistantPhase: "commentary",
		},
		{
			Role:        messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{ToolCallID: "new_call", Name: "shell", Content: "current result"}},
		},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	model := &promptCapturingModel{reply: "historical work summary"}
	compactor := NewCompactor(DefaultProtectRule, &testModel{Model: model})

	newManager, compacted, err := compactor.Compact(context.Background(), manager)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !compacted || newManager == nil {
		t.Fatal("Compact() did not create a compacted manager")
	}
	if len(model.prompts) != 1 {
		t.Fatalf("summary model calls = %d, want 1", len(model.prompts))
	}
	if newManager.GetSessionID() == manager.GetSessionID() {
		t.Fatalf("compacted manager reused session ID %q", manager.GetSessionID())
	}

	if !strings.Contains(model.prompts[0], "large historical output") {
		t.Fatalf("summary prompt omitted historical tool result:\n%s", model.prompts[0])
	}
	compactedMessages := newManager.CloneMessageList()
	if got := compactedMessages[2].Content; !strings.Contains(got, "historical work summary") {
		t.Fatalf("compacted summary = %q", got)
	}
	if got := compactedMessages[5].ToolResults[0].Content; got != "current result" {
		t.Fatalf("current tool result = %q, want unchanged", got)
	}
	for _, message := range compactedMessages {
		if message.ResponsesResponseID != "" {
			t.Fatalf("compacted message retained provider response ID: %#v", message)
		}
	}
	retainedToolCall := compactedMessages[4]
	if len(retainedToolCall.ResponsesReasoningItems) != 1 || len(retainedToolCall.ResponsesOutputItems) != 1 || retainedToolCall.ResponsesAssistantPhase != "commentary" {
		t.Fatalf("compacted retained message lost replay metadata: %#v", retainedToolCall)
	}
}

func TestCompactSkipsFullyProtectedConversation(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []messages.Message{
		{Role: messages.MessageRoleSystem, Content: strings.Repeat("system ", 200)},
		{
			Role: messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{{
				ID:        "old_call",
				Name:      "shell",
				Arguments: `{"command":"generate-report"}`,
			}},
		},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{
				ToolCallID: "old_call",
				Name:       "shell",
				Content:    strings.Repeat("large historical output ", 1_000),
				Meta:       &messages.ToolResultMeta{Summary: "report generated"},
			}},
		},
		{Role: messages.MessageRoleUser, Content: "current request"},
		{Role: messages.MessageRoleAssistant, Content: "current draft"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	model := &promptCapturingModel{reply: "summary should not be called"}
	compactor := NewCompactor(DefaultProtectRule, &testModel{Model: model})

	newManager, compacted, err := compactor.Compact(context.Background(), manager)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compacted || newManager != nil {
		t.Fatal("Compact() compacted a fully protected conversation")
	}
	if len(model.prompts) != 0 {
		t.Fatalf("summary model calls = %d, want 0 for fully protected window", len(model.prompts))
	}
}

func TestCompactLeavesProtectedOrphanedToolResultUnchanged(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []messages.Message{
		{Role: messages.MessageRoleSystem, Content: "system"},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{
				ToolCallID: "missing_call",
				Name:       "shell",
				Content:    strings.Repeat("orphaned historical output ", 1_000),
			}},
		},
		{Role: messages.MessageRoleUser, Content: "current request"},
		{Role: messages.MessageRoleAssistant, Content: "current response"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	compactor := NewCompactor(DefaultProtectRule, &testModel{})

	newManager, compacted, err := compactor.Compact(context.Background(), manager)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compacted || newManager != nil {
		t.Fatal("Compact() compacted a fully protected orphaned tool result")
	}
}

func TestCompactDropsDeletedRecoverableToolResultMode(t *testing.T) {
	sessionFolder := t.TempDir()
	completePath := filepath.Join(sessionFolder, "tr_complete.data")
	partialPath := filepath.Join(sessionFolder, "tr_partial.data")
	for _, path := range []string{completePath, partialPath} {
		writeTestArtifact(t, path, time.Now().Add(time.Hour))
	}
	manager, err := contextmanager.NewContextManagerFromMessageList(sessionFolder, []messages.Message{
		{Role: messages.MessageRoleSystem, Content: "system"},
		{Role: messages.MessageRoleUser, Content: "old request"},
		{
			Role: messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{
				{ID: "old_complete", Name: "shell", Arguments: `{"command":"go test ./..."}`},
				{ID: "old_partial", Name: "inspect_episode", Arguments: `{"episode_id":"ep_1"}`},
			},
		},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{
				{
					ToolCallID: "old_complete",
					Name:       "shell",
					Content:    strings.Repeat("complete historical output ", 1_000),
					Meta: &messages.ToolResultMeta{
						ArtifactPath:     completePath,
						ArtifactComplete: true,
						Summary:          "128 passed, 2 failed",
					},
				},
				{
					ToolCallID: "old_partial",
					Name:       "inspect_episode",
					Content:    strings.Repeat("partial historical output ", 1_000),
					Meta: &messages.ToolResultMeta{
						ArtifactPath:     partialPath,
						ArtifactComplete: false,
						Summary:          "episode ep_1 completed",
					},
				},
			},
		},
		{Role: messages.MessageRoleAssistant, Content: strings.Repeat("old answer ", 500)},
		{Role: messages.MessageRoleUser, Content: "current request"},
		{
			Role:      messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{{ID: "current_call", Name: "shell", Arguments: `{"command":"pwd"}`}},
		},
		{
			Role:        messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{ToolCallID: "current_call", Name: "shell", Content: "current result"}},
		},
		{
			Role:      messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{{ID: "current_call_2", Name: "shell", Arguments: `{"command":"grep -F recovery /tmp/tool-results/tr_current.data"}`}},
		},
		{
			Role:        messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{ToolCallID: "current_call_2", Name: "shell", Content: "current recovery result"}},
		},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	model := &promptCapturingModel{reply: "summary deliberately omits every artifact reference"}
	compactor := NewCompactor(DefaultProtectRule, &testModel{Model: model})

	newManager, compacted, err := compactor.Compact(context.Background(), manager)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !compacted || newManager == nil {
		t.Fatal("Compact() did not create a compacted manager")
	}
	if len(model.prompts) != 1 {
		t.Fatalf("summary model calls = %d, want 1", len(model.prompts))
	}

	messageList := newManager.CloneMessageList()
	combined := ""
	for _, message := range messageList {
		combined += message.Content
	}
	for _, unwanted := range []string{
		"## Recoverable Tool Results",
		completePath,
		partialPath,
	} {
		if strings.Contains(combined, unwanted) {
			t.Fatalf("compacted context retained deleted recovery data %q:\n%s", unwanted, combined)
		}
	}
	for _, message := range messageList {
		if len(message.RecoverableToolResults) != 0 {
			t.Fatalf("compacted context retained recovery metadata: %#v", message.RecoverableToolResults)
		}
	}
	currentContents := make([]string, 0, 2)
	currentUserPreserved := false
	for _, message := range messageList {
		if message.Role == messages.MessageRoleUser && message.Content == "current request" {
			currentUserPreserved = true
		}
		for _, result := range message.ToolResults {
			if result.ToolCallID == "current_call" || result.ToolCallID == "current_call_2" {
				currentContents = append(currentContents, result.Content)
			}
		}
	}
	if !currentUserPreserved || len(currentContents) != 2 || currentContents[0] != "current result" || currentContents[1] != "current recovery result" {
		t.Fatalf("current turn was not preserved: user=%v results=%#v messages=%#v", currentUserPreserved, currentContents, messageList)
	}
}

func messageListContains(messages []messages.Message, value string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, value) {
			return true
		}
		for _, result := range message.ToolResults {
			if strings.Contains(result.Content, value) {
				return true
			}
		}
	}
	return false
}

func writeTestArtifact(t *testing.T, dataPath string, expiresAt time.Time) {
	t.Helper()
	if err := os.WriteFile(dataPath, []byte("artifact"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", dataPath, err)
	}
	metadata, err := json.Marshal(contextmanager.ArtifactMetadata{
		MIMEType:  "text/plain",
		Size:      int64(len("artifact")),
		CreatedAt: time.Now().Add(-time.Hour),
		ExpiresAt: expiresAt,
		Complete:  true,
	})
	if err != nil {
		t.Fatalf("Marshal artifact metadata: %v", err)
	}
	metadataPath := strings.TrimSuffix(dataPath, ".data") + ".json"
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", metadataPath, err)
	}
}
