package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestDefaultModeFinishesWithoutVerifier(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("done")}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("output = %q", result.Output)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1", model.callCount)
	}
}

func TestEnterPlanModePreservesToolSteps(t *testing.T) {
	executor := newRoleCollaborativeExecutor(
		&scriptedModel{},
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
	)
	state := &roleLoopState{
		Phase: phaseDefault,
		ToolSteps: []schema.AgentStep{{
			Action:      schema.AgentAction{Tool: "echo"},
			Observation: "ok",
		}},
	}
	turn := executor.handlePlannerMetaTool(phaseDefault, state, schema.AgentAction{
		Tool:      toolEnterPlanMode,
		ToolInput: `{"reason":"complex"}`,
	})
	if turn.Kind != plannerTurnEnterPlan {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.Phase != phasePlan {
		t.Fatalf("phase = %q", state.Phase)
	}
	if len(state.ToolSteps) != 1 {
		t.Fatalf("tool steps = %d, want 1", len(state.ToolSteps))
	}
}

func TestCommitPlanOnlyInPlanMode(t *testing.T) {
	executor := newRoleCollaborativeExecutor(
		&scriptedModel{},
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
	)
	state := &roleLoopState{Phase: phaseDefault}
	turn := executor.handlePlannerMetaTool(phaseDefault, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: `{"objective":"x","plan":["step"],"reason":"ready"}`,
	})
	if turn.Kind != plannerTurnInvalidMeta {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if !strings.Contains(turn.InvalidMetaStep.Observation, "only available in plan mode") {
		t.Fatalf("observation = %q", turn.InvalidMetaStep.Observation)
	}
}

func TestCommitPlanEntersExecutionMode(t *testing.T) {
	executor := newRoleCollaborativeExecutor(
		&scriptedModel{},
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
	)
	state := &roleLoopState{Phase: phasePlan}
	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: `{"objective":"inspect","completion_criteria":["done"],"plan":["screenshot","answer"],"reason":"ready"}`,
	})
	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.Phase != phaseExecution {
		t.Fatalf("phase = %q", state.Phase)
	}
	if state.PlanStepIndex != 0 || state.NextStep != "screenshot" {
		t.Fatalf("state = %#v", state)
	}
}

func TestCancelPlanReturnsToDefaultMode(t *testing.T) {
	executor := newRoleCollaborativeExecutor(
		&scriptedModel{},
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
	)
	state := &roleLoopState{
		Phase:     phasePlan,
		Plan:      []string{"draft"},
		NextStep:  "draft",
		Objective: "draft",
	}
	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCancelPlan,
		ToolInput: `{"reason":"abort"}`,
	})
	if turn.Kind != plannerTurnCancelPlan {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.Phase != phaseDefault {
		t.Fatalf("phase = %q", state.Phase)
	}
	if len(state.Plan) != 0 {
		t.Fatalf("plan should be cleared: %#v", state.Plan)
	}
}

func TestAdvancePlanStepOrExhaust(t *testing.T) {
	state := roleLoopState{
		Phase:         phaseExecution,
		Plan:          []string{"one"},
		PlanStepIndex: 0,
	}
	if !state.advancePlanStepOrExhaust() {
		t.Fatal("expected exhausted transition")
	}
	if state.Phase != phasePlan || !state.PlanExhausted {
		t.Fatalf("state = %#v", state)
	}
}
