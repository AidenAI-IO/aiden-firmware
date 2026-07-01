package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestCommitPlanConsecutiveFailuresStopRetryLoop(t *testing.T) {
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
	action := schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: `{"plan":`,
	}

	for i := 1; i < maxConsecutiveCommitPlanFailures; i++ {
		turn := executor.handlePlannerMetaTool(phasePlan, state, action)
		if turn.Kind != plannerTurnInvalidMeta {
			t.Fatalf("failure %d turn kind = %v, want invalid meta", i, turn.Kind)
		}
		if state.ConsecutiveCommitPlanFailures != i {
			t.Fatalf("failure count = %d, want %d", state.ConsecutiveCommitPlanFailures, i)
		}
	}

	turn := executor.handlePlannerMetaTool(phasePlan, state, action)
	if turn.Kind != plannerTurnFinish {
		t.Fatalf("final turn kind = %v, want finish", turn.Kind)
	}
	if !strings.Contains(turn.Answer, "规划提交连续失败") {
		t.Fatalf("answer = %q", turn.Answer)
	}
	if state.Phase != phaseDefault || state.PlanCommitRequired {
		t.Fatalf("state = %#v, want default with commit no longer required", state)
	}
}

func TestCommitPlanFailureObservationIncludesStructuredRepairHint(t *testing.T) {
	observation := commitPlanFailureObservation(newPlanValidationError(
		planErrArtifactPreparedAfterTargetOpen,
		`artifact contract violation: target_text artifact "message" must be prepared before target_open_step`,
		targetTextArtifactContractHint(),
	))
	if !strings.HasPrefix(observation, "commit_plan failed: ") {
		t.Fatalf("observation = %q, want commit_plan failed prefix", observation)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(observation, "commit_plan failed: ")), &payload); err != nil {
		t.Fatalf("decode failure payload: %v; observation=%q", err, observation)
	}
	if payload["code"] != planErrArtifactPreparedAfterTargetOpen {
		t.Fatalf("code = %#v, want %q", payload["code"], planErrArtifactPreparedAfterTargetOpen)
	}
	hint, ok := payload["repair_hint"].(map[string]any)
	if !ok || hint["artifact_kind"] != planArtifactKindTargetText {
		t.Fatalf("repair_hint = %#v, want target_text hint", payload["repair_hint"])
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

func TestCommitPlanDoesNotRejectByPhoneWorkflowText(t *testing.T) {
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
	state.World.UpdateDeviceEnvironment(&PhoneEnvironment{Platform: "ios"})
	payload, _ := json.Marshal(map[string]any{
		"objective":           "look up contact data and send a status message in the target chat app",
		"completion_criteria": []string{"message is sent with screenshot evidence"},
		"plan": []string{
			"query the contact data",
			"open the target chat app and find the recipient chat",
			"write the target text to clipboard, paste it into the input field, and send it",
		},
		"reason": "needs phone workflow",
	})

	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: string(payload),
	})

	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v, want commit without text-keyword policy", turn.Kind)
	}
	if state.Phase != phaseExecution || state.PlanCommitRequired {
		t.Fatalf("state = %#v, want committed plan", state)
	}
}

func TestCommitPlanAcceptsBatchedIOSPhoneBridgePlan(t *testing.T) {
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
	state.World.UpdateDeviceEnvironment(&PhoneEnvironment{Platform: "ios"})
	payload, _ := json.Marshal(map[string]any{
		"objective":           "look up contact data and send a status message in the target chat app",
		"completion_criteria": []string{"message is sent with screenshot evidence"},
		"plan": []string{
			"query contact data, compose the final message, and write it to clipboard",
			"open the target chat app and find the recipient chat",
			"focus the input field, paste the prepared clipboard text, verify, send, and capture evidence",
		},
		"sources": []map[string]any{{
			"id":          "contact_lookup",
			"tool":        "contacts",
			"action":      "query",
			"step":        1,
			"query":       "Example Contact",
			"produces":    []string{"phone_numbers"},
			"artifact_id": "chat_message",
		}},
		"artifacts": []map[string]any{{
			"id":               "chat_message",
			"kind":             "target_text",
			"delivery":         "clipboard",
			"prepare_step":     1,
			"target_open_step": 2,
			"consume_step":     3,
			"text_template":    "Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			"source_refs":      []string{"contact_lookup.phone_numbers"},
			"target_app":       "WeChat",
			"target_label":     "chat recipient",
		}},
		"reason": "batch Aiden app work first",
	})

	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: string(payload),
	})

	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v, want commit", turn.Kind)
	}
	if state.Phase != phaseExecution || state.NextStep != "query contact data, compose the final message, and write it to clipboard" {
		t.Fatalf("state = %#v, want execution with batched first step", state)
	}
}

func TestCommitPlanAcceptsFixedTargetTextArtifact(t *testing.T) {
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
	payload, _ := json.Marshal(map[string]any{
		"objective":           "prepare fixed target-app message",
		"completion_criteria": []string{"message is sent"},
		"plan": []string{
			"write the fixed message to clipboard while Aiden is foreground",
			"open the target chat and paste the prepared message",
		},
		"artifacts": []map[string]any{{
			"id":               "fixed_message",
			"kind":             "target_text",
			"delivery":         "clipboard",
			"prepare_step":     1,
			"target_open_step": 2,
			"consume_step":     2,
			"text_template":    "Please confirm whether this number is still active.",
			"target_app":       "WeChat",
			"target_label":     "Sample Recipient",
		}},
		"reason": "fixed text still needs target-preserving clipboard delivery",
	})

	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: string(payload),
	})

	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v, want commit; step=%#v", turn.Kind, turn.Step)
	}
	if len(state.PlanArtifacts) != 1 || state.PlanArtifacts[0].TextTemplate != "Please confirm whether this number is still active." {
		t.Fatalf("plan artifacts = %#v", state.PlanArtifacts)
	}
}

