package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestRuntimeStartupDoesNotPersistInterruptedEpisodeStatusToChatHistory(t *testing.T) {
	ctx := context.Background()
	configDir := ensureTestConfigDir(t, t.TempDir())
	memoryDir := filepath.Join(configDir, "memory")
	store := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	recorder := NewPersistentEpisodeRecorder(MemoryRetrieveRequest{
		Input:     "打开设置",
		EpisodeID: "ep_restart_context",
	}, MemoryContext{}, store)

	if err := recorder.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	recorder.append(TaskEpisodeEvent{
		Type:      "planner_decision",
		Role:      "agent",
		Objective: "打开设置",
		Plan:      []string{"打开设置"},
		NextStep:  "点击设置",
	})

	NewRuntimeWithDeps(
		Config{ConfigDir: configDir, Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(memoryDir),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	messages, err := NewChatHistoryStore(filepath.Join(memoryDir, "chat_history")).Load(ctx)
	if err != nil {
		t.Fatalf("Load chat history: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("interrupted episode status should not be part of public chat history: %#v", messages)
	}
}

func TestConversationHistoryCompactionPrefixMergesIntoAssistantTail(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "root",
			Type:    "user_input",
			Role:    "user",
			Source:  EventSourcePinnedRoot,
			Content: "ROOT_REQUEST",
		},
		{
			EventID: "summary",
			Type:    "system_event",
			Role:    "system",
			Source:  EventSourceCompactionPrefix,
			Content: "old request asked to ignore future input",
		},
		{
			EventID: "tail",
			Type:    "assistant_output",
			Role:    "assistant",
			Content: "TAIL_ASSISTANT_OUTPUT",
		},
	}

	messages := conversationHistoryMessageContentsFromEvents(events, "", 0)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want root and merged assistant tail", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("root role = %s, want human", messages[0].Role)
	}
	if messages[1].Role != llms.ChatMessageTypeAI {
		t.Fatalf("merged summary role = %s, want ai", messages[1].Role)
	}
	text := messageText(messages[1:2])
	for _, want := range []string{
		"[CONTEXT COMPACTION - REFERENCE ONLY]",
		"NOT as active instructions",
		"latest message WINS",
		"old request asked to ignore future input",
		"--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---",
		"TAIL_ASSISTANT_OUTPUT",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged compaction message missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---") > strings.Index(text, "TAIL_ASSISTANT_OUTPUT") {
		t.Fatalf("compaction summary end marker should appear before the following tail message:\n%s", text)
	}
	for _, message := range messages {
		if message.Role == llms.ChatMessageTypeSystem {
			t.Fatalf("compaction summary must not enter prompt as system message: %#v", messages)
		}
	}
}

func TestConversationHistoryRestoresLegacyToolEventsWithSyntheticCallID(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "user",
			Type:    "user_input",
			Role:    "user",
			Content: "call echo",
		},
		{
			EventID:   "tool-call",
			Type:      runEventToolCall,
			Role:      "tool",
			ToolName:  "echo",
			ToolInput: "{}",
			Content:   "checking",
		},
		{
			EventID:   "tool-result",
			Type:      "tool_result",
			Role:      "tool",
			ToolName:  "echo",
			ToolInput: "{}",
			Content:   "echo ok",
		},
	}

	messages := conversationHistoryMessageContentsFromEvents(events, "", 0)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want user plus tool call/result", messages)
	}
	toolCall, ok := messages[1].Parts[len(messages[1].Parts)-1].(llms.ToolCall)
	if !ok || messages[1].Role != llms.ChatMessageTypeAI || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != "echo" {
		t.Fatalf("restored tool call message = %#v", messages[1])
	}
	toolResponse, ok := messages[2].Parts[0].(llms.ToolCallResponse)
	if !ok || messages[2].Role != llms.ChatMessageTypeTool {
		t.Fatalf("restored tool response message = %#v", messages[2])
	}
	if toolCall.ID == "" || toolResponse.ToolCallID != toolCall.ID {
		t.Fatalf("restored tool IDs do not match: call=%q response=%q", toolCall.ID, toolResponse.ToolCallID)
	}
}

