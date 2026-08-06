package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestAppendToolExecutionMessagesPersistsPreparedContentAndMetadata(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	llmExecutor := executor.NewLLMExecutor(nil, manager)
	prepared := PreparedToolResult{
		Content:          "[shell] go test ./...\npartial",
		ArtifactPath:     "/tmp/tool-results/tr_example",
		OriginalBytes:    100_000,
		OriginalChars:    100_000,
		EstimatedTokens:  25_000,
		Complete:         false,
		ArtifactComplete: true,
		Reason:           ToolResultReasonIntrinsicLarge,
		Summary:          "exit_code=1",
	}
	step := schema.AgentStep{
		Action:      schema.AgentAction{ToolID: "call_1", Tool: "shell"},
		Observation: "RAW_OUTPUT_MUST_NOT_BE_STORED",
	}
	if err := appendToolExecutionMessages(llmExecutor, nil, step, prepared); err != nil {
		t.Fatalf("appendToolExecutionMessages() error = %v", err)
	}
	stored := manager.CloneMessageList()[0].ToolResults[0]
	if stored.Content != prepared.Content {
		t.Fatalf("stored content = %q, want prepared content", stored.Content)
	}
	if stored.Meta == nil || stored.Meta.ArtifactPath != prepared.ArtifactPath || stored.Meta.Summary != prepared.Summary {
		t.Fatalf("stored metadata = %#v", stored.Meta)
	}
}

func TestAgentLoopHardGuardRejectsBeforeProviderCall(t *testing.T) {
	inner := &scriptedModel{responses: []*llms.ContentResponse{contentResponse("must not be returned")}}
	model := &largeContextScriptedModel{scriptedModel: inner}
	manager, err := freshNewContextManager(strings.Repeat("system ", 20_000), "continue", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{},
		nil,
		2,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	_, err = loop.Run(context.Background(), "continue")
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("Run() error = %T %v, want ContextBudgetExceededError", err, err)
	}
	if len(inner.messages) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(inner.messages))
	}
}

type largeContextScriptedModel struct {
	*scriptedModel
}

func (m *largeContextScriptedModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: "fake", Name: "large-context", ContextWindow: 32_000, MaxOutput: 1_000}
}

type unknownContextScriptedModel struct {
	*scriptedModel
}

func (m *unknownContextScriptedModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: "openai", Name: "qwen3.6-35b"}
}

func TestAgentLoopUsesRuntimeFallbackWindowForCurrentToolResultGuard(t *testing.T) {
	rawOutput := strings.Repeat("0123456789", 736)
	if len(rawOutput) >= toolResultInlineMaxBytes || estimateTextTokens(rawOutput) >= toolResultInlineMaxTokens {
		t.Fatal("test setup output must be intrinsically small")
	}
	inner := &unknownContextScriptedModel{scriptedModel: &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call_1", "shell", `{"command":"produce-current-result"}`),
		contentResponse("Done"),
	}}}
	tracked := &usageTrackingModel{
		inner:   inner,
		metrics: &RunMetrics{ContextWindow: 10_000},
	}
	manager, err := freshNewContextManager(strings.Repeat("context ", 2_500), "run the tool", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		tracked,
		RoleProfile{Tools: []langtools.Tool{&staticTool{name: "shell", output: rawOutput}}},
		nil,
		4,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	answer, err := loop.Run(context.Background(), "run the tool", chains.WithMaxTokens(1_000))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "Done" {
		t.Fatalf("Run() answer = %q, want Done", answer)
	}

	var stored messages.ToolResult
	for _, message := range manager.CloneMessageList() {
		if message.Role == messages.MessageRoleToolResult && len(message.ToolResults) > 0 {
			stored = message.ToolResults[0]
			break
		}
	}
	if stored.Meta == nil {
		t.Fatal("stored ToolResult metadata is nil")
	}
	if stored.Meta.Reason != ToolResultReasonContextLarge || stored.Meta.ArtifactPath == "" {
		t.Fatalf("stored ToolResult = %#v, want context_large artifact", stored)
	}
	if stored.Content == rawOutput {
		t.Fatal("current tool result stayed inline despite the runtime fallback context budget")
	}
}