func TestCommitPlanParsesArtifactsEncodedAsJSONString(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"prepare encoded artifacts",
		"plan":["prepare message","paste message"],
		"artifacts":"[{\"id\":\"message\",\"kind\":\"target_text\",\"delivery\":\"clipboard\",\"prepare_step\":1,\"target_open_step\":2,\"consume_step\":2,\"text_template\":\"hello\",\"target_app\":\"QQ\"}]"
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Artifacts) != 1 || decision.Artifacts[0].ID != "message" {
		t.Fatalf("artifacts = %#v", decision.Artifacts)
	}
	if err := validateCommittedPlanPolicy(decision, worldState{}); err != nil {
		t.Fatalf("validateCommittedPlanPolicy() error = %v", err)
	}
}

func TestPlanArtifactsSchemaRequiresTargetTextBranchFields(t *testing.T) {
	schema := planArtifactsArgSchema()
	items, ok := schema["items"].(map[string]any)
	if !ok {
		t.Fatalf("items = %#v, want object schema", schema["items"])
	}
	rawBranches, ok := items["oneOf"].([]map[string]any)
	if !ok || len(rawBranches) != 2 {
		t.Fatalf("oneOf = %#v, want two artifact branches", items["oneOf"])
	}
	var targetBranch map[string]any
	for _, branch := range rawBranches {
		properties, _ := branch["properties"].(map[string]any)
		kind, _ := properties["kind"].(map[string]any)
		values, _ := kind["enum"].([]string)
		if len(values) == 1 && values[0] == planArtifactKindTargetText {
			targetBranch = branch
			break
		}
	}
	if targetBranch == nil {
		t.Fatalf("target_text branch not found in %#v", rawBranches)
	}
	required, ok := targetBranch["required"].([]string)
	if !ok {
		t.Fatalf("target_text required = %#v, want string slice", targetBranch["required"])
	}
	for _, field := range []string{"consume_step", "text_template", "target_app"} {
		if !roleLoopTestStringSliceContains(required, field) {
			t.Fatalf("target_text required = %#v, missing %q", required, field)
		}
	}
}

func TestCommitPlanRejectsUnknownArtifactFields(t *testing.T) {
	_, err := parseCommitPlanInput(`{
		"objective":"reject unknown artifact fields",
		"plan":["prepare","open target","consume"],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":1,
			"target_open_step":2,
			"consume_step":3,
			"text_template":"hello",
			"target_app":"WeChat",
			"artifact_id":"wrong_place"
		}]
	}`)
	if err == nil || !strings.Contains(err.Error(), `unknown field "artifact_id"`) {
		t.Fatalf("parseCommitPlanInput error = %v, want unknown artifact field rejection", err)
	}
}

func TestCommitPlanMovesMisplacedSourcesOutOfArtifacts(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"source accidentally placed in artifacts",
		"plan":["query source","open target","prepare text","send"],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":3,
			"target_open_step":3,
			"consume_step":4,
			"text_template":"hello",
			"target_app":"WeChat"
		},{
			"id":"contact_lookup",
			"tool":"contacts",
			"action":"query",
			"step":1,
			"query":"Example Contact",
			"produces":["phone_numbers"],
			"artifact_id":"message_text"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Artifacts) != 1 || decision.Artifacts[0].ID != "message_text" {
		t.Fatalf("artifacts = %#v, want only message_text artifact", decision.Artifacts)
	}
	if len(decision.Sources) != 1 || decision.Sources[0].ID != "contact_lookup" {
		t.Fatalf("sources = %#v, want moved contact_lookup source", decision.Sources)
	}
}

func TestCommitPlanAcceptsLatestLogMisplacedSourceRepairPayload(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"artifacts": [{
			"consume_step": 4,
			"delivery": "clipboard",
			"id": "chat_message",
			"kind": "target_text",
			"prepare_step": 3,
			"target_app": "WeChat",
			"target_label": "Target Friend",
			"target_open_step": 3,
			"text_template": "Please confirm whether this number is still active."
		}, {
			"action": "query",
			"artifact_id": "chat_message",
			"id": "contact_lookup",
			"produces": ["phone_numbers"],
			"query": "Example Contact",
			"step": 1,
			"tool": "contacts"
		}],
		"completion_criteria": ["contact data is available", "target chat is open", "message is sent"],
		"objective": "look up contact data, then send a message to Target Friend in WeChat.",
		"plan": ["query Example Contact with the contacts tool", "prepare the final message text", "open WeChat and find Target Friend", "paste the prepared message into the chat input and send"],
		"reason": "the task crosses app-side data preparation and target-app UI automation."
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Artifacts) != 1 || decision.Artifacts[0].ID != "chat_message" {
		t.Fatalf("artifacts = %#v, want chat_message only", decision.Artifacts)
	}
	if len(decision.Sources) != 1 || decision.Sources[0].ID != "contact_lookup" || decision.Sources[0].Query != "Example Contact" {
		t.Fatalf("sources = %#v, want moved contact_lookup source", decision.Sources)
	}
}