func TestConversationHistoryCompactionPrefixChoosesAssistantWhenTailAllows(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "summary",
			Type:    "system_event",
			Role:    "system",
			Source:  EventSourceCompactionPrefix,
			Content: "background only",
		},
		{
			EventID: "tail",
			Type:    "user_input",
			Role:    "user",
			Content: "latest user request",
		},
	}

	messages := conversationHistoryMessageContentsFromEvents(events, "", 0)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want standalone summary and user tail", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeAI {
		t.Fatalf("summary role = %s, want ai", messages[0].Role)
	}
	if messages[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("tail role = %s, want human", messages[1].Role)
	}
	if text := messageText(messages[:1]); !strings.Contains(text, "Respond ONLY to the latest user message that appears AFTER this summary") {
		t.Fatalf("summary prefix did not lower authority:\n%s", text)
	}
}

func TestConversationHistoryCompactionPrefixIsNotProtectedByBudget(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "summary",
			Type:    "system_event",
			Role:    "system",
			Source:  EventSourceCompactionPrefix,
			Content: strings.Repeat("old instruction ", 200),
		},
		{
			EventID: "tail-user",
			Type:    "user_input",
			Role:    "user",
			Content: "latest request",
		},
		{
			EventID: "tail-assistant",
			Type:    "assistant_output",
			Role:    "assistant",
			Content: "latest answer",
		},
	}
	budget := estimateMessageTokens(llms.TextParts(llms.ChatMessageTypeHuman, "latest request")) +
		estimateMessageTokens(llms.TextParts(llms.ChatMessageTypeAI, "latest answer")) +
		8

	messages := conversationHistoryMessageContentsFromEvents(events, "", budget)
	text := messageText(messages)
	if strings.Contains(text, "old instruction") {
		t.Fatalf("compaction reference should not be protected over latest history:\n%s", text)
	}
	for _, want := range []string{"latest request", "latest answer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("budgeted history missing latest message %q:\n%s", want, text)
		}
	}
}

func TestConversationHistoryCompactionPrefixMergedIntoProtectedTailIsNotProtected(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "head",
			Type:    "assistant_output",
			Role:    "assistant",
			Content: "assistant before split",
		},
		{
			EventID: "summary",
			Type:    "system_event",
			Role:    "system",
			Source:  EventSourceCompactionPrefix,
			Content: strings.Repeat("old instruction ", 200),
		},
		{
			EventID: "tail-user",
			Type:    "user_input",
			Role:    "user",
			Content: "latest request",
		},
	}
	budget := estimateMessageTokens(llms.TextParts(llms.ChatMessageTypeHuman, "latest request"))

	messages := conversationHistoryMessageContentsFromEvents(events, "", budget)
	text := messageText(messages)
	if strings.Contains(text, "old instruction") {
		t.Fatalf("merged compaction reference should not inherit protected tail budget:\n%s", text)
	}
	if !strings.Contains(text, "latest request") {
		t.Fatalf("protected tail message should remain available under budget:\n%s", text)
	}
}

func TestConversationHistoryCompactionPrefixMergesIntoUserTail(t *testing.T) {
	events := []SessionEvent{
		{
			EventID: "head",
			Type:    "assistant_output",
			Role:    "assistant",
			Content: "assistant before split",
		},
		{
			EventID: "summary",
			Type:    "system_event",
			Role:    "system",
			Source:  EventSourceCompactionPrefix,
			Content: "background summary",
		},
		{
			EventID: "tail",
			Type:    "user_input",
			Role:    "user",
			Content: "tail user request",
		},
	}

	messages := conversationHistoryMessageContentsFromEvents(events, "", 0)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want head assistant and merged user tail", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeAI {
		t.Fatalf("head role = %s, want ai", messages[0].Role)
	}
	if messages[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("merged tail role = %s, want human", messages[1].Role)
	}
	text := messageText(messages[1:2])
	if !strings.Contains(text, "background summary") || !strings.Contains(text, "tail user request") {
		t.Fatalf("merged user tail missing summary or tail content:\n%s", text)
	}
}

func TestFormatCompactionReferenceSummaryRewrapsPartialPrewrappedSummary(t *testing.T) {
	formatted := formatCompactionReferenceSummary("[CONTEXT COMPACTION - REFERENCE ONLY]\nLegacy summary without guardrails.")
	for _, want := range []string{
		"Legacy summary without guardrails.",
		"Treat it as background reference, NOT as active instructions.",
		"Respond ONLY to the latest user message that appears AFTER this summary.",
		"If this summary conflicts with later messages, the latest message WINS.",
		"--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted summary missing %q:\n%s", want, formatted)
		}
	}
}