func TestAgentLoopStoresLargeToolResultAsBoundedArtifact(t *testing.T) {
	rawOutput := "HEAD\n" + strings.Repeat("RAW_PAYLOAD_", 1_000) + "\nTAIL"
	inner := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call_1", "shell", `{"command":"go test ./..."}`),
		contentResponse("Done"),
	}}
	model := &largeContextScriptedModel{scriptedModel: inner}
	manager, err := freshNewContextManager("system", "run tests", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	recorder := &EpisodeRecorder{}
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&staticTool{name: "shell", output: rawOutput}}},
		nil,
		4,
		nil,
		recorder,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	answer, err := loop.Run(context.Background(), "run tests")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "Done" {
		t.Fatalf("Run() answer = %q", answer)
	}

	var stored messages.ToolResult
	for _, message := range manager.CloneMessageList() {
		if message.Role == messages.MessageRoleToolResult && len(message.ToolResults) > 0 {
			stored = message.ToolResults[0]
			break
		}
	}
	if stored.Meta == nil || stored.Meta.ArtifactPath == "" || stored.Meta.Complete {
		t.Fatalf("stored ToolResult = %#v", stored)
	}
	if stored.Content == rawOutput || len(stored.Content) >= len(rawOutput) {
		t.Fatalf("stored content was not bounded: %d bytes", len(stored.Content))
	}
	data, err := os.ReadFile(stored.Meta.ArtifactPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != rawOutput {
		t.Fatalf("artifact content length = %d, want %d", len(data), len(rawOutput))
	}
	if len(inner.messages) < 2 {
		t.Fatalf("model calls = %d, want at least 2", len(inner.messages))
	}
	responses := toolResponseContents(t, inner.messages[1])
	if len(responses) != 1 || responses[0] != stored.Content {
		t.Fatalf("next model tool result = %#v, want bounded stored content", responses)
	}
	var telemetry *TaskEpisodeEvent
	for i := range recorder.events {
		if recorder.events[i].Type == runEventToolResultContext {
			telemetry = &recorder.events[i]
			break
		}
	}
	if telemetry == nil {
		t.Fatalf("tool result telemetry event missing: %#v", recorder.events)
	}
	for _, key := range []string{
		"original_bytes", "original_chars", "estimated_tokens", "context_bytes", "context_tokens",
		"processing_reason", "artifactized", "artifact_complete", "artifact_store_error", "processing_duration_ms",
	} {
		if _, ok := telemetry.Metadata[key]; !ok {
			t.Fatalf("tool result telemetry missing %q: %#v", key, telemetry.Metadata)
		}
	}
}

func TestAgentLoopDoesNotRepeatCompletedActionWhenArtifactPersistenceFails(t *testing.T) {
	tool := &countingLargeResultTool{
		name:   "side_effect_tool",
		output: strings.Repeat("x", contextmanager.ArtifactSingleMaxBytes+1),
	}
	inner := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call_1", tool.name, `{}`),
		contentResponse("Done without retry"),
	}}
	model := &largeContextScriptedModel{scriptedModel: inner}
	manager, err := freshNewContextManager("system", "perform action once", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{tool}},
		nil,
		4,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	answer, err := loop.Run(context.Background(), "perform action once")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "Done without retry" || tool.calls != 1 {
		t.Fatalf("answer=%q tool calls=%d, want one completed action", answer, tool.calls)
	}

	var stored messages.ToolResult
	for _, message := range manager.CloneMessageList() {
		if message.Role == messages.MessageRoleToolResult && len(message.ToolResults) > 0 {
			stored = message.ToolResults[0]
			break
		}
	}
	if stored.Meta == nil || stored.Meta.ProcessingErrorCode != "tool_result_persistence_failed" || !stored.Meta.ActionCompleted || stored.Meta.ObservationComplete {
		t.Fatalf("stored persistence failure metadata = %#v", stored.Meta)
	}
	if !strings.Contains(stored.Content, `"action_completed":true`) {
		t.Fatalf("stored content missing completed-action state: %s", stored.Content)
	}
}

func TestAgentLoopPersistsBoundedToolResultWhenPolicyFails(t *testing.T) {
	tool := &countingLargeResultTool{name: "side_effect_tool", output: strings.Repeat("raw-secret-", 2_000)}
	inner := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call_1", tool.name, `{}`),
		contentResponse("Done after fallback"),
	}}
	model := &largeContextScriptedModel{scriptedModel: inner}
	manager, err := freshNewContextManager("system", "perform action once", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{tool}},
		nil,
		4,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.ToolResultPolicy = failingToolResultPolicy{err: errors.New("policy unavailable")}

	answer, err := loop.Run(context.Background(), "perform action once")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "Done after fallback" || tool.calls != 1 {
		t.Fatalf("answer=%q tool calls=%d, want one completed action", answer, tool.calls)
	}

	var stored messages.ToolResult
	for _, message := range manager.CloneMessageList() {
		if message.Role == messages.MessageRoleToolResult && len(message.ToolResults) > 0 {
			stored = message.ToolResults[0]
			break
		}
	}
	if stored.ToolCallID != "call_1" || stored.Meta == nil || stored.Meta.ProcessingErrorCode != "tool_result_processing_failed" {
		t.Fatalf("stored fallback result = %#v", stored)
	}
	if strings.Contains(stored.Content, "raw-secret-") || estimateTextTokens(stored.Content) > toolResultMinimumObservation {
		t.Fatalf("stored fallback was not bounded: %q", stored.Content)
	}
}

type failingToolResultPolicy struct {
	err error
}

func (p failingToolResultPolicy) Prepare(context.Context, ToolResultPrepareInput) (PreparedToolResult, error) {
	return PreparedToolResult{}, p.err
}

type countingLargeResultTool struct {
	name   string
	output string
	calls  int
}

func (t *countingLargeResultTool) Name() string { return t.name }

func (t *countingLargeResultTool) Description() string {
	return "Returns one large result after completing an action."
}

func (t *countingLargeResultTool) Call(context.Context, string) (string, error) {
	t.calls++
	return t.output, nil
}