func TestCommitPlanInfersSingleContactsTargetTextWorkflowContract(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"look up contact data and send a chat message",
		"plan":["query Example Contact","prepare message","open WeChat","send"],
		"sources":[{
			"id":"contact_lookup",
			"tool":"contacts",
			"action":"query",
			"step":1,
			"query":"Example Contact",
			"produces":["phone_numbers"]
		}],
		"artifacts":[{
			"id":"chat_message",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":2,
			"target_open_step":3,
			"text_template":"Please confirm whether this number is still active.",
			"source_refs":[],
			"target_app":"WeChat",
			"target_label":"Target Friend chat input"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Sources) != 1 || decision.Sources[0].ArtifactID != "chat_message" {
		t.Fatalf("sources = %#v, want source linked to artifact", decision.Sources)
	}
	if len(decision.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v, want one artifact", decision.Artifacts)
	}
	artifact := decision.Artifacts[0]
	if artifact.ConsumeStep != 3 {
		t.Fatalf("consume_step = %d, want inferred 3", artifact.ConsumeStep)
	}
	if !roleLoopTestStringSliceContains(artifact.SourceRefs, "contact_lookup.phone_numbers") {
		t.Fatalf("source_refs = %#v, want inferred contact_lookup.phone_numbers", artifact.SourceRefs)
	}
	if err := validateCommittedPlanPolicy(decision, worldState{}); err != nil {
		t.Fatalf("validateCommittedPlanPolicy() error = %v", err)
	}
}

func TestCommitPlanNormalizesGenericSourceRefForSingleSourceWorkflow(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"look up contact data and send a chat message",
		"plan":["query Example Contact","prepare message","open WeChat"],
		"sources":[{
			"id":"contact_lookup",
			"tool":"contacts",
			"action":"query",
			"step":1,
			"query":"Example Contact"
		}],
		"artifacts":[{
			"id":"chat_message",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":2,
			"consume_step":3,
			"text_template":"Please confirm whether {{source}} is still active.",
			"source_refs":["source"],
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if refs := decision.Artifacts[0].SourceRefs; !roleLoopTestStringSliceContains(refs, "contact_lookup.phone_numbers") {
		t.Fatalf("source_refs = %#v, want generic source normalized", refs)
	}
}

func TestCommitPlanRejectsUnlinkedContactsSourceWhenWorkflowAmbiguous(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"look up multiple contacts before a chat message",
		"plan":["query contacts","prepare message","open WeChat"],
		"sources":[
			{"id":"contact_a","tool":"contacts","action":"query","step":1,"query":"Contact A"},
			{"id":"contact_b","tool":"contacts","action":"query","step":1,"query":"Contact B"}
		],
		"artifacts":[{
			"id":"chat_message",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":2,
			"consume_step":3,
			"text_template":"Please confirm whether the number is still active.",
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	err = validateCommittedPlanPolicy(decision, worldState{})
	if err == nil || !strings.Contains(err.Error(), "must be linked to a target_text artifact") {
		t.Fatalf("validateCommittedPlanPolicy() error = %v, want unlinked source rejection", err)
	}
}

func TestCommittedPlanClipboardWriteRequiresDeclaredArtifact(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		Plan:          []string{"prepare data", "use target app"},
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","text":"final text"}`,
	}

	result, allowed := state.beforeArtifactToolCall(context.Background(), call)
	if allowed || result.Error == nil || !strings.Contains(result.Output, "declared by commit_plan artifacts") {
		t.Fatalf("clipboard without artifact allowed=%v result=%#v, want artifact rejection", allowed, result)
	}
}

func TestClipboardPayloadCannotPrepareBeforeLaterPlanSteps(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		Plan:          []string{"prepare data", "use target app"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:          "clipboard_text",
			Kind:        planArtifactKindClipboardPayload,
			Delivery:    planArtifactDeliveryClipboard,
			PrepareStep: 1,
		}}),
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"clipboard_text","text":"final text"}`,
	}

	result, allowed := state.beforeArtifactToolCall(context.Background(), call)
	if allowed || result.Error == nil || !strings.Contains(result.Output, "use target_text with a future consume_step") {
		t.Fatalf("clipboard_payload allowed=%v result=%#v, want cross-step target_text rejection", allowed, result)
	}
}

func TestPlanArtifactContactsSourceBlocksContactsUIAndTargetApp(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		Plan:          []string{"prepare data", "open target app"},
		PlanSources: initialPlanSourceStates([]planSource{{
			ID:         "contact_lookup",
			Tool:       planSourceToolContacts,
			Action:     planSourceActionQuery,
			Step:       1,
			Query:      "Example Contact",
			Produces:   []string{"phone_numbers"},
			ArtifactID: "message_text",
		}}),
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Message {{value}}",
			SourceRefs:     []string{"contact_lookup.phone_numbers"},
			TargetApp:      "WeChat",
		}}),
	}

	sourceCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: `{"app":"Contacts"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), sourceCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "do not open Contacts UI") {
		t.Fatalf("source app navigation allowed=%v result=%#v, want contacts UI rejection", allowed, result)
	}

	targetCall := ToolCall{
		Spec:  ToolSpec{Name: "search_launch_app"},
		Input: `{"app":"WeChat"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), targetCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "target-app navigation") {
		t.Fatalf("target app navigation allowed=%v result=%#v, want blocked", allowed, result)
	}

	rawSourceCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: "Contacts",
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), rawSourceCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "do not open Contacts UI") {
		t.Fatalf("raw source app navigation allowed=%v result=%#v, want contacts UI rejection", allowed, result)
	}

	rawTargetCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: "WeChat",
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), rawTargetCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "target-app navigation") {
		t.Fatalf("raw target app navigation allowed=%v result=%#v, want blocked", allowed, result)
	}

	contactsCall := ToolCall{
		Spec:  ToolSpec{Name: "contacts"},
		Input: `{"action":"query","query":"Example Contact","limit":20}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), contactsCall); !allowed || result.Error != nil {
		t.Fatalf("contacts query allowed=%v result=%#v, want allowed", allowed, result)
	}
	state.StepExecutionResults = append(state.StepExecutionResults, roleExecutionResult{
		Action: &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact","limit":20}`},
		Step:   &schema.AgentStep{Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["5550103"]}]}`},
	})

	unrelatedContactsCall := ToolCall{
		Spec:  ToolSpec{Name: "contacts"},
		Input: `{"action":"query","query":"Other Contact","limit":20}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), unrelatedContactsCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "declared source contract") {
		t.Fatalf("unrelated contacts query allowed=%v result=%#v, want declared-source rejection", allowed, result)
	}
}

