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

func TestFinishStepEntersVerifierReview(t *testing.T) {
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
		Phase:               phaseExecution,
		StepExecutionActive: true,
		NextStep:            "use echo",
	}
	turn := executor.handleExecutorMetaTool(state, schema.AgentAction{
		Tool:      toolFinishStep,
		ToolInput: `{"summary":"echo ok"}`,
	})
	if turn.Kind != executorTurnFinishStep {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.ExecutorStepOutcome != "finished" || state.ExecutorStepSummary != "echo ok" {
		t.Fatalf("state = %#v", state)
	}
}

func TestFinishStepStoresKeyInfo(t *testing.T) {
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
		Phase:               phaseExecution,
		StepExecutionActive: true,
		NextStep:            "read the account id",
	}
	turn := executor.handleExecutorMetaTool(state, schema.AgentAction{
		Tool:      toolFinishStep,
		ToolInput: `{"summary":"account id found","key_info":["account_id=abc123","page=Account Details"]}`,
	})
	if turn.Kind != executorTurnFinishStep {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.ExecutorStepSummary != "account id found" {
		t.Fatalf("summary = %q", state.ExecutorStepSummary)
	}
	if got := strings.Join(state.ExecutorStepKeyInfo, "|"); got != "account_id=abc123|page=Account Details" {
		t.Fatalf("key info = %#v", state.ExecutorStepKeyInfo)
	}
	if !strings.Contains(turn.Step.Observation, "account_id=abc123") {
		t.Fatalf("observation should include key_info: %s", turn.Step.Observation)
	}
}

func TestAbortStepEntersVerifierReview(t *testing.T) {
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
		Phase:               phaseExecution,
		StepExecutionActive: true,
		NextStep:            "blocked step",
	}
	turn := executor.handleExecutorMetaTool(state, schema.AgentAction{
		Tool:      toolAbortStep,
		ToolInput: `{"reason":"permission denied"}`,
	})
	if turn.Kind != executorTurnAbortStep {
		t.Fatalf("turn kind = %v", turn.Kind)
	}
	if state.ExecutorStepOutcome != "aborted" || state.ExecutorStepSummary != "permission denied" {
		t.Fatalf("state = %#v", state)
	}
}

func TestBeginStepExecutionClearsStepScratchpad(t *testing.T) {
	state := roleLoopState{
		StepToolSteps: []schema.AgentStep{{Observation: "old"}},
		StepExecutionResults: []roleExecutionResult{{
			CandidateAnswer: "old",
		}},
		ExecutorStepOutcome: "finished",
		ExecutorStepSummary: "old",
		ExecutorStepKeyInfo: []string{"old-id"},
	}
	state.beginStepExecution()
	if len(state.StepToolSteps) != 0 || len(state.StepExecutionResults) != 0 {
		t.Fatalf("step scratchpad not cleared: %#v", state)
	}
	if state.ExecutorStepOutcome != "" || state.ExecutorStepSummary != "" || len(state.ExecutorStepKeyInfo) != 0 || !state.StepExecutionActive {
		t.Fatalf("step execution not reset: %#v", state)
	}
}

func TestRecordPlanStepResultSurvivesStepClear(t *testing.T) {
	state := roleLoopState{
		Plan:                []string{"read account id", "use account id"},
		PlanStepIndex:       0,
		NextStep:            "read account id",
		ExecutorStepOutcome: "finished",
		ExecutorStepSummary: "account id found",
		ExecutorStepKeyInfo: []string{"account_id=abc123"},
	}
	state.recordPlanStepResult(verifierDecision{NeedsReplan: false, Reason: "step ok"})
	state.clearStepExecution()

	if len(state.PlanStepResults) != 1 {
		t.Fatalf("plan step results = %#v", state.PlanStepResults)
	}
	record := state.PlanStepResults[0]
	if record.StepIndex != 1 || record.StepText != "read account id" || record.Summary != "account id found" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if got := strings.Join(record.KeyInfo, "|"); got != "account_id=abc123" {
		t.Fatalf("key info = %#v", record.KeyInfo)
	}
	if len(state.ExecutorStepKeyInfo) != 0 {
		t.Fatalf("step key info should be cleared: %#v", state.ExecutorStepKeyInfo)
	}
}

func TestExecutorPromptIncludesPriorPlanStepResults(t *testing.T) {
	state := roleLoopState{
		Plan:          []string{"read account id", "use account id"},
		PlanStepIndex: 1,
		NextStep:      "use account id",
		PlanStepResults: []planStepResult{{
			StepIndex: 1,
			StepText:  "read account id",
			Outcome:   "finished",
			Summary:   "account id found",
			KeyInfo:   []string{"account_id=abc123"},
		}},
	}

	prompt := buildExecutorStatePrompt(state, "Executor task.")
	for _, want := range []string{
		"Prior step results",
		"step_index=1",
		"summary=\"account id found\"",
		"key_info=[account_id=abc123]",
		"Planner-approved next_step:\nuse account id",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("executor prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Current plan:\n") || strings.Contains(prompt, "Original user request") {
		t.Fatalf("executor prompt should not expose full planner context:\n%s", prompt)
	}
}
