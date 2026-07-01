package agent

import (
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestCommitPlanFailuresReturnStructuredObservationAndStopAfterLimit(t *testing.T) {
	executor := &roleCollaborativeExecutor{}
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}
	action := schema.AgentAction{Tool: toolCommitPlan, ToolInput: `{"plan":[]}`}

	first := executor.handlePlannerMetaTool(phasePlan, state, action)
	if first.Kind != plannerTurnInvalidMeta || first.InvalidMetaStep == nil {
		t.Fatalf("first turn = %#v, want invalid meta", first)
	}
	if !strings.Contains(first.InvalidMetaStep.Observation, `"error"`) ||
		!strings.Contains(first.InvalidMetaStep.Observation, "commit_plan requires a non-empty plan") {
		t.Fatalf("first observation = %q, want structured error", first.InvalidMetaStep.Observation)
	}
	if state.ConsecutiveCommitPlanFailures != 1 {
		t.Fatalf("failure count = %d, want 1", state.ConsecutiveCommitPlanFailures)
	}

	_ = executor.handlePlannerMetaTool(phasePlan, state, action)
	third := executor.handlePlannerMetaTool(phasePlan, state, action)
	if third.Kind != plannerTurnFinish {
		t.Fatalf("third turn kind = %v, want finish", third.Kind)
	}
	if state.Phase != phaseDefault || state.PlanCommitRequired {
		t.Fatalf("state after failure limit = %#v", state)
	}
	if !strings.Contains(third.Answer, "规划提交连续失败") ||
		!strings.Contains(third.Answer, "commit_plan requires a non-empty plan") {
		t.Fatalf("final answer = %q", third.Answer)
	}
}

func TestCommitPlanSuccessResetsFailureCounter(t *testing.T) {
	executor := &roleCollaborativeExecutor{}
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}

	_ = executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: `{"plan":[]}`,
	})
	if state.ConsecutiveCommitPlanFailures != 1 {
		t.Fatalf("failure count after invalid plan = %d, want 1", state.ConsecutiveCommitPlanFailures)
	}

	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: `{"objective":"inspect","plan":["screenshot"],"reason":"ready"}`,
	})
	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.ConsecutiveCommitPlanFailures != 0 {
		t.Fatalf("failure count after commit = %d, want reset", state.ConsecutiveCommitPlanFailures)
	}
}