func TestPlanArtifactBlocksTargetAppOpenWhenPreOpenArtifactMissing(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"prepare text", "open target app", "send"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    3,
			TextTemplate:   "Message",
			TargetApp:      "WeChat",
		}}),
	}
	targetCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: `{"app":"WeChat"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), targetCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "pre-open app contract") {
		t.Fatalf("target app open allowed=%v result=%#v, want pre-open artifact rejection", allowed, result)
	}

	state.PlanArtifacts[0].PreparedText = "Message"
	if result, allowed := state.beforeArtifactToolCall(context.Background(), targetCall); !allowed || result.Error != nil {
		t.Fatalf("target app open with prepared text allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestTargetTextClipboardCanPrepareBeforeDeclaredPrepareStepWhenOpenAppBlocked(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"query source", "open target app", "prepare text", "send"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    3,
			TargetOpenStep: 2,
			ConsumeStep:    4,
			TextTemplate:   "Message",
			TargetApp:      "WeChat",
		}}),
	}
	clipCall := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"message_text","text":"Message"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), clipCall); !allowed || result.Error != nil {
		t.Fatalf("early target_text clipboard prepare allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestPreparedTargetTextCanBeConsumedAfterTargetAppOpenedBeforeConsumeStep(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"query source", "prepare message", "open target", "paste and send"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    3,
			TargetOpenStep: 3,
			ConsumeStep:    4,
			TextTemplate:   "Message",
			TargetApp:      "WeChat",
		}}),
		StepExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "clipboard", ToolInput: `{"action":"write","artifact_id":"message_text","text":"Message"}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true}`},
		}, {
			Action: &schema.AgentAction{Tool: "open_app", ToolInput: `{"app":"WeChat"}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true}`},
		}},
	}
	state.PlanArtifacts[0].PreparedText = "Message"
	state.PlanArtifacts[0].PreparedAt = time.Now()

	call := ToolCall{
		Spec:  ToolSpec{Name: "enter_text_via_bridge"},
		Input: `{"artifact_id":"message_text","text":"Message","focus":{"x":500,"y":950,"coord_space":"normalized"},"send_after_commit":true}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("prepared artifact consumption allowed=%v result=%#v, want allowed after target navigation", allowed, result)
	}
}

func TestPhoneBridgeAppSideToolsBlockedAfterTargetAppNavigation(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"open target app", "late app-side work"},
		ExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "open_app", ToolInput: `{"app":"WeChat"}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true}`},
		}},
	}
	for _, toolName := range phoneBridgeAppForegroundToolNames() {
		call := ToolCall{
			Spec:  ToolSpec{Name: toolName},
			Input: `{}`,
		}
		if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil ||
			!strings.Contains(result.Output, "must run before target-app navigation") ||
			!strings.Contains(result.Output, "clipboard, calendar, contacts, notification") {
			t.Fatalf("%s after target navigation allowed=%v result=%#v, want foreground-boundary rejection", toolName, allowed, result)
		}
	}
}

func TestExecutorAutoAbortsRepeatedPhoneBridgeForegroundFailure(t *testing.T) {
	state := &roleLoopState{
		StepExecutionResults: []roleExecutionResult{{
			Action:    &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact","limit":5}`},
			Step:      &schema.AgentStep{Observation: "phone bridge did not return to foreground within 8s"},
			ToolError: NewToolError(CodeAppBackgrounded, "phone bridge did not return to foreground within 8s"),
		}},
	}
	action := schema.AgentAction{
		Tool:      "contacts",
		ToolInput: `{"action":"query","query":"Example Contact","limit":5}`,
	}
	abortAction, ok := state.autoAbortRepeatedPhoneBridgeForegroundFailure(action)
	if !ok {
		t.Fatal("autoAbortRepeatedPhoneBridgeForegroundFailure() ok=false, want abort")
	}
	if abortAction.Tool != toolAbortStep {
		t.Fatalf("abort tool = %q, want %q", abortAction.Tool, toolAbortStep)
	}
	if !strings.Contains(abortAction.ToolInput, "already failed in this step") ||
		!strings.Contains(abortAction.ToolInput, "Aiden stayed backgrounded") {
		t.Fatalf("abort input = %s, want foreground failure reason", abortAction.ToolInput)
	}
}

