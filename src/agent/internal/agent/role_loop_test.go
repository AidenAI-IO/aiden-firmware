package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
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
		nil,
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
	if !state.PlanCommitRequired {
		t.Fatal("entering plan mode should require commit_plan before execution")
	}
	if len(state.ToolSteps) != 1 {
		t.Fatalf("tool steps = %d, want 1", len(state.ToolSteps))
	}
}

func TestExecutorScratchpadDoesNotReplaySpeechArgumentAsContent(t *testing.T) {
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
		nil,
	)
	state := roleLoopState{
		StepToolSteps: []schema.AgentStep{{
			Action: schema.AgentAction{
				Tool:      "mouse_click",
				ToolInput: `{"button":"left","x":500,"y":850}`,
				Log:       formatToolActionLog("mouse_click", `{"button":"left","speech":"点击支付按钮","x":500,"y":850}`, "", "\n"),
				ToolID:    "call_1",
			},
			Observation: "ok",
		}},
	}

	messages := executor.roleMessages(
		RoleProfile{Name: RoleExecutor, SystemPrompt: "system"},
		map[string]string{"input": "continue"},
		state,
		"task",
	)
	for _, message := range messages {
		for _, part := range message.Parts {
			toolCall, ok := part.(llms.ToolCall)
			if !ok || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != "mouse_click" {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &args); err != nil {
				t.Fatalf("decode scratchpad tool arguments: %v", err)
			}
			if _, ok := args["speech"]; ok {
				t.Fatalf("scratchpad should not replay previous tool-call speech as an argument; args=%#v", args)
			}
			return
		}
	}
	t.Fatalf("scratchpad mouse_click tool call not found: %#v", messages)
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
		nil,
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

func TestDecisionPhaseEntersPlanModeBeforeDomainTools(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse(`{"mode":"plan","reason":"three subtasks","confidence":0.91}`),
	}}
	tool := &stubTool{name: "echo", description: "Echo.", output: "ok"}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		[]langtools.Tool{tool},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phaseDecision}
	toolSpecs := NewToolSpecs([]langtools.Tool{tool})
	inputs := map[string]string{"input": "multi-step task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecs)
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}
	if turn.Kind != plannerTurnEnterPlan {
		t.Fatalf("turn kind = %v, want enter plan", turn.Kind)
	}
	if state.Phase != phasePlan {
		t.Fatalf("phase = %q, want plan", state.Phase)
	}
	if !state.PlanCommitRequired {
		t.Fatal("plan commit should be required after entering plan mode")
	}
	if len(tool.inputs) != 0 {
		t.Fatalf("tool executions = %d, want 0", len(tool.inputs))
	}
	if turn.Step == nil || turn.Step.Action.Tool != toolEnterPlanMode {
		t.Fatalf("observation = %#v", turn.Step)
	}
	if len(model.tools) != 1 || len(model.tools[0]) != 0 {
		t.Fatalf("route phase should not expose tools: %#v", model.tools)
	}
}

func TestDecisionPhaseUseSimpleModeSwitchesToDefaultWithoutToolStep(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse(`{"mode":"simple","reason":"ordinary tool loop is enough","confidence":0.82}`),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phaseDecision}
	inputs := map[string]string{"input": "simple task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, NewToolSpecs(nil))
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}

	if turn.Kind != plannerTurnUseSimpleMode {
		t.Fatalf("turn kind = %v, want use simple mode", turn.Kind)
	}
	if state.Phase != phaseDefault {
		t.Fatalf("phase = %q, want default", state.Phase)
	}
	if turn.Step != nil {
		t.Fatalf("simple route should not create a tool step: %#v", turn.Step)
	}
}

func TestRoutePolicyOverridesSimpleForMultiStageRequests(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse(`{"mode":"simple","reason":"model underestimated task"}`),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phaseDecision}
	inputs := map[string]string{
		"input":   "Stage 1: compute A.\nStage 2: compute B.\nStage 3: reconcile invoice total.",
		"history": "",
	}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, NewToolSpecs(nil))
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}
	if turn.Kind != plannerTurnEnterPlan {
		t.Fatalf("turn kind = %v, want enter plan", turn.Kind)
	}
	if state.Phase != phasePlan || !state.PlanCommitRequired {
		t.Fatalf("state = %#v, want plan phase with commit required", state)
	}
}

