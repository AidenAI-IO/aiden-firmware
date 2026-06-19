package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestRuntimeStartupPersistsInterruptedEpisodeStatusToChatHistory(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	store := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	recorder := NewPersistentEpisodeRecorder(MemoryRetrieveRequest{
		Input:     "打开设置",
		EpisodeID: "ep_restart_context",
	}, MemoryContext{}, store)

	if err := recorder.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	recorder.RecordPlannerDecision(plannerDecision{
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
	status, ok := firstMessageOfType(messages, "episode_status")
	if !ok {
		t.Fatalf("missing episode_status in chat history: %#v", messages)
	}
	if status.EpisodeID != "ep_restart_context" || status.Status != "interrupted" {
		t.Fatalf("unexpected episode status message: %#v", status)
	}
	for _, want := range []string{"打开设置", "点击设置", "agent restarted before the task episode completed"} {
		if !strings.Contains(status.Content, want) {
			t.Fatalf("episode status missing %q:\n%s", want, status.Content)
		}
	}
}

func TestRuntimeRunKeepsCompressedHotWindowAsChatMessages(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)

	if err := manager.AppendExchange(ctx, "default", "上一轮用户问题", "上一轮回答"); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}
	sessionDir := filepath.Join(memoryDir, "session")
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("# Session Summary\nEarlier compressed context."), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("继续完成")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "继续上一轮"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 2 {
		t.Fatalf("expected planner system and user messages, got %#v", model.messages)
	}

	systemPrompt := messageText(model.messages[0][:1])
	if strings.Contains(systemPrompt, "上一轮用户问题") || strings.Contains(systemPrompt, "上一轮回答") {
		t.Fatalf("hot-window chat history must not be injected into planner system prompt:\n%s", systemPrompt)
	}
	messages := model.messages[0]
	if len(messages) < 4 {
		t.Fatalf("expected restored chat messages before planner task prompt, got %#v", messages)
	}
	if messages[1].Role != "human" || messageText(messages[1:2]) != "上一轮用户问题\n" {
		t.Fatalf("unexpected restored user history message: role=%s text=%q", messages[1].Role, messageText(messages[1:2]))
	}
	if messages[2].Role != "ai" || messageText(messages[2:3]) != "上一轮回答\n" {
		t.Fatalf("unexpected restored assistant history message: role=%s text=%q", messages[2].Role, messageText(messages[2:3]))
	}
	plannerTaskPrompt := messageText(messages[3:])
	if strings.Contains(plannerTaskPrompt, "Conversation history:") || strings.Contains(plannerTaskPrompt, "上一轮回答") {
		t.Fatalf("planner task prompt should not duplicate hot-window chat history:\n%s", plannerTaskPrompt)
	}
	for _, marker := range []string{"=== Recent session context (hot window) ===", "=== End of recent context ==="} {
		if strings.Contains(plannerTaskPrompt, marker) {
			t.Fatalf("planner task prompt should not include hot-window boundary marker %q:\n%s", marker, plannerTaskPrompt)
		}
	}
}

func TestRuntimeRunScopesHotWindowConversationHistoryToPlanner(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir, WithSessionBoundaryEnabled(false))

	const (
		historyUser      = "HOT_WINDOW_USER_ONLY_PLANNER"
		historyAssistant = "HOT_WINDOW_ASSISTANT_ONLY_PLANNER"
	)
	if err := manager.AppendExchange(ctx, "default", historyUser, historyAssistant); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}

	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"use echo"},
			toolCallResponse("call_1", "echo", `{"__arg1":"ok"}`),
			finishStepToolCall("used echo"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:   configDir,
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &stubTool{name: "echo", description: "Echo.", output: "ok"},
		}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "continue the current task with echo"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 5 {
		t.Fatalf("expected planner, executor, and verifier model calls, got %d", len(model.messages))
	}

	plannerPrompt := messageText(model.messages[0])
	for _, want := range []string{historyUser, historyAssistant} {
		if !strings.Contains(plannerPrompt, want) {
			t.Fatalf("planner prompt missing hot-window history %q:\n%s", want, plannerPrompt)
		}
	}

	for _, roleMessages := range []struct {
		name     string
		messages []llms.MessageContent
	}{
		{name: "executor", messages: model.messages[2]},
		{name: "verifier", messages: model.messages[4]},
	} {
		prompt := messageText(roleMessages.messages)
		for _, unexpected := range []string{historyUser, historyAssistant} {
			if strings.Contains(prompt, unexpected) {
				t.Fatalf("%s prompt should not receive hot-window history %q:\n%s", roleMessages.name, unexpected, prompt)
			}
		}
	}
}