func TestPlanArtifactDoesNotInferContactsSourceFromStepText(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		Plan:          []string{"query contact data and prepare message text", "open target app"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Message {{value}}",
			TargetApp:      "WeChat",
		}}),
	}
	sourceCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: `{"app":"Contacts"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), sourceCall); !allowed || result.Error != nil {
		t.Fatalf("source app navigation allowed=%v result=%#v, want allowed without explicit source contract", allowed, result)
	}
}

func TestPlanArtifactAppLabelMatchingPreservesAppNames(t *testing.T) {
	if got := normalizeArtifactAppLabel("WhatsApp"); got != "whatsapp" {
		t.Fatalf("normalizeArtifactAppLabel(WhatsApp) = %q, want whatsapp", got)
	}
	if got := normalizeArtifactAppLabel("WeChat App"); got != "wechat" {
		t.Fatalf("normalizeArtifactAppLabel(WeChat App) = %q, want wechat", got)
	}
	if got := normalizeArtifactAppLabel("微信app"); got != "wechat" {
		t.Fatalf("normalizeArtifactAppLabel(微信app) = %q, want wechat", got)
	}
	if !appLabelsMatch("微信/WeChat", "weixin") {
		t.Fatal("expected bilingual app label to match known alias")
	}
	if appLabelsMatch("WhatsApp", "Whats") {
		t.Fatal("unexpected match after app suffix normalization")
	}
}

func TestCommitPlanRejectsTargetTextArtifactWithoutTargetApp(t *testing.T) {
	_, err := parseCommitPlanInput(`{
		"objective":"prepare cross app text",
		"plan":["prepare text","open target"],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":1,
			"target_open_step":2,
			"consume_step":2,
			"text_template":"Message {{value}}"
		}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "requires target_app") {
		t.Fatalf("parseCommitPlanInput error = %v, want target_app validation", err)
	}
}

func TestCommitPlanAcceptsTargetTextArtifactWithoutTargetOpenStep(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"prepare cross app text",
		"plan":["prepare text","open target"],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":1,
			"consume_step":2,
			"text_template":"Message {{value}}",
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Artifacts) != 1 || decision.Artifacts[0].TargetOpenStep != 0 {
		t.Fatalf("artifacts = %#v, want target_open_step omitted", decision.Artifacts)
	}
}

func TestCommitPlanAllowsTargetTextPreparedAfterDeclaredTargetOpenBecauseRuntimeBlocksActualOpen(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"prepare target text too late",
		"plan":["query source","open target","prepare clipboard","consume text"],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":3,
			"target_open_step":2,
			"consume_step":4,
			"text_template":"fixed message",
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if err := validateCommittedPlanPolicy(decision, worldState{}); err != nil {
		t.Fatalf("validateCommittedPlanPolicy() error = %v", err)
	}
}

func TestCommitPlanAllowsSourceAfterDeclaredTargetOpenBecauseRuntimeBlocksActualOpen(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"query source after target app",
		"plan":["prepare shell","open target","query contacts","consume text"],
		"sources":[{
			"id":"contact_lookup",
			"tool":"contacts",
			"action":"query",
			"step":3,
			"query":"Example Contact",
			"artifact_id":"message_text"
		}],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":3,
			"target_open_step":2,
			"consume_step":4,
			"text_template":"Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			"source_refs":["contact_lookup.phone_numbers"],
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Sources) != 1 || decision.Sources[0].Step != 3 {
		t.Fatalf("sources = %#v, want source retained for runtime gating", decision.Sources)
	}
}

func TestCommitPlanRejectsPlaceholderOnlyTargetTextTemplate(t *testing.T) {
	decision, err := parseCommitPlanInput(`{
		"objective":"query contact data and prepare target-app text",
		"plan":["query source","prepare clipboard","open target","consume text"],
		"sources":[{
			"id":"contact_lookup",
			"tool":"contacts",
			"action":"query",
			"step":1,
			"query":"Example Contact",
			"artifact_id":"message_text"
		}],
		"artifacts":[{
			"id":"message_text",
			"kind":"target_text",
			"delivery":"clipboard",
			"prepare_step":2,
			"target_open_step":3,
			"consume_step":4,
			"text_template":"{{contact_lookup.phone_numbers}}",
			"source_refs":["contact_lookup.phone_numbers"],
			"target_app":"WeChat"
		}]
	}`)
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	err = validateCommittedPlanPolicy(decision, worldState{})
	if err == nil || !strings.Contains(err.Error(), "text_template cannot be only placeholder") {
		t.Fatalf("validateCommittedPlanPolicy() error = %v, want placeholder-only rejection", err)
	}
}

func TestCommitPlanAllowsAndroidTargetPreservingClipboardPlan(t *testing.T) {
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
	state.World.UpdateDeviceEnvironment(&PhoneEnvironment{Platform: "android"})
	payload, _ := json.Marshal(map[string]any{
		"objective":           "look up contact data and send a status message in the target chat app",
		"completion_criteria": []string{"message is sent with screenshot evidence"},
		"plan": []string{
			"query contact data",
			"open the target chat app and find the recipient chat",
			"use target-preserving clipboard write and paste, verify the field, then send",
		},
		"reason": "android can preserve target app",
	})

	turn := executor.handlePlannerMetaTool(phasePlan, state, schema.AgentAction{
		Tool:      toolCommitPlan,
		ToolInput: string(payload),
	})

	if turn.Kind != plannerTurnCommitPlan {
		t.Fatalf("turn kind = %v, want commit", turn.Kind)
	}
}

