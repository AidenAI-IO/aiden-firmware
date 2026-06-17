package agent

import (
	"context"
	"fmt"
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
		"Root request: prior user 00",
	} {
		if !strings.Contains(plannerTaskPrompt, want) {
			t.Fatalf("planner task prompt missing %q:\n%s", want, plannerTaskPrompt)
		}
	}
}