func TestRuntimeRunKeepsHotWindowConversationHistoryForForceSimpleLoop(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir, WithSessionBoundaryEnabled(false))

	const (
		historyUser      = "SIMPLE_LOOP_HOT_WINDOW_USER"
		historyAssistant = "SIMPLE_LOOP_HOT_WINDOW_ASSISTANT"
	)
	if err := manager.AppendExchange(ctx, "default", historyUser, historyAssistant); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("done")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:       configDir,
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Answer directly.",
			ForceSimpleLoop: true,
			MaxIterations:   1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "continue"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) != 1 {
		t.Fatalf("force_simple_loop should use one planner-role model call, got %d", len(model.messages))
	}
	prompt := messageText(model.messages[0])
	for _, want := range []string{historyUser, historyAssistant} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("force_simple_loop prompt missing hot-window history %q:\n%s", want, prompt)
		}
	}
}

func TestRuntimeRunDoesNotLabelCompressedHotWindowChatMessages(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)

	if err := manager.AppendExchange(ctx, "default", "最近用户问题", "最近回答"); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}
	sessionDir := filepath.Join(memoryDir, "session")
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("# Session Summary\nEarlier compressed context."), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("继续完成")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "继续"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 2 {
		t.Fatalf("expected planner system and user messages, got %#v", model.messages)
	}

	messages := model.messages[0]
	if len(messages) < 4 {
		t.Fatalf("expected restored chat messages before planner task prompt, got %#v", messages)
	}
	if messages[1].Role != "human" || messageText(messages[1:2]) != "最近用户问题\n" {
		t.Fatalf("unexpected restored user history message: role=%s text=%q", messages[1].Role, messageText(messages[1:2]))
	}
	if messages[2].Role != "ai" || messageText(messages[2:3]) != "最近回答\n" {
		t.Fatalf("unexpected restored assistant history message: role=%s text=%q", messages[2].Role, messageText(messages[2:3]))
	}
	plannerTaskPrompt := messageText(messages[3:])
	for _, marker := range []string{"=== Recent session context (hot window) ===", "=== End of recent context ===", "Current session recent history", "Prior messages retained from this session", "compressed into the session summary"} {
		if strings.Contains(plannerTaskPrompt, marker) {
			t.Fatalf("planner task prompt should not include hot-window label or marker %q:\n%s", marker, plannerTaskPrompt)
		}
	}
}

func TestRuntimeRunKeepsCurrentRequestOutOfCompressedHistoryBlock(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)

	if err := manager.AppendExchange(ctx, "default", "历史用户问题", "历史回答"); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}
	sessionDir := filepath.Join(memoryDir, "session")
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("# Session Summary\nEarlier compressed context."), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("继续完成")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	currentRequest := "这是新的用户请求"
	if _, err := runtime.Run(ctx, RunRequest{Input: currentRequest}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 2 {
		t.Fatalf("expected planner system and user messages, got %#v", model.messages)
	}

	messages := model.messages[0]
	if len(messages) < 4 {
		t.Fatalf("expected restored chat messages before planner task prompt, got %#v", messages)
	}
	historyMessages := messages[1:3]
	if messageText(historyMessages[0:1]) != "历史用户问题\n" || messageText(historyMessages[1:2]) != "历史回答\n" {
		t.Fatalf("unexpected restored history messages: %#v", historyMessages)
	}
	if strings.Contains(messageText(historyMessages), currentRequest) {
		t.Fatalf("current request must not be inside restored conversation history messages:\n%s", messageText(historyMessages))
	}
	plannerTaskPrompt := messageText(messages[3:])
	for _, marker := range []string{"hot window", "Current session recent history", "Prior messages retained from this session", "compressed into the session summary"} {
		if strings.Contains(plannerTaskPrompt, marker) {
			t.Fatalf("planner task prompt should not include hot-window label %q:\n%s", marker, plannerTaskPrompt)
		}
	}
}