func TestPlanArtifactClipboardWriteRequiresBindingAndTemplateMatch(t *testing.T) {
	state := &roleLoopState{
		PlanStepIndex: 0,
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Please check {{lookup.value}} now",
		}}),
	}

	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","text":"Please check 123 now"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("clipboard without artifact_id allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"action":"write","artifact_id":"message_text","text":"lookup value: 123"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "does not satisfy text_template") {
		t.Fatalf("clipboard with mismatched template allowed=%v result=%#v, want template rejection", allowed, result)
	}

	call.Input = `{"action":"write","artifact_id":"message_text","text":"Please check 123 now"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("clipboard with matching template allowed=%v result=%#v, want allowed", allowed, result)
	}
	state.afterArtifactToolCall(context.Background(), call, ToolResult{Output: `{"ok":true}`})
	if got := state.PlanArtifacts[0].PreparedText; got != "Please check 123 now" {
		t.Fatalf("prepared text = %q", got)
	}
}

func TestPlanArtifactClipboardWriteMatchesFixedTemplateExactly(t *testing.T) {
	state := &roleLoopState{
		PlanStepIndex: 0,
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Fixed message",
		}}),
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"message_text","text":"Fixed message plus extra"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "does not satisfy text_template") {
		t.Fatalf("clipboard with mismatched fixed template allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"action":"write","artifact_id":"message_text","text":"Fixed message"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("clipboard with fixed template allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestTargetTextClipboardDoesNotInferContactsValuesWithoutSourceContract(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"query contacts", "open target", "prepare text", "consume"},
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:           "wechat_message",
			Kind:         planArtifactKindTargetText,
			Delivery:     planArtifactDeliveryClipboard,
			PrepareStep:  3,
			ConsumeStep:  4,
			TextTemplate: "Target Friend, can you confirm the latest status for Example Contact?",
			TargetApp:    "WeChat",
			TargetLabel:  "Target Friend chat input",
		}}),
		ExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact","limit":10}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["555 0101","5550102"]}]}`},
		}},
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"wechat_message","text":"Target Friend, can you confirm the latest status for Example Contact?"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("clipboard without explicit source contract allowed=%v result=%#v, want no inferred contacts-value rejection", allowed, result)
	}
}