func TestPlanRequiresCommitBeforeExecutionToolUse(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("echo_1", "echo", `{"text":"three"}`),
	}}
	tool := &stubTool{name: "echo", description: "Echo.", output: "ok"}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		[]langtools.Tool{tool},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}
	toolSpecs := NewToolSpecs([]langtools.Tool{tool})
	inputs := map[string]string{"input": "multi-step task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecs)
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}

	if turn.Kind != plannerTurnInvalidMeta {
		t.Fatalf("turn kind = %v, want invalid meta", turn.Kind)
	}
	if state.Phase != phasePlan || !state.PlanCommitRequired {
		t.Fatalf("state = %#v, want plan phase with commit still required", state)
	}
	if len(tool.inputs) != 0 {
		t.Fatalf("tool executions = %d, want 0", len(tool.inputs))
	}
	if turn.Step == nil || !strings.Contains(turn.Step.Observation, "commit_plan") {
		t.Fatalf("observation = %#v, want commit_plan instruction", turn.Step)
	}
}

func TestAutoPlanRejectsCancelBeforeCommit(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("cancel_1", toolCancelPlan, `{"reason":"already solved"}`),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}
	inputs := map[string]string{"input": "multi-step task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, NewToolSpecs(nil))
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}

	if turn.Kind != plannerTurnInvalidMeta {
		t.Fatalf("turn kind = %v, want invalid meta", turn.Kind)
	}
	if state.Phase != phasePlan || !state.PlanCommitRequired {
		t.Fatalf("state = %#v, want plan phase with commit still required", state)
	}
	if turn.Step == nil || !strings.Contains(turn.Step.Observation, "commit_plan") {
		t.Fatalf("observation = %#v, want commit_plan instruction", turn.Step)
	}
}

func TestPlanModeAllowsReadOnlyToolBeforeCommit(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("recall_1", "recall_memory", `{"tags":["invoice"],"limit":3}`),
	}}
	tool := &stubTool{name: "recall_memory", description: "Recall memory.", output: `{"memories":[]}`}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		[]langtools.Tool{tool},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}
	toolSpecs := NewToolSpecs([]langtools.Tool{tool})
	inputs := map[string]string{"input": "plan after checking memory", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecs)
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}

	if turn.Kind != plannerTurnTool {
		t.Fatalf("turn kind = %v, want readonly planner tool", turn.Kind)
	}
	if state.Phase != phasePlan || !state.PlanCommitRequired {
		t.Fatalf("state = %#v, want plan phase with commit still required", state)
	}
	if len(tool.inputs) != 1 {
		t.Fatalf("tool executions = %d, want 1", len(tool.inputs))
	}
	if len(state.PlannerEvidence) != 1 {
		t.Fatalf("planner evidence = %#v, want one readonly result", state.PlannerEvidence)
	}
}

func TestPlanModeRejectsDomainToolsAfterCommit(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("echo_1", "echo", `{"text":"do work"}`),
	}}
	tool := &stubTool{name: "echo", description: "Echo.", output: "ok"}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		[]langtools.Tool{tool},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phasePlan, PlanCommitted: true}
	toolSpecs := NewToolSpecs([]langtools.Tool{tool})
	inputs := map[string]string{"input": "multi-step task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecs)
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}

	if turn.Kind != plannerTurnInvalidMeta {
		t.Fatalf("turn kind = %v, want invalid meta", turn.Kind)
	}
	if len(tool.inputs) != 0 {
		t.Fatalf("tool executions = %d, want 0", len(tool.inputs))
	}
	if turn.Step == nil || !strings.Contains(turn.Step.Observation, "Put execution, computation, and state-changing work into committed executor steps") {
		t.Fatalf("observation = %#v, want plan-mode tool rejection", turn.Step)
	}
}