func TestRuntimeRunIncludesFullActivePlannerHistoryAndSessionRootContext(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)
	for i := 0; i < 12; i++ {
		if err := manager.AppendExchange(ctx, "default", fmt.Sprintf("prior user %02d", i), fmt.Sprintf("prior assistant %02d", i)); err != nil {
			t.Fatalf("AppendExchange(%d) error = %v", i, err)
		}
	}

	model := &scriptedModel{responses: roleDirectResponses("继续完成")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "继续"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 2 {
		t.Fatalf("expected planner system and user messages, got %#v", model.messages)
	}

	plannerTaskPrompt := messageText(model.messages[0][1:])
	for _, want := range []string{
		"prior user 00",
		"prior assistant 00",
		"prior user 02",
		"prior assistant 02",
		"prior user 11",
		"prior assistant 11",
	} {
		if !strings.Contains(plannerTaskPrompt, want) {
			t.Fatalf("planner task prompt missing %q:\n%s", want, plannerTaskPrompt)
		}
	}
	if strings.Contains(plannerTaskPrompt, "Root request:") {
		t.Fatalf("planner task prompt should not repeat root request in session context:\n%s", plannerTaskPrompt)
	}
}

func TestRuntimeRunBudgetsActivePlannerHistoryBeforeModelCall(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 800
	cfg.ReserveTokens = 200
	cfg.KeepRecentTokens = 220
	manager := NewMemoryManager(memoryDir, WithExtractionConfig(cfg), WithSessionBoundaryEnabled(false))

	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Source:  EventSourcePinnedRoot,
		Content: "PINNED_ROOT_REQUEST",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent(pinned root) error = %v", err)
	}
	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "system_event",
		Role:    "system",
		Source:  EventSourceCompactionPrefix,
		Content: "SPLIT_SYNTHETIC_CONTEXT",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent(synthetic context) error = %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := manager.AppendExchange(ctx, "default",
			fmt.Sprintf("DROP_OLD_USER_%02d %s", i, strings.Repeat("old ", 160)),
			fmt.Sprintf("DROP_OLD_ASSISTANT_%02d %s", i, strings.Repeat("old ", 160)),
		); err != nil {
			t.Fatalf("AppendExchange(old %d) error = %v", i, err)
		}
	}
	if err := manager.AppendExchange(ctx, "default",
		"KEEP_RECENT_USER "+strings.Repeat("recent ", 28),
		"KEEP_RECENT_ASSISTANT "+strings.Repeat("recent ", 28),
	); err != nil {
		t.Fatalf("AppendExchange(recent) error = %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("继续完成")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model, spec: ModelSpec{ContextWindow: 800}},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "CURRENT_REQUEST_MARKER 继续"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 3 {
		t.Fatalf("expected planner system, history, and state prompt messages, got %#v", model.messages)
	}

	plannerMessages := model.messages[0]
	historyText := messageText(plannerMessages[1 : len(plannerMessages)-1])
	for _, want := range []string{"PINNED_ROOT_REQUEST", "KEEP_RECENT_USER", "KEEP_RECENT_ASSISTANT"} {
		if !strings.Contains(historyText, want) {
			t.Fatalf("budgeted planner history missing %q:\n%s", want, historyText)
		}
	}
	for _, unwanted := range []string{"SPLIT_SYNTHETIC_CONTEXT", "DROP_OLD_USER_00", "DROP_OLD_ASSISTANT_00", "CURRENT_REQUEST_MARKER"} {
		if strings.Contains(historyText, unwanted) {
			t.Fatalf("budgeted planner history should not contain %q:\n%s", unwanted, historyText)
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := manager.WaitMaintenance(waitCtx); err != nil {
		t.Fatalf("WaitMaintenance() error = %v", err)
	}
	if !manager.HasCompressedHistory() {
		t.Fatal("over-budget active history should preserve a compression signal for maintenance")
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