func TestTargetTextClipboardRequiresExplicitContactsPhoneValues(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		PlanSources: initialPlanSourceStates([]planSource{{
			ID:         "contact_lookup",
			Tool:       planSourceToolContacts,
			Action:     planSourceActionQuery,
			Step:       1,
			Query:      "Example Contact",
			ArtifactID: "wechat_message",
		}}),
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:           "wechat_message",
			Kind:         planArtifactKindTargetText,
			Delivery:     planArtifactDeliveryClipboard,
			PrepareStep:  1,
			ConsumeStep:  2,
			TextTemplate: "Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			SourceRefs:   []string{"contact_lookup.phone_numbers"},
			TargetApp:    "WeChat",
			TargetLabel:  "Target Friend chat input",
		}}),
		StepExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact","limit":10}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["555 0101"]}]}`},
		}},
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"wechat_message","text":"Please confirm whether this number is still active."}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "555 0101") {
		t.Fatalf("clipboard without explicit source number allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"action":"write","artifact_id":"wechat_message","text":"Please confirm whether 555 0101 is still active."}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("clipboard with explicit source number allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestContactsSourceContractRequiresContactsBeforeClipboardAndFinish(t *testing.T) {
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
		PlanCommitted:       true,
		PlanStepIndex:       0,
		StepExecutionActive: true,
		Plan:                []string{"prepare message from source", "open target app"},
		PlanSources: initialPlanSourceStates([]planSource{{
			ID:         "contact_lookup",
			Tool:       planSourceToolContacts,
			Action:     planSourceActionQuery,
			Step:       1,
			Query:      "Example Contact",
			Produces:   []string{"phone_numbers"},
			ArtifactID: "message_text",
		}}),
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			SourceRefs:     []string{"contact_lookup.phone_numbers"},
			TargetApp:      "WeChat",
		}}),
	}
	clipCall := ToolCall{
		Spec:  ToolSpec{Name: "clipboard"},
		Input: `{"action":"write","artifact_id":"message_text","text":"Please confirm whether 5550103 is still active."}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), clipCall); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "source contract") {
		t.Fatalf("clipboard before contacts allowed=%v result=%#v, want source rejection", allowed, result)
	}

	turn := executor.handleExecutorMetaTool(state, schema.AgentAction{
		Tool:      toolFinishStep,
		ToolInput: `{"summary":"prepared"}`,
	})
	if turn.Kind != executorTurnInvalidMeta || turn.InvalidMetaStep == nil ||
		!strings.Contains(turn.InvalidMetaStep.Observation, "unsatisfied source contract") {
		t.Fatalf("finish before contacts turn=%#v, want source rejection", turn)
	}

	state.StepExecutionResults = append(state.StepExecutionResults, roleExecutionResult{
		Action: &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact","limit":20}`},
		Step:   &schema.AgentStep{Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["5550103"]}]}`},
	})
	if result, allowed := state.beforeArtifactToolCall(context.Background(), clipCall); !allowed || result.Error != nil {
		t.Fatalf("clipboard after contacts allowed=%v result=%#v, want allowed", allowed, result)
	}
	state.afterArtifactToolCall(context.Background(), clipCall, ToolResult{Output: `{"ok":true}`})

	turn = executor.handleExecutorMetaTool(state, schema.AgentAction{
		Tool:      toolFinishStep,
		ToolInput: `{"summary":"prepared","key_info":["phone_numbers=5550103"]}`,
	})
	if turn.Kind != executorTurnFinishStep {
		t.Fatalf("finish after contacts turn=%#v, want finish", turn)
	}
}

func TestContactsSourceContractPersistsAcrossPlanSteps(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 1,
		Plan:          []string{"prepare message from source", "open target app", "send"},
		PlanSources: initialPlanSourceStates([]planSource{{
			ID:         "contact_lookup",
			Tool:       planSourceToolContacts,
			Action:     planSourceActionQuery,
			Step:       1,
			Query:      "Example Contact",
			ArtifactID: "message_text",
		}}),
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    3,
			TextTemplate:   "Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			SourceRefs:     []string{"contact_lookup.phone_numbers"},
			TargetApp:      "WeChat",
		}}),
		ExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Example Contact"}`},
			Step:   &schema.AgentStep{Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["5550103"]}]}`},
		}},
	}
	state.PlanArtifacts[0].PreparedText = "Please confirm whether 5550103 is still active."
	targetCall := ToolCall{
		Spec:  ToolSpec{Name: "open_app"},
		Input: `{"app":"WeChat"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), targetCall); !allowed || result.Error != nil {
		t.Fatalf("target app open after persisted source allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestPlanArtifactTextEntryConsumesPreparedArtifact(t *testing.T) {
	state := &roleLoopState{
		PlanStepIndex: 1,
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    2,
			TextTemplate:   "Please check {{lookup.value}} now",
		}}),
	}
	state.PlanArtifacts[0].PreparedText = "Please check 123 now"
	state.PlanArtifacts[0].PreparedAt = time.Now()

	call := ToolCall{
		Spec:  ToolSpec{Name: "enter_text_in_field"},
		Input: `{"text":"Please check 123 now"}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("text entry without artifact_id allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"text":"Target Friend","mode":"search"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("structured search text entry allowed=%v result=%#v, want allowed", allowed, result)
	}

	call.Input = `{"text":"Target Friend","mode":"search","send_after_commit":true}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("search text entry with send_after_commit allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"text":"Target Friend","mode":"form"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("form text entry without artifact_id allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Spec.Name = "enter_text_via_bridge"
	call.Input = `{"text":"Target Friend","mode":"search","focus":{"x":500,"y":200,"coord_space":"normalized"}}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("bridge structured search text entry allowed=%v result=%#v, want allowed", allowed, result)
	}

	call.Spec.Name = "enter_text_in_field"
	call.Input = `{"artifact_id":"message_text","text":"Different text"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil ||
		!strings.Contains(result.Output, "must exactly match prepared text") {
		t.Fatalf("text entry mismatch allowed=%v result=%#v, want rejection", allowed, result)
	}

	call.Input = `{"artifact_id":"message_text","text":"Please check 123 now"}`
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("text entry allowed=%v result=%#v, want allowed", allowed, result)
	}
	state.afterArtifactToolCall(context.Background(), call, ToolResult{Output: `{"ok":true,"committed":true}`})
	if state.PlanArtifacts[0].ConsumedAt.IsZero() {
		t.Fatal("expected artifact to be marked consumed")
	}
}

func TestPreparePhoneWorkflowCannotOpenBeforeCommittedBoundary(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:             "message_text",
			Kind:           planArtifactKindTargetText,
			Delivery:       planArtifactDeliveryClipboard,
			PrepareStep:    1,
			TargetOpenStep: 2,
			ConsumeStep:    3,
			TextTemplate:   "hello",
			TargetApp:      "WeChat",
		}}),
	}
	call := ToolCall{
		Spec:  ToolSpec{Name: "prepare_phone_app_workflow"},
		Input: `{"target_text":"hello","target_app":"WeChat","open_target_app":true}`,
	}
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("prepare workflow open before boundary allowed=%v result=%#v, want rejection", allowed, result)
	}

	state.PlanStepIndex = 1
	if result, allowed := state.beforeArtifactToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("prepare workflow open at boundary allowed=%v result=%#v, want allowed", allowed, result)
	}
}