func TestPlanModeAllowsEvidenceBackedFinalAnswer(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		contentResponse("<final_answer>(b)</final_answer>"),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{
		Phase:         phasePlan,
		PlanCommitted: true,
		VerifierResults: []verifierDecision{{
			NeedsReplan: true,
			Reason:      "final answer can be derived from evidence",
		}},
	}
	inputs := map[string]string{"input": "multi-step task", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, NewToolSpecs(nil))
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}
	if turn.Kind != plannerTurnFinish || !strings.Contains(turn.Answer, "<final_answer>(b)</final_answer>") {
		t.Fatalf("turn = %#v, want final answer", turn)
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
		nil,
	)
	state := &roleLoopState{Phase: phasePlan, PlanCommitRequired: true}
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
	if state.PlanCommitRequired {
		t.Fatal("plan commit requirement should be cleared after commit_plan")
	}
	if state.Todo.Mode != TodoModePlanned || state.Todo.Revision != 1 || len(state.Todo.Items) != 2 {
		t.Fatalf("planned todo not initialized: %#v", state.Todo)
	}
	if state.Todo.Items[0].Status != TodoPending || state.Todo.Items[1].Status != TodoPending {
		t.Fatalf("planned todo items should start pending: %#v", state.Todo.Items)
	}
}

func TestCommitPlanParsesStringPlanAndCriteria(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"reconcile",
		"completion_criteria":"- compute total\n- output final answer",
		"plan":"Step 1: compute subtotal. Step 2: compare options. Step 3: output final answer.",
		"reason":"ready"
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.CompletionCriteria) != 2 {
		t.Fatalf("criteria = %#v, want 2 parsed criteria", decision.CompletionCriteria)
	}
	if len(decision.Plan) != 3 {
		t.Fatalf("plan = %#v, want 3 parsed steps", decision.Plan)
	}
	if decision.Plan[0] != "Step 1: compute subtotal." || decision.Plan[2] != "Step 3: output final answer." {
		t.Fatalf("unexpected parsed plan: %#v", decision.Plan)
	}
}

