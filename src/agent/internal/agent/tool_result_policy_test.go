package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
)

func TestToolResultPolicyKeepsSmallResultInline(t *testing.T) {
	policy := NewToolResultPolicy()
	prepared, err := policy.Prepare(context.Background(), ToolResultPrepareInput{
		Call: ToolCall{
			Spec:  ToolSpec{Name: "shell"},
			Input: `{"command":"pwd"}`,
		},
		Result:      ToolResult{Output: "/workspace\n"},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Content != "/workspace\n" {
		t.Fatalf("Prepare() content = %q, want raw output", prepared.Content)
	}
	if !prepared.Complete {
		t.Fatal("Prepare() complete = false, want true")
	}
	if prepared.ArtifactRef != "" {
		t.Fatalf("Prepare() artifact ref = %q, want empty", prepared.ArtifactRef)
	}
	if prepared.Reason != ToolResultReasonInline {
		t.Fatalf("Prepare() reason = %q, want %q", prepared.Reason, ToolResultReasonInline)
	}
}

func TestToolResultPolicyBoundsResultWhenCurrentContextIsFull(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []contextmanager.Message{
		{Role: contextmanager.MessageRoleSystem, Content: strings.Repeat("context ", 3_650)},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	output := strings.Repeat("result ", 400)
	if len(output) >= toolResultInlineMaxBytes || estimateTextTokens(output) >= toolResultInlineMaxTokens {
		t.Fatal("test setup output must be intrinsically small")
	}

	policy := NewToolResultPolicy()
	prepared, err := policy.Prepare(context.Background(), ToolResultPrepareInput{
		Call: ToolCall{
			Spec:  ToolSpec{Name: "recall_memory"},
			Input: `{"tags":["context"]}`,
		},
		Result:         ToolResult{Output: output},
		ContextManager: manager,
		ModelSpec:      model.ModelSpec{ContextWindow: 10_000, MaxOutput: 1_000},
		CallOptions:    []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Reason != ToolResultReasonContextLarge {
		t.Fatalf("Prepare() reason = %q, want %q", prepared.Reason, ToolResultReasonContextLarge)
	}
	if prepared.Complete {
		t.Fatal("Prepare() complete = true, want false")
	}
	if got := estimateTextTokens(prepared.Content); got >= estimateTextTokens(output) {
		t.Fatalf("Prepare() content tokens = %d, want less than raw result", got)
	}
}

func TestToolResultPolicyBoundsIntrinsicallyLargeResult(t *testing.T) {
	output := "HEAD_MARKER\n" + strings.Repeat("0123456789", 1_200) + "\nTAIL_MARKER"
	policy := NewToolResultPolicy()
	prepared, err := policy.Prepare(context.Background(), ToolResultPrepareInput{
		Call: ToolCall{
			Spec:  ToolSpec{Name: "shell"},
			Input: `{"command":"pwd"}`,
		},
		Result:      ToolResult{Output: output},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Complete {
		t.Fatal("Prepare() complete = true, want false")
	}
	if prepared.Reason != ToolResultReasonIntrinsicLarge {
		t.Fatalf("Prepare() reason = %q, want %q", prepared.Reason, ToolResultReasonIntrinsicLarge)
	}
	for _, want := range []string{"[shell] pwd", "HEAD_MARKER", "TAIL_MARKER", "chars"} {
		if !strings.Contains(prepared.Content, want) {
			t.Fatalf("Prepare() content missing %q:\n%s", want, prepared.Content)
		}
	}
	if strings.Contains(prepared.Content, "tool result omitted") {
		t.Fatalf("Prepare() used opaque omission text: %s", prepared.Content)
	}
	if got := estimateTextTokens(prepared.Content); got > toolResultInlineMaxTokens {
		t.Fatalf("Prepare() tokens = %d, want <= %d", got, toolResultInlineMaxTokens)
	}
}

func TestToolResultPolicyShrinksIntrinsicResultToCurrentBudget(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []contextmanager.Message{
		{Role: contextmanager.MessageRoleSystem, Content: strings.Repeat("context ", 3_000)},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	output := strings.Repeat("large-result ", 1_000)
	input := ToolResultPrepareInput{
		Call:           ToolCall{Spec: ToolSpec{Name: "shell"}, Input: `{"command":"go test ./..."}`},
		Result:         ToolResult{Output: output},
		ContextManager: manager,
		ModelSpec:      model.ModelSpec{ContextWindow: 10_000, MaxOutput: 1_000},
		CallOptions:    []llms.CallOption{llms.WithMaxTokens(1_000)},
	}
	available := availableToolResultTokens(input)
	if available <= toolResultMinimumObservation || available >= toolResultPreviewTargetToken {
		t.Fatalf("test setup available tokens = %d, want constrained positive budget", available)
	}
	prepared, err := NewToolResultPolicy().Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got := estimateTextTokens(prepared.Content); got > available {
		t.Fatalf("Prepare() content tokens = %d, want <= available %d", got, available)
	}
	if prepared.ArtifactRef == "" || !strings.Contains(prepared.Content, prepared.ArtifactRef) {
		t.Fatalf("Prepare() content lost recovery ref %q:\n%s", prepared.ArtifactRef, prepared.Content)
	}
}

func TestToolResultPolicyDoesNotRecursivelyArtifactizeArtifactRead(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	output := `{"content":` + mustMarshalJSONString(t, strings.Repeat("detail ", 1_500)) + `,"offset":0,"next_offset":10500,"complete":false}`
	prepared, err := NewToolResultPolicy().Prepare(context.Background(), ToolResultPrepareInput{
		Call:           ToolCall{Spec: ToolSpec{Name: "artifact_read"}, Input: `{"ref":"artifact://tr_example","limit":16384}`},
		Result:         ToolResult{Output: output},
		ContextManager: manager,
		ModelSpec:      model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions:    []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !prepared.Complete || prepared.Content != output {
		t.Fatalf("Prepare() complete=%v content bytes=%d, want full bounded recovery page", prepared.Complete, len(prepared.Content))
	}
	if prepared.ArtifactRef != "" {
		t.Fatalf("Prepare() artifact ref = %q, want no recursive artifact", prepared.ArtifactRef)
	}
}

func TestToolResultPolicyUsesClipboardProjection(t *testing.T) {
	clipboardText := "VISIBLE_PREFIX_" + strings.Repeat("secret-value-", 800) + "TAIL_SECRET"
	output := `{"ok":true,"text":` + mustMarshalJSONString(t, clipboardText) + `}`
	policy := NewToolResultPolicy()
	prepared, err := policy.Prepare(context.Background(), ToolResultPrepareInput{
		Call: ToolCall{
			Spec:  ToolSpec{Name: toolBridgeClipboard},
			Input: `{"action":"read"}`,
		},
		Result:      ToolResult{Output: output},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	for _, want := range []string{"[bridge_clipboard] read", `"ok":true`, `"text_chars":`, "VISIBLE_PREFIX_"} {
		if !strings.Contains(prepared.Content, want) {
			t.Fatalf("Prepare() content missing %q:\n%s", want, prepared.Content)
		}
	}
	if strings.Contains(prepared.Content, "TAIL_SECRET") {
		t.Fatalf("Prepare() leaked clipboard tail into active context: %s", prepared.Content)
	}
}

func TestToolResultPolicyPersistsLargeResultAsArtifact(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	output := "HEAD\n" + strings.Repeat("large-output-", 1_000) + "\nTAIL"
	policy := NewToolResultPolicy()
	prepared, err := policy.Prepare(context.Background(), ToolResultPrepareInput{
		Call: ToolCall{
			Spec:   ToolSpec{Name: "shell"},
			Action: schema.AgentAction{ToolID: "call_1", Tool: "shell"},
			Input:  `{"command":"go test ./..."}`,
		},
		Result:         ToolResult{Output: output},
		ContextManager: manager,
		ModelSpec:      model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions:    []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.ArtifactRef == "" || !prepared.ArtifactComplete {
		t.Fatalf("Prepare() artifact = %q complete=%v", prepared.ArtifactRef, prepared.ArtifactComplete)
	}
	if !strings.Contains(prepared.Content, "Full result: "+prepared.ArtifactRef) {
		t.Fatalf("Prepare() content missing artifact ref: %s", prepared.Content)
	}
	chunk, err := manager.ReadArtifact(prepared.ArtifactRef, 0, contextmanager.ArtifactReadMaxBytes)
	if err != nil {
		t.Fatalf("ReadArtifact() error = %v", err)
	}
	if string(chunk.Content) != output || !chunk.Complete {
		t.Fatalf("ReadArtifact() complete=%v bytes=%d, want full %d bytes", chunk.Complete, len(chunk.Content), len(output))
	}
}

func TestToolResultPolicyPreservesStructuredStatusOutsideHeadTail(t *testing.T) {
	output := `{"output":"` + strings.Repeat("A", 6_000) + `","exit_code":7,"exit_error":"tests failed","padding":"` + strings.Repeat("Z", 6_000) + `"}`
	prepared, err := NewToolResultPolicy().Prepare(context.Background(), ToolResultPrepareInput{
		Call:        ToolCall{Spec: ToolSpec{Name: "shell"}, Input: `{"action":"poll","session_id":"sh_1"}`},
		Result:      ToolResult{Output: output},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	for _, want := range []string{`"exit_code":7`, `"exit_error":"tests failed"`} {
		if !strings.Contains(prepared.Content, want) {
			t.Fatalf("Prepare() content missing %q:\n%s", want, prepared.Content)
		}
	}
}

func TestToolResultPolicyPreservesShellStderrTail(t *testing.T) {
	output := "Error: exit status 1\nStderr:\nFAIL TestPhoneBridge\n" + strings.Repeat("stderr-detail ", 600) + "STDERR_TAIL\nStdout:\n" + strings.Repeat("stdout-detail ", 600)
	prepared, err := NewToolResultPolicy().Prepare(context.Background(), ToolResultPrepareInput{
		Call:        ToolCall{Spec: ToolSpec{Name: "shell"}, Input: `{"command":"go test ./..."}`},
		Result:      ToolResult{Output: output},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	for _, want := range []string{"Error: exit status 1", "FAIL TestPhoneBridge", "STDERR_TAIL", "Stderr:", "Stdout:"} {
		if !strings.Contains(prepared.Content, want) {
			t.Fatalf("Prepare() content missing %q:\n%s", want, prepared.Content)
		}
	}
}

func TestToolResultPolicyProjectsCollectionCountAndTopK(t *testing.T) {
	results := make([]map[string]any, 100)
	for i := range results {
		results[i] = map[string]any{
			"id":      fmt.Sprintf("memory_%03d", i+1),
			"title":   fmt.Sprintf("Memory %03d", i+1),
			"content": strings.Repeat("detail ", 20),
		}
	}
	data, err := json.Marshal(map[string]any{
		"results":  results,
		"has_more": true,
		"cursor":   "cursor_2",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	prepared, err := NewToolResultPolicy().Prepare(context.Background(), ToolResultPrepareInput{
		Call:        ToolCall{Spec: ToolSpec{Name: "recall_memory"}, Input: `{"tags":["test"]}`},
		Result:      ToolResult{Output: string(data)},
		ModelSpec:   model.ModelSpec{ContextWindow: 32_000, MaxOutput: 1_000},
		CallOptions: []llms.CallOption{llms.WithMaxTokens(1_000)},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	for _, want := range []string{`"results_total":100`, `"has_more":true`, `"cursor":"cursor_2"`, `"id":"memory_001"`} {
		if !strings.Contains(prepared.Content, want) {
			t.Fatalf("Prepare() content missing %q:\n%s", want, prepared.Content)
		}
	}
	if strings.Contains(prepared.Content, "memory_100") {
		t.Fatalf("Prepare() included collection tail instead of Top-K: %s", prepared.Content)
	}
}

func mustMarshalJSONString(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(data)
}
