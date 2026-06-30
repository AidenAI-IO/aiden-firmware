package agent

import (
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

// TestParsePlannerDecisionExtractsDescriptionFromToolCall tests that when planner
// incorrectly returns a tool_call instead of JSON, we extract the description field
// as the next_step to maintain executor coordination.
func TestParsePlannerDecisionExtractsDescriptionFromToolCall(t *testing.T) {
	// Simulate planner incorrectly calling a tool instead of returning JSON
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: "",
				ToolCalls: []llms.ToolCall{
					{
						FunctionCall: &llms.FunctionCall{
							Name:      "touch_gesture",
							Arguments: `{"__arg1": "{\"type\": \"tap\", \"point\": {\"x\": 90, \"y\": 50}}", "description": "点击左上角返回箭头，回到上一级设置菜单"}`,
						},
					},
				},
			},
		},
	}

	decision := parsePlannerDecision(response, "查找5分钟设置")

	if decision.NextStep != "点击左上角返回箭头，回到上一级设置菜单" {
		t.Errorf("next_step = %q, want %q", decision.NextStep, "点击左上角返回箭头，回到上一级设置菜单")
	}

	if decision.Reason != "planner incorrectly returned tool_call instead of JSON; extracted description field as next_step" {
		t.Errorf("reason = %q, expected tool_call extraction reason", decision.Reason)
	}

	if len(decision.Plan) == 0 || decision.Plan[0] != "点击左上角返回箭头，回到上一级设置菜单" {
		t.Errorf("plan = %#v, should contain extracted description", decision.Plan)
	}
}

// TestParsePlannerDecisionHandlesLegacyFuncCall tests legacy FuncCall format
func TestParsePlannerDecisionHandlesLegacyFuncCall(t *testing.T) {
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: "",
				FuncCall: &llms.FunctionCall{
					Name:      "touch_gesture",
					Arguments: `{"description": "返回主页面"}`,
				},
			},
		},
	}

	decision := parsePlannerDecision(response, "fallback")

	if decision.NextStep != "返回主页面" {
		t.Errorf("next_step = %q, want %q", decision.NextStep, "返回主页面")
	}
}

func TestRoleResponseDebugTextDoesNotDuplicateStructuredToolCall(t *testing.T) {
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: `tool_call: current_time input={"timezone":"local"}`,
				ToolCalls: []llms.ToolCall{
					{
						FunctionCall: &llms.FunctionCall{
							Name:      "current_time",
							Arguments: `{"timezone":"local"}`,
						},
					},
				},
			},
		},
	}

	debug := roleResponseDebugText(response)

	if count := strings.Count(debug, "tool_call: current_time"); count != 1 {
		t.Fatalf("tool call debug count = %d, want 1; debug=%q", count, debug)
	}
}

// TestParsePlannerDecisionFallsBackOnEmptyResponse tests fallback behavior
func TestParsePlannerDecisionFallsBackOnEmptyResponse(t *testing.T) {
	response := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{
				Content: "",
			},
		},
	}

	decision := parsePlannerDecision(response, "original task")

	if decision.NextStep != "original task" {
		t.Errorf("next_step = %q, want fallback %q", decision.NextStep, "original task")
	}

	if decision.Objective != "original task" {
		t.Errorf("objective = %q, want fallback %q", decision.Objective, "original task")
	}
}

func TestParseVerifierDecisionRejectsMalformedText(t *testing.T) {
	decision := parseVerifierDecision("need more evidence", "candidate answer")

	if decision.CanFinish {
		t.Fatalf("malformed verifier text should not finish: %#v", decision)
	}
	if !decision.NeedsReplan {
		t.Fatalf("malformed verifier text should request replan: %#v", decision)
	}
}

func TestParseVerifierDecisionRejectsAnswerlessJSONFinish(t *testing.T) {
	decision := parseVerifierDecision(`{"can_finish":true,"reason":"looks good"}`, "candidate answer")

	if decision.CanFinish {
		t.Fatalf("answerless verifier JSON should not finish: %#v", decision)
	}
	if !decision.NeedsReplan {
		t.Fatalf("answerless verifier JSON should request replan: %#v", decision)
	}
}

func TestParseVerifierDecisionReplanOverridesFinish(t *testing.T) {
	decision := parseVerifierDecision(`{"can_finish":true,"needs_replan":true,"final_answer":"not done","reason":"step incomplete"}`, "")

	if decision.CanFinish {
		t.Fatalf("needs_replan should override can_finish: %#v", decision)
	}
	if !decision.NeedsReplan {
		t.Fatalf("needs_replan should remain true: %#v", decision)
	}
}

func TestParseVerifierDecisionAcceptsExplicitTextFinalAnswer(t *testing.T) {
	decision := parseVerifierDecision("Final Answer: done", "")

	if !decision.CanFinish {
		t.Fatalf("explicit final answer should finish: %#v", decision)
	}
	if decision.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want done", decision.FinalAnswer)
	}
}

