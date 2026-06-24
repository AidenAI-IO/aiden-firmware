package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func toolCallsContain(calls []ToolCall, name string) bool {
	for _, call := range calls {
		if toolNameEqual(call.Action.Tool, name) {
			return true
		}
	}
	return false
}

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

func TestDefaultModeExposesHumanHandoffTool(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("handoff_1", toolHumanHandoffStep, `{"reason":"authentication","details":"manual login confirmation is required"}`),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{Planner: RoleProfile{Name: RolePlanner, SystemPrompt: "planner"}},
		[]langtools.Tool{NewHumanHandoffTool()},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)
	state := &roleLoopState{Phase: phaseDefault}
	inputs := map[string]string{"input": "登录当前页面", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecsForRole(RolePlanner, executor.Tools))
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}
	if turn.Kind != plannerTurnSleep || turn.Step == nil {
		t.Fatalf("turn = %#v, want handoff tool execution that pauses the run", turn)
	}
	if turn.Step.Action.Tool != toolHumanHandoffStep {
		t.Fatalf("tool = %q, want %q", turn.Step.Action.Tool, toolHumanHandoffStep)
	}
	if !strings.Contains(turn.Step.Observation, "HUMAN_HANDOFF_REQUESTED") {
		t.Fatalf("observation = %q", turn.Step.Observation)
	}
}

func TestHumanHandoffToolPausesRun(t *testing.T) {
	handoffContent := "请在手机上选择登录方式，完成后告诉我继续。"
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponseWithContent("handoff_1", toolHumanHandoffStep, `{"reason":"login_method_selection","details":"manual login method selection is required"}`, handoffContent),
		contentResponse("should not be used"),
	}}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{Planner: RoleProfile{Name: RolePlanner, SystemPrompt: "planner"}},
		[]langtools.Tool{NewHumanHandoffTool()},
		nil,
		10,
		nil,
		nil,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)

	result, err := executor.Call(context.Background(), map[string]any{
		"input":   "登录当前页面",
		"history": "",
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if output := strings.TrimSpace(fmt.Sprint(result["output"])); output != handoffContent {
		t.Fatalf("output = %q, want %q", output, handoffContent)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1", model.callCount)
	}
}

func TestDefaultFinalReviewConvertsHumanQuestionToHandoff(t *testing.T) {
	review := `{
		"can_finish": false,
		"needs_human_handoff": true,
		"handoff_reason": "login_method_selection",
		"handoff_details": "当前页面需要用户在手机上选择登录方式后才能继续。",
		"suggested_action": "请在手机上选择登录方式，完成后告诉我继续。",
		"reason": "需要用户选择登录方式"
	}`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("screen_1", "screenshot", `{"__arg1":"{}"}`),
		contentResponse("请问你想用哪种方式登录？"),
		contentResponse(review),
	}}
	handler := &toolExecutionCallbackRecorder{}
	executor := newRoleCollaborativeExecutor(
		model,
		RoleProfiles{
			Planner: RoleProfile{Name: RolePlanner, SystemPrompt: "planner"},
		},
		[]langtools.Tool{
			&staticTool{name: "screenshot", output: `{"format":"jpeg","width":497,"height":1080,"size":10}`},
			NewHumanHandoffTool(),
		},
		nil,
		10,
		nil,
		handler,
		nil,
		ScreenshotPruningConfig{},
		nil,
	)

	result, err := executor.Call(context.Background(), map[string]any{
		"input":   "启动小红书 登录我的账号",
		"history": "",
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if output := strings.TrimSpace(fmt.Sprint(result["output"])); output != "请在手机上选择登录方式，完成后告诉我继续。" {
		t.Fatalf("output = %q", output)
	}
	if !toolCallsContain(handler.calls, toolHumanHandoffStep) {
		t.Fatalf("tool calls = %#v, want synthesized %s", handler.calls, toolHumanHandoffStep)
	}
	if model.callCount != 3 {
		t.Fatalf("model call count = %d, want 3", model.callCount)
	}
}

func TestDefaultFinalReviewPromptRequestsChineseForChineseInput(t *testing.T) {
	prompt := buildDefaultFinalReviewPrompt(
		map[string]string{"input": "打开小红书登录我的账号", "history": ""},
		roleLoopState{},
		"请问你想用哪种方式登录？",
	)
	if !strings.Contains(prompt, "Simplified Chinese, matching the latest user request") {
		t.Fatalf("prompt should request Simplified Chinese for Chinese input:\n%s", prompt)
	}
	if !strings.Contains(prompt, "handoff_details, suggested_action") {
		t.Fatalf("prompt should apply language rule to handoff fields:\n%s", prompt)
	}
}

func TestDefaultFinalReviewFailsClosedOnNonJSON(t *testing.T) {
	review := parseDefaultFinalReview("not json", "请问你想用哪种方式登录？")
	if review.CanFinish {
		t.Fatalf("CanFinish = true, want false")
	}
	if review.FinalAnswer != "" {
		t.Fatalf("FinalAnswer = %q, want empty", review.FinalAnswer)
	}
	if !strings.Contains(review.Reason, "non-JSON") {
		t.Fatalf("Reason = %q, want non-JSON failure reason", review.Reason)
	}
}

func TestHumanHandoffPauseUsesSuggestedActionWithoutToolContent(t *testing.T) {
	action := schema.AgentAction{
		Tool:      toolHumanHandoffStep,
		ToolInput: `{"reason":"login_method_selection","details":"当前需要选择登录方式。","suggested_action":"请在手机上选择登录方式，完成后告诉我继续。"}`,
	}
	step := &schema.AgentStep{
		Action:      action,
		Observation: `{"status":"HUMAN_HANDOFF_REQUESTED","reason":"login_method_selection","details":"当前需要选择登录方式。","suggested_action":"请在手机上选择登录方式，完成后告诉我继续。"}`,
	}

	if got := runPausingToolFinalAnswer(step); got != "请在手机上选择登录方式，完成后告诉我继续。" {
		t.Fatalf("final answer = %q", got)
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

func TestPlanModeAllowsHumanHandoffBeforeCommit(t *testing.T) {
	payload := `{"reason":"authentication","details":"Login screen requires the user's password","suggested_action":"Please enter the password on the phone and tell me when finished."}`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("handoff_1", toolHumanHandoffStep, payload),
	}}
	tool := NewHumanHandoffTool()
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
	toolSpecs := toolSpecsForRole(RolePlanner, executor.Tools)
	inputs := map[string]string{"input": "登录当前页面", "history": ""}

	turn, err := executor.callPlannerTurn(context.Background(), inputs, state, toolSpecs)
	if err != nil {
		t.Fatalf("planner turn error: %v", err)
	}
	if turn.Kind != plannerTurnSleep || turn.Step == nil {
		t.Fatalf("turn = %#v, want handoff planner tool that pauses the run", turn)
	}
	if turn.Step.Action.Tool != toolHumanHandoffStep {
		t.Fatalf("tool = %q, want %q", turn.Step.Action.Tool, toolHumanHandoffStep)
	}
	if !strings.Contains(turn.Step.Observation, "HUMAN_HANDOFF_REQUESTED") {
		t.Fatalf("observation = %q", turn.Step.Observation)
	}
	if state.Phase != phasePlan || !state.PlanCommitRequired {
		t.Fatalf("state = %#v, want plan phase with commit still required", state)
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
	prompt := buildPlannerStatePrompt(inputs, roleLoopState{}, "Planner task.")

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
	if !strings.Contains(prompt, "Original user request") {
		t.Fatalf("planner prompt should still include the original request as context:\n%s", prompt)
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
