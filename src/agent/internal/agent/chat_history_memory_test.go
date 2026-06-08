package agent

import (
	"context"
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
