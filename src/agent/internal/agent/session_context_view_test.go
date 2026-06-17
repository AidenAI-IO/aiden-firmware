package agent

import "testing"

func TestBuildSessionContextViewUsesLatestTaskRootForContinuation(t *testing.T) {
	events := []SessionEvent{
		{Type: "user_input", Role: "user", Content: "查天气", Relation: FollowUpRootRequest},
		{Type: "planner_decision", Role: string(RolePlanner), Objective: "查询天气", Plan: []string{"打开天气"}},
		{Type: "user_input", Role: "user", Content: "打开微信", Relation: FollowUpNewTask},
		{Type: "assistant_output", Role: "assistant", Content: "已打开微信"},
		{Type: "user_input", Role: "user", Content: "然后给张三发消息", Relation: FollowUpContinuation},
	}

	view := BuildSessionContextView(events, "然后给张三发消息")
	if view.RootUserRequest != "打开微信" {
		t.Fatalf("root request = %q, want latest new task root", view.RootUserRequest)
	}
	if view.LatestCommittedPlan != nil {
		t.Fatalf("latest committed plan should not cross task root, got %#v", view.LatestCommittedPlan)
	}
}