func TestPreparePhoneWorkflowMarksSingleMatchingArtifactPrepared(t *testing.T) {
	state := &roleLoopState{
		PlanCommitted: true,
		PlanStepIndex: 0,
		PlanSources: initialPlanSourceStates([]planSource{{
			ID:         "contact_lookup",
			Tool:       planSourceToolContacts,
			Action:     planSourceActionQuery,
			Step:       1,
			Query:      "Example Contact",
			Produces:   []string{"phone_numbers"},
			ArtifactID: "fan_phone",
		}}),
		PlanArtifacts: initialPlanArtifactStates([]planArtifact{{
			ID:           "fan_phone",
			Kind:         planArtifactKindTargetText,
			Delivery:     planArtifactDeliveryClipboard,
			PrepareStep:  2,
			ConsumeStep:  4,
			SourceRefs:   []string{"contact_lookup.phone_numbers"},
			TextTemplate: "Please confirm whether {{contact_lookup.phone_numbers}} is still active.",
			TargetApp:    "WeChat",
			TargetLabel:  "Target Friend",
		}}),
		ExecutionResults: []roleExecutionResult{{
			Action: &schema.AgentAction{
				Tool:      "contacts",
				ToolInput: `{"action":"query","query":"Example Contact"}`,
			},
			Step: &schema.AgentStep{
				Observation: `{"ok":true,"contacts":[{"name":"Example Contact","phone_numbers":["555 0101","5550102"]}]}`,
			},
		}},
	}

	call := ToolCall{
		Spec:  ToolSpec{Name: "prepare_phone_app_workflow"},
		Input: `{"artifact_id":"fan_phone","target_text":"Please confirm whether 555 0101 and 5550102 are still active.","target_app":"WeChat","open_target_app":false}`,
	}
	result := ToolResult{Output: `{"ok":true,"workflow":"prepare_phone_app_workflow","target_app":"WeChat","target_text":"Please confirm whether 555 0101 and 5550102 are still active.","clipboard_prepared":true,"opened_target_app":false}`}

	state.afterArtifactToolCall(context.Background(), call, result)
	if got := state.PlanArtifacts[0].PreparedText; got != "Please confirm whether 555 0101 and 5550102 are still active." {
		t.Fatalf("prepared text = %q", got)
	}
	if state.PlanArtifacts[0].PreparedAt.IsZero() {
		t.Fatal("expected workflow-prepared artifact timestamp")
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

func TestCommitPlanParsesPlanEncodedAsJSONStringArray(t *testing.T) {
	encodedPlan, err := json.Marshal([]string{
		"Use the contacts tool to query Example Contact and collect all phone numbers.",
		"Compose the final message text from the contact data and write it to clipboard.",
		"Use open_app to open WeChat.",
		`Wait for WeChat to load, then search for "Target Friend" and open the chat.`,
		"Use enter_text_in_field to paste the prepared clipboard text and send the message.",
		"Wait for send completion and confirm the sent message appears in the chat.",
	})
	if err != nil {
		t.Fatalf("marshal encoded plan: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"objective": "Query Example Contact's phone numbers, then message \"Target Friend\" in WeChat to confirm whether the numbers are still active.",
		"completion_criteria": []string{
			"Example Contact phone numbers are returned by the contacts tool",
			"WeChat is open and the Target Friend chat is visible",
			"The message is entered, sent, and visible as an outgoing message",
		},
		"plan": string(encodedPlan),
		"sources": []map[string]any{{
			"id":          "fan_phone_lookup",
			"tool":        "contacts",
			"action":      "query",
			"query":       "Example Contact",
			"produces":    []string{"phone_numbers"},
			"step":        1,
			"artifact_id": "fan_phone_message",
		}},
		"artifacts": []map[string]any{{
			"id":               "fan_phone_message",
			"kind":             "target_text",
			"delivery":         "clipboard",
			"prepare_step":     2,
			"consume_step":     5,
			"source_refs":      []string{"fan_phone_lookup.phone_numbers"},
			"text_template":    "Example Contact numbers are {{fan_phone_lookup.phone_numbers}}. Are these numbers still active?",
			"target_app":       "WeChat",
			"target_label":     "Target Friend",
			"target_open_step": 3,
		}},
		"reason": "The workflow needs app-side contact data before opening WeChat.",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	decision, err := parseCommitPlanInput(string(payload))
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Plan) != 6 {
		t.Fatalf("plan = %#v, want 6 parsed steps", decision.Plan)
	}
	if !strings.Contains(decision.Plan[3], `"Target Friend"`) {
		t.Fatalf("quoted chat label not preserved in plan: %#v", decision.Plan)
	}
	if err := validateCommittedPlanPolicy(decision, worldState{}); err != nil {
		t.Fatalf("validateCommittedPlanPolicy() error = %v", err)
	}
}

func TestCommitPlanParsesLogPlanStringArrayWithUnescapedInnerQuotes(t *testing.T) {
	planText := `
["Step 1: Use contacts to query "Example Contact" and collect all phone numbers.", "Step 2: Compose the final message and write it to clipboard.", "Step 3: Use open_app to open WeChat and wait for it to load.", "Step 4: Search for or find the "Target Friend" chat and open it.", "Step 5: Use enter_text_in_field to paste the prepared message, then send it.", "Step 6: Verify with a screenshot that the message was sent."]
`
	payload, err := json.Marshal(map[string]any{
		"objective": "Query Example Contact's phone numbers, then message \"Target Friend\" in WeChat to confirm whether the numbers are still active.",
		"plan":      planText,
		"sources": []map[string]any{{
			"id":          "fan_phone_lookup",
			"tool":        "contacts",
			"action":      "query",
			"step":        1,
			"query":       "Example Contact",
			"produces":    []string{"phone_numbers"},
			"artifact_id": "message_to_send",
		}},
		"artifacts": []map[string]any{{
			"id":            "message_to_send",
			"kind":          "target_text",
			"delivery":      "clipboard",
			"prepare_step":  2,
			"consume_step":  5,
			"text_template": "Example Contact numbers are {{fan_phone_lookup.phone_numbers}}. Are these numbers still active?",
			"target_app":    "WeChat",
			"target_label":  "Target Friend",
			"source_refs":   []string{"fan_phone_lookup.phone_numbers"},
		}},
		"reason": "repro latest board log",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	decision, err := parseCommitPlanInput(string(payload))
	if err != nil {
		t.Fatalf("parseCommitPlanInput() error = %v", err)
	}
	if len(decision.Plan) != 6 {
		t.Fatalf("plan = %#v, want 6 parsed steps", decision.Plan)
	}
	if !strings.Contains(decision.Plan[0], `"Example Contact"`) || !strings.Contains(decision.Plan[3], `"Target Friend"`) {
		t.Fatalf("inner quotes not preserved in plan: %#v", decision.Plan)
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

func roleLoopTestStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	if !strings.HasPrefix(content, "note:") || !strings.Contains(content, "tool result omitted") || !strings.Contains(content, "would exceed the model context window") || !strings.Contains(content, "tool call already completed") {
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
		"World State",
		"Original user request / root request:",
		"Current todo state:",
		"Todo reminder:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("force_simple_loop planner runtime context missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{
		"Planner task.",
		"Loop mode:",
		"force_simple_loop: true",
		"Latest user message:",
		"Session context view:",
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