func TestSetTodoRejectsOutOfRangeStatusIndices(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "completed zero",
			input: `{"items":["one","two"],"current_index":1,"completed_indices":[0]}`,
			want:  "completed_indices contains index 0 outside 1..2",
		},
		{
			name:  "completed too large",
			input: `{"items":["one","two"],"current_index":1,"completed_indices":[3]}`,
			want:  "completed_indices contains index 3 outside 1..2",
		},
		{
			name:  "blocked negative",
			input: `{"items":["one","two"],"current_index":1,"blocked_indices":[-1]}`,
			want:  "blocked_indices contains index -1 outside 1..2",
		},
		{
			name:  "blocked too large",
			input: `{"items":["one","two"],"current_index":1,"blocked_indices":[9]}`,
			want:  "blocked_indices contains index 9 outside 1..2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSetTodoInput(tt.input)
			if err == nil {
				t.Fatal("parseSetTodoInput() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseSetTodoInput() error = %q, want containing %q", err.Error(), tt.want)
			}
		})
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
		nil,
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
	if state.Phase != phaseExecution || !state.PlanExhausted {
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
		nil,
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

func TestExecutorMarkedFinalAnswerEntersVerifierReview(t *testing.T) {
	handler := &toolExecutionCallbackRecorder{}
	executor := newRoleCollaborativeExecutor(
		&scriptedModel{responses: []*llms.ContentResponse{
			contentResponse("The result matches option (a).\n\n<final_answer>(a)</final_answer>"),
		}},
		RoleProfiles{},
		nil,
		nil,
		10,
		nil,
		handler,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{
		Phase:               phaseExecution,
		PlanCommitted:       true,
		StepExecutionActive: true,
		NextStep:            "compare result and output final answer",
	}
	inputs := map[string]string{"input": "choose the option", "history": ""}

	turn, err := executor.callExecutorTurn(context.Background(), inputs, state, NewToolSpecs(nil))
	if err != nil {
		t.Fatalf("executor turn error: %v", err)
	}

	if turn.Kind != executorTurnFinishStep {
		t.Fatalf("turn kind = %v, want finish step", turn.Kind)
	}
	if state.ExecutorStepOutcome != "finished" || !strings.Contains(state.ExecutorStepSummary, "<final_answer>(a)</final_answer>") {
		t.Fatalf("state = %#v", state)
	}
	if len(handler.calls) != 1 || handler.calls[0].Action.Tool != toolFinishStep {
		t.Fatalf("callback calls = %#v, want synthetic finish_step action", handler.calls)
	}
}

func TestCallExecutorTurnOmitOversizedToolResultBeforeModelCall(t *testing.T) {
	hugeObservation := strings.Repeat("超", 2000)
	model := &contextWindowScriptedModel{
		scriptedModel: scriptedModel{responses: []*llms.ContentResponse{
			finishStepToolCall("handled oversized tool result"),
		}},
		window: 300,
	}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{
			Executor: RoleProfile{
				Name:         RoleExecutor,
				SystemPrompt: strings.Repeat("executor system prompt ", 20),
			},
		},
		[]langtools.Tool{&stubTool{name: "dump", description: "Return a dump."}},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{
		Phase:               phaseExecution,
		PlanCommitted:       true,
		StepExecutionActive: true,
		NextStep:            "inspect dump",
		StepToolSteps: []schema.AgentStep{{
			Action: schema.AgentAction{
				Tool:      "dump",
				ToolInput: "{}",
				ToolID:    "dump_1",
			},
			Observation: hugeObservation,
		}},
	}
	inputs := map[string]string{"input": "summarize the dump", "history": ""}

	turn, err := executor.callExecutorTurn(context.Background(), inputs, state, NewToolSpecs(executor.Tools))
	if err != nil {
		t.Fatalf("executor turn error: %v", err)
	}
	if turn.Kind != executorTurnFinishStep {
		t.Fatalf("turn kind = %v, want finish step", turn.Kind)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1", model.callCount)
	}
	content := firstToolResponseContent(t, model.messages[0])
	if strings.Contains(content, strings.Repeat("超", 20)) {
		t.Fatalf("oversized raw observation leaked into model prompt: %q", content)
	}
	if !strings.Contains(content, "tool result omitted") || !strings.Contains(content, "context_window=300") {
		t.Fatalf("tool response should explain the context-window rejection, got: %q", content)
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
		nil,
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

type contextWindowScriptedModel struct {
	scriptedModel
	window int
}

func (m *contextWindowScriptedModel) contextWindow() int {
	return m.window
}

func firstToolResponseContent(t *testing.T, messages []llms.MessageContent) string {
	t.Helper()
	for _, message := range messages {
		for _, part := range message.Parts {
			if response, ok := part.(llms.ToolCallResponse); ok {
				return response.Content
			}
		}
	}
	t.Fatalf("no tool response found in messages: %#v", messages)
	return ""
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
		nil,
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

func TestRequestContextPromptDoesNotMarkRootRequestAuthoritative(t *testing.T) {
	inputs := map[string]string{
		"input":              "打开微信",
		rootRequestInputKey:  "查天气",
		latestUserInputKey:   "打开微信",
		"follow_up_relation": "continuation",
	}
	prompt := buildRoleStatePrompt(RolePlanner, inputs, roleLoopState{Phase: phasePlan}, "Planner task.")

	for _, unwanted := range []string{
		"Original user request / root request (authoritative; do not replace it with a subtask)",
		"authoritative; do not replace it with a subtask",
		"Current objective:",
		"Satisfy every explicit requirement in the original user request.",
		"Follow-up classification:",
		"follow_up_relation",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("planner prompt should not contain %q:\n%s", unwanted, prompt)
		}
	}
	for _, want := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"Planner task.",
		"Loop mode:",
		"Original user request / root request:",
		"查天气",
		"Latest user message:",
		"打开微信",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("planner prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestPlannerMessagesAppendRuntimeStateContext(t *testing.T) {
	executor := &roleCollaborativeExecutor{}
	state := roleLoopState{
		Phase:         phasePlan,
		PlanCommitted: true,
		Plan:          []string{"inspect", "act"},
		NextStep:      "inspect",
		PlanStepResults: []planStepResult{{
			StepIndex:      1,
			StepText:       "inspect",
			Outcome:        "blocked",
			Summary:        "screen did not change",
			NeedsReplan:    true,
			VerifierReason: "not enough progress",
		}},
		VerifierResults: []verifierDecision{{
			NeedsReplan: true,
			Reason:      "not enough progress",
		}},
		Todo: TodoState{
			Mode:      TodoModePlanned,
			Objective: "inspect and act",
			Revision:  1,
			CurrentID: "todo-r1-step1",
			Items: []TodoItem{{
				ID:        "todo-r1-step1",
				Text:      "inspect",
				Status:    TodoBlocked,
				Source:    TodoSourceCommittedPlan,
				StepIndex: 1,
			}},
		},
		World: worldState{
			Observation: &worldStateObservation{
				observedWorldState: observedWorldState{
					AppName:     "微信",
					PageName:    "聊天列表",
					Platform:    "android",
					VisibleText: []string{"微信", "通讯录"},
					Dialogs:     []string{"权限提示"},
					Confidence:  0.82,
				},
			},
		},
	}
	state.World.UpdateDeviceEnvironment(&PhoneEnvironment{
		Platform:       "android",
		SystemName:     "Android",
		SystemVersion:  "15",
		ThirdPartyApps: []AvailableAppInfo{{Name: "微信", Available: true}},
	})
	inputs := map[string]string{
		"input":                "fallback request",
		rootRequestInputKey:    "原始用户请求",
		latestUserInputKey:     "最新用户消息",
		sessionContextInputKey: "Session context view:\n- Latest user message: 最新用户消息",
	}

	messages := executor.roleMessages(RoleProfile{Name: RolePlanner, SystemPrompt: "planner system"}, inputs, state, "Planner task.")
	if len(messages) != 3 {
		t.Fatalf("planner messages = %d, want system, raw user input, runtime state: %#v", len(messages), messages)
	}
	if messages[0].Role != llms.ChatMessageTypeSystem {
		t.Fatalf("message[0] role = %s, want system", messages[0].Role)
	}
	if messageText(messages[:1]) != "planner system\n" {
		t.Fatalf("planner system should not receive runtime state:\n%s", messageText(messages[:1]))
	}
	if messages[1].Role != llms.ChatMessageTypeHuman || messageText(messages[1:2]) != "fallback request\n" {
		t.Fatalf("message[1] should be raw current user input, got role=%s text=%q", messages[1].Role, messageText(messages[1:2]))
	}
	statePrompt := messageText(messages[2:])
	for _, want := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"Planner task.",
		"Loop mode:",
		"Original user request",
		"Latest user message",
		"Current plan:",
		"World State",
		"Device environment: available",
		"Confirmed third-party apps:",
		"Observed app/page:",
		"Visible text:",
		"Dialogs:",
		"Session context view:",
		"Current todo state:",
		"Prior step results",
		"summary=\"screen did not change\"",
		"Verifier feedback:",
		"not enough progress",
	} {
		if !strings.Contains(statePrompt, want) {
			t.Fatalf("planner runtime state prompt missing %q:\n%s", want, statePrompt)
		}
	}
	if strings.Contains(statePrompt, "Latest screenshot:") {
		t.Fatalf("planner runtime state should not include world latest screenshot text:\n%s", statePrompt)
	}
}

func TestForceSimpleLoopPlannerRuntimeContextOmitsDelegatedPlanSections(t *testing.T) {
	state := roleLoopState{
		Phase:           phaseDefault,
		ForceSimpleLoop: true,
		Plan:            []string{"inspect", "act"},
		NextStep:        "inspect",
		PlanStepResults: []planStepResult{{
			StepIndex:      1,
			StepText:       "inspect",
			Outcome:        "blocked",
			Summary:        "screen did not change",
			NeedsReplan:    true,
			VerifierReason: "not enough progress",
		}},
		VerifierResults: []verifierDecision{{
			NeedsReplan: true,
			Reason:      "not enough progress",
		}},
		Todo: TodoState{
			Mode:      TodoModeSimple,
			Objective: "inspect and act",
			Revision:  2,
			CurrentID: "todo-simple-2",
			Items: []TodoItem{{
				ID:        "todo-simple-1",
				Text:      "inspect",
				Status:    TodoDone,
				Source:    TodoSourceExplicitSimple,
				StepIndex: 1,
			}, {
				ID:        "todo-simple-2",
				Text:      "act",
				Status:    TodoInProgress,
				Source:    TodoSourceExplicitSimple,
				StepIndex: 2,
			}},
		},
		PendingTodoReminder: "call set_todo if the task now needs an explicit checklist.",
		World: worldState{
			Observation: &worldStateObservation{
				observedWorldState: observedWorldState{
					AppName:     "微信",
					PageName:    "聊天列表",
					Platform:    "android",
					VisibleText: []string{"微信", "通讯录"},
				},
			},
		},
	}
	inputs := map[string]string{
		"input":                "继续",
		rootRequestInputKey:    "打开微信后继续处理",
		latestUserInputKey:     "继续",
		sessionContextInputKey: "Session context view:\n- Latest user message: 继续",
	}

	prompt := buildRoleStatePrompt(RolePlanner, inputs, state, "Planner task.")
	for _, want := range []string{
		"Planner runtime context (synthetic; not a new user request):",
		"Loop mode:",
		"force_simple_loop: true",
		"World State",
		"Original user request / root request:",
		"Latest user message:",
		"Session context view:",
		"Current todo state:",
		"Todo reminder:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("force_simple_loop planner runtime context missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"Current plan:",
		"Prior step results",
		"Verifier feedback:",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("force_simple_loop planner runtime context should not contain %q:\n%s", unwanted, prompt)
		}
	}
}

func TestPlannerCurrentUserMessageIsRawInputOnly(t *testing.T) {
	executor := &roleCollaborativeExecutor{
		InputAttachments: []InputAttachment{{
			Kind:     AttachmentKindImage,
			Name:     "screen.png",
			MIMEType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47},
		}},
	}
	state := roleLoopState{
		Phase: phasePlan,
		Plan:  []string{"inspect"},
		World: worldState{
			Observation: &worldStateObservation{
				observedWorldState: observedWorldState{
					AppName:     "微信",
					PageName:    "聊天列表",
					VisibleText: []string{"微信"},
				},
			},
		},
	}
	inputs := map[string]string{
		"input":             "当前用户原始输入",
		rootRequestInputKey: "原始根请求",
		latestUserInputKey:  "当前用户原始输入",
	}

	messages := executor.roleMessages(RoleProfile{Name: RolePlanner, SystemPrompt: "planner system"}, inputs, state, "Planner task.")
	if len(messages) < 2 {
		t.Fatalf("planner messages = %#v, want system context and user input", messages)
	}
	raw := messages[1]
	if raw.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("raw planner message role = %s, want human; messages=%#v", raw.Role, messages)
	}
	var textParts []string
	var imageParts int
	for _, part := range raw.Parts {
		switch typed := part.(type) {
		case llms.TextContent:
			textParts = append(textParts, typed.Text)
		case llms.ImageURLContent:
			imageParts++
		}
	}
	if len(textParts) != 1 || imageParts != 1 {
		t.Fatalf("raw planner message parts = %#v, want one raw text part and one raw image part", raw.Parts)
	}
	text := textParts[0]
	if text != "当前用户原始输入" {
		t.Fatalf("raw planner user message = %q, want raw input only", text)
	}
	for _, unwanted := range []string{
		"Planner task.",
		"Loop mode:",
		"Original user request",
		"Latest user message",
		"Current plan:",
		"World State",
		"Observed app/page:",
		"Attached content:",
		"screen.png",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("raw planner user message should not contain %q:\n%s", unwanted, text)
		}
	}
	systemText := messageText(messages[:1])
	if strings.TrimSpace(systemText) != "planner system" {
		t.Fatalf("planner system message should not receive runtime state context:\n%s", systemText)
	}
	statePrompt := messageText(messages[len(messages)-1:])
	if !strings.Contains(statePrompt, "Planner runtime context (synthetic; not a new user request):") ||
		!strings.Contains(statePrompt, "Planner task.") ||
		strings.Contains(statePrompt, "Attached content:") ||
		strings.Contains(statePrompt, "screen.png") {
		t.Fatalf("planner runtime state should be final pure-text context without attachment metadata:\n%s", statePrompt)
	}
}

