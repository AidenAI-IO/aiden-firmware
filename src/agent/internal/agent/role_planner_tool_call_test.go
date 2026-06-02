package agent

import (
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
							Arguments: `{"__arg1": "{\"type\": \"tap\", \"point\": {\"x\": 0.09, \"y\": 0.05}}", "description": "点击左上角返回箭头，回到上一级设置菜单"}`,
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

func TestParseVerifierDecisionAcceptsExplicitTextFinalAnswer(t *testing.T) {
	decision := parseVerifierDecision("Final Answer: done", "")

	if !decision.CanFinish {
		t.Fatalf("explicit final answer should finish: %#v", decision)
	}
	if decision.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want done", decision.FinalAnswer)
	}
}
