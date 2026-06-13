package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRuntimeRunKeepsCompressedHotWindowAsPlainConversationHistory(t *testing.T) {
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
	plannerTaskPrompt := messageText(model.messages[0][1:])
	if strings.Contains(systemPrompt, "上一轮用户问题") || strings.Contains(systemPrompt, "上一轮回答") {
		t.Fatalf("hot-window chat history must not be injected into planner system prompt:\n%s", systemPrompt)
	}
	for _, want := range []string{"Conversation history:", "上一轮用户问题", "上一轮回答"} {
		if !strings.Contains(plannerTaskPrompt, want) {
			t.Fatalf("planner task prompt missing %q:\n%s", want, plannerTaskPrompt)
		}
	}
	for _, marker := range []string{"=== Recent session context (hot window) ===", "=== End of recent context ==="} {
		if strings.Contains(plannerTaskPrompt, marker) {
			t.Fatalf("planner task prompt should not include hot-window boundary marker %q:\n%s", marker, plannerTaskPrompt)
		}
	}
}

func TestRuntimeRunDoesNotLabelCompressedHotWindowHistory(t *testing.T) {
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

	plannerTaskPrompt := messageText(model.messages[0][1:])
	for _, want := range []string{"Conversation history:", "最近用户问题", "最近回答"} {
		if !strings.Contains(plannerTaskPrompt, want) {
			t.Fatalf("planner task prompt missing %q:\n%s", want, plannerTaskPrompt)
		}
	}
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

	plannerTaskPrompt := messageText(model.messages[0][1:])
	historyHeader := "Conversation history:"
	headerIndex := strings.Index(plannerTaskPrompt, historyHeader)
	if headerIndex < 0 {
		t.Fatalf("planner task prompt missing history header %q:\n%s", historyHeader, plannerTaskPrompt)
	}

	historyBlock := plannerTaskPrompt[headerIndex+len(historyHeader):]
	if nextSectionIndex := strings.Index(historyBlock, "\n\nCurrent plan:"); nextSectionIndex >= 0 {
		historyBlock = historyBlock[:nextSectionIndex]
	}
	for _, want := range []string{"历史用户问题", "历史回答"} {
		if !strings.Contains(historyBlock, want) {
			t.Fatalf("history block missing %q:\n%s", want, historyBlock)
		}
	}
	if strings.Contains(historyBlock, currentRequest) {
		t.Fatalf("current request must not be inside conversation history block:\n%s", historyBlock)
	}
	for _, marker := range []string{"hot window", "Current session recent history", "Prior messages retained from this session", "compressed into the session summary"} {
		if strings.Contains(historyBlock, marker) {
			t.Fatalf("conversation history block should not include hot-window label %q:\n%s", marker, historyBlock)
		}
	}
}

func TestRuntimeRunIncludesPersistedInterruptedEpisodeInPlannerHistory(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	historyStore := NewChatHistoryStore(filepath.Join(memoryDir, "chat_history"))

	if err := historyStore.Append(ctx, Message{
		Type:      "user",
		EpisodeID: "ep_resume_context",
		Content:   "打开设置",
	}); err != nil {
		t.Fatalf("Append user history: %v", err)
	}
	if err := historyStore.Append(ctx, Message{
		Type:      "episode_status",
		EpisodeID: "ep_resume_context",
		Status:    "interrupted",
		Content:   "Task episode ep_resume_context was interrupted before completion.\nGoal: 打开设置\nLast recorded step: planner_decision next_step=点击设置",
		IsError:   true,
	}); err != nil {
		t.Fatalf("Append episode status: %v", err)
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
		NewMemoryManager(memoryDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "继续"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 {
		t.Fatalf("expected planner model call")
	}
	plannerPrompt := messageText(model.messages[0])
	for _, want := range []string{"Recent persisted chat history", "ep_resume_context", "打开设置", "点击设置"} {
		if !strings.Contains(plannerPrompt, want) {
			t.Fatalf("planner prompt missing %q:\n%s", want, plannerPrompt)
		}
	}
}