func TestPlannerCurrentUserMessagePrecedesToolScratchpad(t *testing.T) {
	executor := &roleCollaborativeExecutor{}
	toolStep := schema.AgentStep{
		Action: schema.AgentAction{
			Tool:      "recall_memory",
			ToolInput: `{"tags":["weather","location"]}`,
			Log:       "我查下默认地点。",
		},
		Observation: `{"results":[]}`,
	}
	state := roleLoopState{
		Phase:     phaseDefault,
		ToolSteps: []schema.AgentStep{toolStep},
		ExecutionResults: []roleExecutionResult{{
			Action: &toolStep.Action,
			Step:   &toolStep,
		}},
	}
	inputs := map[string]string{
		"input": "今天天气怎么样？",
	}

	messages := executor.roleMessages(RoleProfile{Name: RolePlanner, SystemPrompt: "planner system"}, inputs, state, "Planner task.")
	if len(messages) != 5 {
		t.Fatalf("planner messages = %d, want system, user, assistant tool call, tool result, runtime state: %#v", len(messages), messages)
	}
	if messages[0].Role != llms.ChatMessageTypeSystem {
		t.Fatalf("message[0] role = %s, want system", messages[0].Role)
	}
	if messages[1].Role != llms.ChatMessageTypeHuman || messageText(messages[1:2]) != "今天天气怎么样？\n" {
		t.Fatalf("message[1] should be raw current user input before tool scratchpad, got role=%s text=%q", messages[1].Role, messageText(messages[1:2]))
	}
	if messages[2].Role != llms.ChatMessageTypeAI {
		t.Fatalf("message[2] role = %s, want assistant tool call", messages[2].Role)
	}
	if messages[3].Role != llms.ChatMessageTypeTool {
		t.Fatalf("message[3] role = %s, want tool result", messages[3].Role)
	}
	if messages[4].Role != llms.ChatMessageTypeHuman || !strings.Contains(messageText(messages[4:5]), "Planner runtime context") {
		t.Fatalf("message[4] should be planner runtime state, got role=%s text=%q", messages[4].Role, messageText(messages[4:5]))
	}
	if strings.Contains(messageText(messages[4:5]), "Executor results:") {
		t.Fatalf("planner runtime state should not summarize planner tool scratchpad as executor results:\n%s", messageText(messages[4:5]))
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

	inputs := map[string]string{"input": "Use account id from the first step.", "history": ""}
	prompt := buildExecutorStatePrompt(inputs, state, "Executor task.")
	for _, want := range []string{
		"Original user request",
		"Use account id from the first step.",
		"Committed plan",
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
	if strings.Contains(prompt, "raw prior-step scratchpads") {
		t.Fatalf("executor prompt should not expose raw scratchpad internals:\n%s", prompt)
	}
}

func TestExecutorPromptIncludesPlannerEvidence(t *testing.T) {
	state := roleLoopState{
		NextStep: "finish the calculation using known values",
		PlannerEvidence: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "calculator", ToolInput: `{"expression":"1+2"}`},
			Step: &schema.AgentStep{
				Action:      schema.AgentAction{Tool: "calculator", ToolInput: `{"expression":"1+2"}`},
				Observation: "3",
			},
		}},
	}

	inputs := map[string]string{"input": "Compute 1+2 and finish.", "history": ""}
	prompt := buildExecutorStatePrompt(inputs, state, "Executor task.")
	for _, want := range []string{
		"Original user request",
		"Planner-provided evidence",
		"tool=calculator input={\"expression\":\"1+2\"} observation=3",
		"do not repeat the same direct tool call",
		"Planner-approved next_step:\nfinish the calculation using known values",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("executor prompt missing %q:\n%s", want, prompt)
		}
	}
}