func TestParseRouteDecisionTreatsBareSimpleIntentAsSimple(t *testing.T) {
	decision := parseRouteDecision(contentResponse("use_simple_mode\n<reason>no tools needed</reason>"), "Select option (b).")

	if decision.Mode != routeModeSimple {
		t.Fatalf("route mode = %q, want simple", decision.Mode)
	}
	if decision.FinalAnswer != "" {
		t.Fatalf("bare simple intent should not become final answer: %#v", decision)
	}
}

func TestParseRouteDecisionTreatsOrdinaryPlanAndSimpleTextAsDirectAnswer(t *testing.T) {
	for _, text := range []string{
		"I will provide a plan in the final answer.",
		"Let's keep this simple and answer directly.",
		"Here is a simple approach: choose option B.",
	} {
		decision := parseRouteDecision(contentResponse(text), "Select option (b).")
		if decision.Mode != routeModeDirectAnswer {
			t.Fatalf("route mode for %q = %q, want direct_answer", text, decision.Mode)
		}
		if decision.FinalAnswer != text {
			t.Fatalf("final answer for %q = %q", text, decision.FinalAnswer)
		}
	}
}

func TestParseRouteDecisionAcceptsStructuredFinalAnswerText(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"direct_answer","speech":"Short answer.","text":"Complete answer."}`),
		"Answer directly.",
	)

	if decision.Mode != routeModeDirectAnswer {
		t.Fatalf("route mode = %q, want direct_answer", decision.Mode)
	}
	if decision.FinalAnswer == "" {
		t.Fatal("structured final answer text should not be downgraded to simple mode")
	}
	if decision.FinalAnswer != "Complete answer." {
		t.Fatalf("final answer = %q, want complete text", decision.FinalAnswer)
	}
}

func TestParseRouteDecisionTreatsStructuredFinalAnswerWithoutModeAsDirectAnswer(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"speech":"Short answer.","text":"Complete answer."}`),
		"Answer directly.",
	)

	if decision.Mode != routeModeDirectAnswer {
		t.Fatalf("route mode = %q, want direct_answer", decision.Mode)
	}
	if decision.FinalAnswer != "Complete answer." {
		t.Fatalf("final answer = %q, want complete text", decision.FinalAnswer)
	}
}

func TestParseRouteDecisionTreatsExplicitTextModeCommandsAsIntent(t *testing.T) {
	cases := []struct {
		text string
		want routeMode
	}{
		{text: "switch to plan mode", want: routeModePlan},
		{text: "enter_plan_mode because this needs checkpoints", want: routeModePlan},
		{text: "use simple mode", want: routeModeSimple},
		{text: "use_simple_mode\n<reason>short task</reason>", want: routeModeSimple},
	}
	for _, tc := range cases {
		decision := parseRouteDecision(contentResponse(tc.text), "request")
		if decision.Mode != tc.want {
			t.Fatalf("route mode for %q = %q, want %q", tc.text, decision.Mode, tc.want)
		}
	}
}

func TestParseRouteDecisionForcesPlanForMultiStageRequest(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"simple","reason":"model underestimated task"}`),
		"Stage 1: compute A.\nStage 2: compute B.\nStage 3: reconcile invoice total.",
	)

	if decision.Mode != routeModePlan {
		t.Fatalf("route mode = %q, want plan", decision.Mode)
	}
}

func TestParseRouteDecisionRejectsDirectAnswerForDeviceOperation(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"direct_answer","final_answer":"已经发好了","reason":"model guessed"}`),
		"帮我给微信好友发消息问问电话号还在用吗",
	)

	if decision.Mode != routeModeSimple {
		t.Fatalf("route mode = %q, want simple", decision.Mode)
	}
	if decision.FinalAnswer != "" {
		t.Fatalf("device operation should clear final answer: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "requires tool execution") {
		t.Fatalf("route reason missing tool execution guard: %#v", decision)
	}
}

func TestParseRouteDecisionDoesNotForcePlanFromPhoneFlowKeywords(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"simple","reason":"model underestimated phone task"}`),
		"去通讯录里查电话号，然后打开微信给好友问问电话号还在用吗",
	)

	if decision.Mode != routeModeSimple {
		t.Fatalf("route mode = %q, want simple", decision.Mode)
	}
}

func TestParseRouteDecisionAllowsHowToQuestionAboutPhone(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"direct_answer","final_answer":"可以先打开微信搜索联系人。","reason":"how-to answer"}`),
		"怎么在手机上打开微信搜索联系人？",
	)

	if decision.Mode != routeModeDirectAnswer {
		t.Fatalf("route mode = %q, want direct_answer", decision.Mode)
	}
}

func TestParseRouteDecisionAllowsLaunchOnlyOpenAppDirectAnswer(t *testing.T) {
	decision := parseRouteDecision(
		contentResponse(`{"mode":"direct_answer","final_answer":"好的。","reason":"launch-only"}`),
		"打开微信",
	)

	if decision.Mode != routeModeDirectAnswer {
		t.Fatalf("route mode = %q, want direct_answer", decision.Mode)
	}
}
