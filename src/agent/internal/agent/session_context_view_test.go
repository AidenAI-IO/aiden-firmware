package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/memory"
)

func TestBuildSessionContextViewUsesActiveSessionScope(t *testing.T) {
	events := []SessionEvent{
		{Type: "user_input", Role: "user", Content: "查天气"},
		{Type: "planner_decision", Role: string(RolePlanner), Objective: "查询天气", Plan: []string{"打开天气"}},
		{Type: "user_input", Role: "user", Content: "打开微信"},
		{Type: "assistant_output", Role: "assistant", Content: "已打开微信"},
		{Type: "user_input", Role: "user", Content: "然后给张三发消息"},
	}

	view := BuildSessionContextView(events, "然后给张三发消息")
	if view.RootUserRequest != "查天气" {
		t.Fatalf("root request = %q, want first active-session user input", view.RootUserRequest)
	}
	if view.LatestCommittedPlan == nil || view.LatestCommittedPlan.Objective != "查询天气" {
		t.Fatalf("latest committed plan should come from active session, got %#v", view.LatestCommittedPlan)
	}
}

func TestBuildSessionContextViewIgnoresHotWindowEventsWhenRuntimeEventsExist(t *testing.T) {
	events := []SessionEvent{
		{Type: "user_input", Role: "user", Content: "HOT_WINDOW_USER_ONLY_PLANNER"},
		{Type: "assistant_output", Role: "assistant", Content: "HOT_WINDOW_ASSISTANT_ONLY_PLANNER"},
		{Type: "user_input", Role: "user", Content: "打开微信", RunID: "run-1"},
		{Type: "planner_decision", Role: string(RolePlanner), Objective: "打开微信", Plan: []string{"打开微信"}, RunID: "run-1"},
		{Type: "user_input", Role: "user", Content: "继续当前任务", RunID: "run-2"},
	}

	view := BuildSessionContextView(events, "继续当前任务")
	if view.RootUserRequest != "打开微信" {
		t.Fatalf("root request = %q, want first runtime user input", view.RootUserRequest)
	}
	if view.LatestCommittedPlan == nil || view.LatestCommittedPlan.Objective != "打开微信" {
		t.Fatalf("latest committed plan should come from runtime events, got %#v", view.LatestCommittedPlan)
	}
}

func TestBuildSessionContextViewDoesNotLeakPreRootPlanWhenRootIsLatestUser(t *testing.T) {
	canFinish := true
	events := []SessionEvent{
		{Type: "planner_decision", Role: string(RolePlanner), Objective: "old objective", Plan: []string{"old step"}, RunID: "run-old"},
		{Type: "verifier_decision", Role: string(RoleVerifier), Reason: "old verifier", CanFinish: &canFinish, RunID: "run-old"},
		{Type: "user_input", Role: "user", Content: "new root request", RunID: "run-new"},
	}

	view := BuildSessionContextView(events, "new root request")
	if view.RootUserRequest != "new root request" {
		t.Fatalf("root request = %q, want latest user as root", view.RootUserRequest)
	}
	if view.LatestCommittedPlan != nil {
		t.Fatalf("latest committed plan should not leak from before root, got %#v", view.LatestCommittedPlan)
	}
	if view.LatestVerifierSummary != "" {
		t.Fatalf("latest verifier summary should not leak from before root, got %q", view.LatestVerifierSummary)
	}
}

func TestFormatSessionContextViewOmitsLegacyRelationJudgement(t *testing.T) {
	events := []SessionEvent{
		{Type: "user_input", Role: "user", Content: "打开微信"},
		{Type: "assistant_output", Role: "assistant", Content: "正在打开微信"},
		{Type: "user_input", Role: "user", Content: "我喜欢吃小龙虾"},
	}

	view := BuildSessionContextView(events, "我喜欢吃小龙虾")
	formatted := formatSessionContextView(view)
	for _, unwanted := range []string{
		"Follow-up classification:",
		"follow_up_relation",
		"continuation",
		"correction",
		"replacement",
		"new_task",
		"Latest correction:",
		"Interpretation:",
		"Context priority:",
		"Root request:",
		"continue the existing task using the root request as the authority",
		"treat the latest user message",
	} {
		if strings.Contains(formatted, unwanted) {
			t.Fatalf("formatted context should not contain %q:\n%s", unwanted, formatted)
		}
	}
}

func TestSessionContextPlannerMemoryDoesNotExposeLegacyRelationVariable(t *testing.T) {
	ctx := context.Background()
	manager := NewMemoryManager(t.TempDir())
	mem := newSessionContextPlannerMemory(memory.NewSimple(), manager, "default")

	for _, variable := range mem.MemoryVariables(ctx) {
		if variable == "follow_up_relation" {
			t.Fatalf("memory variables should not expose follow_up_relation: %#v", mem.MemoryVariables(ctx))
		}
	}

	values, err := mem.LoadMemoryVariables(ctx, map[string]any{"input": "然后给张三发消息"})
	if err != nil {
		t.Fatalf("LoadMemoryVariables() error = %v", err)
	}
	if _, ok := values["follow_up_relation"]; ok {
		t.Fatalf("memory values should not expose follow_up_relation: %#v", values)
	}
}
