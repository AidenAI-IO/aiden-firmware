package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type roleCollaborativeExecutor struct {
	Model             llms.Model
	Profiles          RoleProfiles
	Tools             []langtools.Tool
	Memory            schema.Memory
	CallbacksHandler  callbacks.Handler
	MaxIterations     int
	InputAttachments  []InputAttachment
	OutputKey         string
	Recorder          *EpisodeRecorder
	ScreenshotPruning ScreenshotPruningConfig
}

const roleModelCallTimeout = 120 * time.Second

type plannerDecision struct {
	Objective          string             `json:"objective,omitempty"`
	CompletionCriteria []string           `json:"completion_criteria,omitempty"`
	Plan               []string           `json:"plan"`
	NextStep           string             `json:"next_step"`
	Reason             string             `json:"reason,omitempty"`
	ObservedState      observedWorldState `json:"observed_state,omitempty"`
}

type verifierDecision struct {
	CanFinish     bool               `json:"can_finish"`
	FinalAnswer   string             `json:"final_answer,omitempty"`
	NeedsReplan   bool               `json:"needs_replan,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	ObservedState observedWorldState `json:"observed_state,omitempty"`
}

type roleExecutionResult struct {
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	CandidateAnswer string
}

type roleLoopState struct {
	Objective          string
	CompletionCriteria []string
	Plan               []string
	NextStep           string
	PlannerReason      string
	World              worldState
	ToolSteps          []schema.AgentStep
	ExecutionResults   []roleExecutionResult
	VerifierResults    []verifierDecision
}

type worldState struct {
	LatestScreenshot *worldScreenshot
	Observation      *worldStateObservation
}

type worldScreenshot struct {
	SourceTool   string
	ToolInput    string
	ActionOutput string
	Width        int
	Height       int
	Format       string
	Size         int
	Data         []byte
	StepNumber   int
}

type observedWorldState struct {
	AppName     string   `json:"app_name,omitempty" yaml:"app_name,omitempty"`
	PageName    string   `json:"page_name,omitempty" yaml:"page_name,omitempty"`
	Platform    string   `json:"platform,omitempty" yaml:"platform,omitempty"`
	VisibleText []string `json:"visible_text,omitempty" yaml:"visible_text,omitempty"`
	Dialogs     []string `json:"dialogs,omitempty" yaml:"dialogs,omitempty"`
	Confidence  float64  `json:"confidence,omitempty" yaml:"confidence,omitempty"`
}

type worldStateObservation struct {
	observedWorldState
	SourceRole     RoleName
	ObservedAt     time.Time
	ScreenshotStep int
}

var _ chains.Chain = (*roleCollaborativeExecutor)(nil)
var _ callbacks.HandlerHaver = (*roleCollaborativeExecutor)(nil)

type roleOutputHandler interface {
	HandleRoleOutput(ctx context.Context, role, content string)
}

func newRoleCollaborativeExecutor(
	model llms.Model,
	profiles RoleProfiles,
	tools []langtools.Tool,
	mem schema.Memory,
	maxIterations int,
	attachments []InputAttachment,
	handler callbacks.Handler,
	recorder *EpisodeRecorder,
	screenshotPruning ScreenshotPruningConfig,
) *roleCollaborativeExecutor {
	if mem == nil {
		mem = memory.NewSimple()
	}
	return &roleCollaborativeExecutor{
		Model:             model,
		Profiles:          profiles,
		Tools:             append([]langtools.Tool{}, tools...),
		Memory:            mem,
		MaxIterations:     maxIterations,
		InputAttachments:  append([]InputAttachment{}, attachments...),
		CallbacksHandler:  handler,
		OutputKey:         "output",
		Recorder:          recorder,
		ScreenshotPruning: screenshotPruning.WithDefaults(),
	}
}

func (e *roleCollaborativeExecutor) Call(ctx context.Context, inputValues map[string]any, options ...chains.ChainCallOption) (map[string]any, error) {
	inputs, err := executorInputsToString(inputValues)
	if err != nil {
		return nil, err
	}
	if _, ok := inputs["history"]; !ok {
		inputs["history"] = ""
	}

	nameToTool := executorNameToTool(e.Tools)
	state := roleLoopState{}
	for i := 0; i < e.MaxIterations; i++ {
		plan, err := e.callPlanner(ctx, inputs, state, options...)
		if err != nil {
			return nil, err
		}
		state.applyPlannerDecision(plan)
		state.World.UpdateObservedState(plan.ObservedState, RolePlanner)
		if e.Recorder != nil {
			e.Recorder.RecordPlannerDecision(plan)
		}

		execution, err := e.callExecutor(ctx, inputs, state, nameToTool, options...)
		if err != nil {
			return nil, err
		}
		state.ExecutionResults = append(state.ExecutionResults, execution)
		if e.Recorder != nil {
			e.Recorder.RecordExecution(execution)
		}
		if execution.Step != nil {
			state.ToolSteps = append(state.ToolSteps, *execution.Step)
			state.World.UpdateFromStep(*execution.Step, len(state.ToolSteps))
		}

		verification, err := e.callVerifier(ctx, inputs, state, options...)
		if err != nil {
			return nil, err
		}
		state.World.UpdateObservedState(verification.ObservedState, RoleVerifier)
		state.VerifierResults = append(state.VerifierResults, verification)
		if e.Recorder != nil {
			e.Recorder.RecordVerifierDecision(verification)
		}
		if verification.CanFinish {
			finalAnswer := strings.TrimSpace(verification.FinalAnswer)
			if finalAnswer == "" {
				finalAnswer = strings.TrimSpace(execution.CandidateAnswer)
			}
			if e.CallbacksHandler != nil {
				e.streamFinalAnswer(ctx, finalAnswer)
				e.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
					ReturnValues: map[string]any{e.OutputKey: finalAnswer},
					Log:          verification.Reason,
				})
			}
			return map[string]any{e.OutputKey: finalAnswer}, nil
		}

	}

	if e.CallbacksHandler != nil {
		e.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
			ReturnValues: map[string]any{e.OutputKey: agents.ErrNotFinished.Error()},
		})
	}
	return map[string]any{e.OutputKey: ""}, agents.ErrNotFinished
}

func (e *roleCollaborativeExecutor) callPlanner(ctx context.Context, inputs map[string]string, state roleLoopState, options ...chains.ChainCallOption) (plannerDecision, error) {
	messages := e.roleMessages(e.Profiles.Planner, inputs, state, "Planner task: create or update the plan and current next_step.")
	res, err := e.generateRoleContent(ctx, RolePlanner, messages, chains.GetLLMCallOptions(options...)...)
	if err != nil {
		return plannerDecision{}, err
	}
	e.emitRoleOutput(ctx, RolePlanner, roleResponseDebugText(res))
	decision := parsePlannerDecision(res, inputs["input"])
	if len(decision.Plan) == 0 {
		decision.Plan = append([]string{}, state.Plan...)
	}
	if len(decision.Plan) == 0 {
		decision.Plan = []string{inputs["input"]}
	}
	if strings.TrimSpace(decision.NextStep) == "" {
		decision.NextStep = firstNonEmptyStep(decision.Plan, inputs["input"])
	}
	return decision, nil
}

func (e *roleCollaborativeExecutor) callExecutor(
	ctx context.Context,
	inputs map[string]string,
	state roleLoopState,
	nameToTool map[string]langtools.Tool,
	options ...chains.ChainCallOption,
) (roleExecutionResult, error) {
	messages := e.roleMessages(e.Profiles.Executor, inputs, state, "Executor task: execute only current next_step. Use at most one tool call.")
	parser := &FunctionAgent{
		Tools:     e.Tools,
		OutputKey: e.OutputKey,
	}
	callOptions := append(chains.GetLLMCallOptions(options...), llms.WithTools(parser.toolsAsLLM()))
	res, err := e.generateRoleContent(ctx, RoleExecutor, messages, callOptions...)
	if err != nil {
		return roleExecutionResult{}, err
	}
	e.emitRoleOutput(ctx, RoleExecutor, roleResponseDebugText(res))

	actions, finish, err := parser.ParseOutput(res)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		return roleExecutionResult{
			Step: &schema.AgentStep{Observation: err.Error()},
		}, nil
	}
	if err != nil {
		return roleExecutionResult{}, err
	}
	if len(actions) == 0 && finish == nil {
		return roleExecutionResult{}, agents.ErrAgentNoReturn
	}
	if len(actions) == 0 {
		answer := ""
		if finish != nil {
			if value, ok := finish.ReturnValues[e.OutputKey].(string); ok {
				answer = value
			}
		}
		return roleExecutionResult{CandidateAnswer: answer}, nil
	}

	action := actions[0]
	// Only emit the agent-action callback when the tool actually exists. The
	// runtime callback handler tracks a pending action per HandleAgentAction and
	// clears it on the matching tool-end callback; an unknown tool never reaches
	// the tool wrapper, so emitting here would leave a dangling pending action.
	if _, ok := nameToTool[strings.ToUpper(action.Tool)]; ok && e.CallbacksHandler != nil {
		e.CallbacksHandler.HandleAgentAction(ctx, action)
	}
	step, err := e.callTool(ctx, nameToTool, action)
	if err != nil {
		return roleExecutionResult{}, err
	}
	return roleExecutionResult{
		Action: &action,
		Step:   &step,
	}, nil
}

func (e *roleCollaborativeExecutor) callVerifier(ctx context.Context, inputs map[string]string, state roleLoopState, options ...chains.ChainCallOption) (verifierDecision, error) {
	messages := e.roleMessages(e.Profiles.Verifier, inputs, state, "Verifier task: decide whether this run can finish. Return the required JSON.")
	res, err := e.generateRoleContent(ctx, RoleVerifier, messages, chains.GetLLMCallOptions(options...)...)
	if err != nil {
		return verifierDecision{}, err
	}
	e.emitRoleOutput(ctx, RoleVerifier, roleResponseDebugText(res))
	return parseVerifierDecision(contentResponseText(res), state.lastCandidateAnswer()), nil
}

func (e *roleCollaborativeExecutor) generateRoleContent(ctx context.Context, role RoleName, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, roleModelCallTimeout)
	defer cancel()
	callCtx = contextWithTelemetryRole(callCtx, role)
	res, err := e.Model.GenerateContent(callCtx, messages, options...)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s role model call timed out after %s", role, roleModelCallTimeout)
	}
	return res, err
}

func (e *roleCollaborativeExecutor) callTool(ctx context.Context, nameToTool map[string]langtools.Tool, action schema.AgentAction) (schema.AgentStep, error) {
	tool, ok := nameToTool[strings.ToUpper(action.Tool)]
	if !ok {
		return schema.AgentStep{
			Action:      action,
			Observation: fmt.Sprintf("%s is not a valid tool, try another one", action.Tool),
		}, nil
	}
	toolInput := normalizeToolInput(action.ToolInput)
	observation, err := tool.Call(ctx, toolInput)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return schema.AgentStep{}, err
		}
		return schema.AgentStep{
			Action:      action,
			Observation: fmt.Sprintf("error: %s failed: %v", action.Tool, err),
		}, nil
	}
	return schema.AgentStep{Action: action, Observation: observation}, nil
}

func (e *roleCollaborativeExecutor) roleMessages(profile RoleProfile, inputs map[string]string, state roleLoopState, task string) []llms.MessageContent {
	messages := []llms.MessageContent{{
		Role:  llms.ChatMessageTypeSystem,
		Parts: []llms.ContentPart{llms.TextPart(profile.SystemPrompt)},
	}}

	if roleSeesToolScratchpad(profile.Name) && len(state.ToolSteps) > 0 {
		scratchpad := (&FunctionAgent{Tools: e.Tools, ScreenshotPruning: e.ScreenshotPruning}).constructFunctionScratchPad(state.ToolSteps)
		messages = append(messages, scratchpad...)
	}

	statePrompt := buildRoleStatePrompt(profile.Name, inputs, state, task)
	messages = append(messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: buildRoleUserMessageParts(statePrompt, e.InputAttachments, state.World),
	})
	return messages
}

func roleSeesToolScratchpad(role RoleName) bool {
	return role == RolePlanner || role == RoleVerifier
}

func buildRoleStatePrompt(role RoleName, inputs map[string]string, state roleLoopState, task string) string {
	switch role {
	case RolePlanner:
		return buildPlannerStatePrompt(inputs, state, task)
	case RoleExecutor:
		return buildExecutorStatePrompt(state, task)
	case RoleVerifier:
		return buildVerifierStatePrompt(inputs, state, task)
	default:
		return buildPlannerStatePrompt(inputs, state, task)
	}
}

func buildPlannerStatePrompt(inputs map[string]string, state roleLoopState, task string) string {
	var builder strings.Builder
	builder.WriteString(task)
	writeWorldState(&builder, state.World)
	writeRequestObjectiveAndCriteria(&builder, inputs, state)
	if history := strings.TrimSpace(inputs["history"]); history != "" {
		builder.WriteString("\n\nConversation history:\n")
		builder.WriteString(history)
	}
	writeCurrentPlan(&builder, state)
	writeExecutorResults(&builder, state)
	writeVerifierFeedback(&builder, state)
	return strings.TrimSpace(builder.String())
}

func buildExecutorStatePrompt(state roleLoopState, task string) string {
	var builder strings.Builder
	builder.WriteString(task)
	writeWorldState(&builder, state.World)
	builder.WriteString("\n\nPlanner-approved next_step:\n")
	if next := strings.TrimSpace(state.NextStep); next != "" {
		builder.WriteString(next)
	} else {
		builder.WriteString("(none)")
	}
	if result, ok := state.latestExecutionResult(); ok {
		builder.WriteString("\n\nLocal execution context (latest prior result only):\n")
		writeExecutionResultLine(&builder, 1, result)
	}
	builder.WriteString("\n\nThe full plan, original request, verifier feedback, and broader history are intentionally not available to executor. Execute only the approved next_step.")
	return strings.TrimSpace(builder.String())
}

func buildVerifierStatePrompt(inputs map[string]string, state roleLoopState, task string) string {
	var builder strings.Builder
	builder.WriteString(task)
	writeWorldState(&builder, state.World)
	writeRequestObjectiveAndCriteria(&builder, inputs, state)
	writeExecutorEvidence(&builder, state)
	builder.WriteString("\nVerifier mandatory checklist:\n")
	builder.WriteString("- Re-read the original user request and completion criteria immediately before deciding.\n")
	builder.WriteString("- Do not finish merely because the latest executor step succeeded.\n")
	builder.WriteString("- Finish only if the accumulated observations prove every explicit requirement is satisfied.\n")
	builder.WriteString("- If any requested item is incomplete, uncertain, or no longer visible in context, return can_finish=false and explain the missing evidence.\n")
	builder.WriteString("\nOriginal user request repeated for final verification:\n")
	builder.WriteString(inputs["input"])
	return strings.TrimSpace(builder.String())
}

func buildRoleUserMessageParts(input string, attachments []InputAttachment, world worldState) []llms.ContentPart {
	parts := buildUserMessageParts(input, attachments)
	if world.LatestScreenshot == nil || len(world.LatestScreenshot.Data) == 0 {
		return parts
	}
	return append(parts, buildImagePart(world.LatestScreenshot.MIMEType(), world.LatestScreenshot.Data))
}

func writeWorldState(builder *strings.Builder, world worldState) {
	builder.WriteString("\n\nWorld State (shared across planner, executor, and verifier):\n")
	if world.LatestScreenshot == nil {
		builder.WriteString("- Latest screenshot: none yet.\n")
	} else {
		screenshot := world.LatestScreenshot
		builder.WriteString(fmt.Sprintf(
			"- Latest screenshot: step=%d source_tool=%s size=%dx%d format=%s bytes=%d. The current screenshot image is attached to this message.\n",
			screenshot.StepNumber,
			screenshot.SourceTool,
			screenshot.Width,
			screenshot.Height,
			screenshot.Format,
			screenshot.Size,
		))
		if input := strings.TrimSpace(screenshot.ToolInput); input != "" {
			builder.WriteString("- Screenshot source input: ")
			builder.WriteString(input)
			builder.WriteByte('\n')
		}
		if actionOutput := strings.TrimSpace(screenshot.ActionOutput); actionOutput != "" {
			builder.WriteString("- Post-action output before screenshot: ")
			builder.WriteString(compactToolObservation(actionOutput))
			builder.WriteByte('\n')
		}
	}
	if world.Observation != nil {
		obs := world.Observation
		label := strings.TrimSpace(obs.AppName)
		if page := strings.TrimSpace(obs.PageName); page != "" {
			if label != "" {
				label += " / "
			}
			label += page
		}
		if label != "" {
			builder.WriteString(fmt.Sprintf("- Observed app/page: %s", label))
			if platform := strings.TrimSpace(obs.Platform); platform != "" {
				builder.WriteString(fmt.Sprintf(" platform=%s", platform))
			}
			if obs.Confidence > 0 {
				builder.WriteString(fmt.Sprintf(" confidence=%.2f", obs.Confidence))
			}
			if obs.SourceRole != "" {
				builder.WriteString(fmt.Sprintf(" source_role=%s", obs.SourceRole))
			}
			if obs.ScreenshotStep > 0 {
				builder.WriteString(fmt.Sprintf(" screenshot_step=%d", obs.ScreenshotStep))
			}
			builder.WriteByte('\n')
		}
		if len(obs.VisibleText) > 0 {
			builder.WriteString("- Visible text: ")
			builder.WriteString(truncateForLog(strings.Join(obs.VisibleText, " | "), 240))
			builder.WriteByte('\n')
		}
		if len(obs.Dialogs) > 0 {
			builder.WriteString("- Dialogs: ")
			builder.WriteString(truncateForLog(strings.Join(obs.Dialogs, " | "), 160))
			builder.WriteByte('\n')
		}
	}
}

func writeRequestObjectiveAndCriteria(builder *strings.Builder, inputs map[string]string, state roleLoopState) {
	builder.WriteString("\n\nOriginal user request (authoritative; do not replace it with a subtask):\n")
	builder.WriteString(inputs["input"])
	builder.WriteString("\n\nCurrent objective:\n")
	if objective := strings.TrimSpace(state.Objective); objective != "" {
		builder.WriteString(objective)
	} else {
		builder.WriteString(inputs["input"])
	}
	builder.WriteString("\n\nCompletion criteria:\n")
	if len(state.CompletionCriteria) == 0 {
		builder.WriteString("- Satisfy every explicit requirement in the original user request.\n")
		return
	}
	for _, criterion := range state.CompletionCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			builder.WriteString("- ")
			builder.WriteString(criterion)
			builder.WriteByte('\n')
		}
	}
}

func writeCurrentPlan(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nCurrent plan:\n")
	if len(state.Plan) == 0 {
		builder.WriteString("(none yet)")
	} else {
		for i, step := range state.Plan {
			builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, step))
		}
	}
	if next := strings.TrimSpace(state.NextStep); next != "" {
		builder.WriteString("\nCurrent next_step:\n")
		builder.WriteString(next)
	}
	if reason := strings.TrimSpace(state.PlannerReason); reason != "" {
		builder.WriteString("\n\nPlanner reason:\n")
		builder.WriteString(reason)
	}
}

func writeExecutorResults(builder *strings.Builder, state roleLoopState) {
	if len(state.ExecutionResults) > 0 {
		builder.WriteString("\n\nExecutor results:\n")
		for i, result := range state.ExecutionResults {
			writeExecutionResultLine(builder, i+1, result)
		}
	}
}

func writeExecutorEvidence(builder *strings.Builder, state roleLoopState) {
	if len(state.ExecutionResults) == 0 {
		builder.WriteString("\n\nExecutor evidence:\n(none yet)")
		return
	}
	builder.WriteString("\n\nExecutor evidence:\n")
	for i, result := range state.ExecutionResults {
		writeExecutionResultLine(builder, i+1, result)
	}
}

func writeExecutionResultLine(builder *strings.Builder, index int, result roleExecutionResult) {
	builder.WriteString(fmt.Sprintf("%d. ", index))
	if result.Action != nil {
		builder.WriteString(fmt.Sprintf("tool=%s input=%s", result.Action.Tool, result.Action.ToolInput))
	} else {
		builder.WriteString("candidate_answer=")
		builder.WriteString(result.CandidateAnswer)
	}
	if result.Step != nil {
		builder.WriteString(" observation=")
		builder.WriteString(compactExecutionObservation(result.Action, *result.Step))
	}
	builder.WriteByte('\n')
}

func compactExecutionObservation(action *schema.AgentAction, step schema.AgentStep) string {
	toolName := ""
	if action != nil {
		toolName = action.Tool
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = step.Action.Tool
	}
	if summary, ok := compactScreenshotObservation(toolName, step.Observation); ok {
		return summary
	}
	return compactToolObservation(step.Observation)
}

func compactScreenshotObservation(toolName, observation string) (string, bool) {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(observation), &result); err != nil {
		return "", false
	}
	if result.Data == "" || result.Width <= 0 || result.Height <= 0 {
		return "", false
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = "tool"
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	size := result.Size
	if size <= 0 {
		if imageBytes, err := base64.StdEncoding.DecodeString(result.Data); err == nil {
			size = len(imageBytes)
		}
	}
	if strings.TrimSpace(result.ActionOutput) != "" {
		return fmt.Sprintf(
			"%s completed with output %q, then returned a screenshot observation after the action settled: format=%s width=%d height=%d size=%d bytes. Image data omitted from text summary.",
			toolName,
			compactToolObservation(result.ActionOutput),
			format,
			result.Width,
			result.Height,
			size,
		), true
	}
	return fmt.Sprintf(
		"%s returned a screenshot observation: format=%s width=%d height=%d size=%d bytes. Image data omitted from text summary.",
		toolName,
		format,
		result.Width,
		result.Height,
		size,
	), true
}

func writeVerifierFeedback(builder *strings.Builder, state roleLoopState) {
	if len(state.VerifierResults) > 0 {
		builder.WriteString("\nVerifier feedback:\n")
		for i, result := range state.VerifierResults {
			builder.WriteString(fmt.Sprintf("%d. can_finish=%v needs_replan=%v reason=%s\n", i+1, result.CanFinish, result.NeedsReplan, result.Reason))
		}
	}
}

func (s *roleLoopState) applyPlannerDecision(decision plannerDecision) {
	if objective := strings.TrimSpace(decision.Objective); objective != "" {
		s.Objective = objective
	}
	if len(decision.CompletionCriteria) > 0 {
		s.CompletionCriteria = uniqueNonEmpty(decision.CompletionCriteria)
	}
	s.Plan = append([]string{}, decision.Plan...)
	s.NextStep = strings.TrimSpace(decision.NextStep)
	s.PlannerReason = strings.TrimSpace(decision.Reason)
}

func (s roleLoopState) latestExecutionResult() (roleExecutionResult, bool) {
	if len(s.ExecutionResults) == 0 {
		return roleExecutionResult{}, false
	}
	return s.ExecutionResults[len(s.ExecutionResults)-1], true
}

func (s *worldState) UpdateFromStep(step schema.AgentStep, stepNumber int) {
	screenshot, ok := screenshotFromStep(step, stepNumber)
	if !ok {
		return
	}
	s.LatestScreenshot = &screenshot
}

func (s *worldState) UpdateObservedState(observed observedWorldState, source RoleName) {
	observed = normalizeObservedWorldState(observed)
	if observed.IsEmpty() {
		return
	}
	step := 0
	if s.LatestScreenshot != nil {
		step = s.LatestScreenshot.StepNumber
	}
	s.Observation = &worldStateObservation{
		observedWorldState: observed,
		SourceRole:         source,
		ObservedAt:         time.Now(),
		ScreenshotStep:     step,
	}
}

func (o observedWorldState) IsEmpty() bool {
	return strings.TrimSpace(o.AppName) == "" &&
		strings.TrimSpace(o.PageName) == "" &&
		strings.TrimSpace(o.Platform) == "" &&
		len(uniqueNonEmpty(o.VisibleText)) == 0 &&
		len(uniqueNonEmpty(o.Dialogs)) == 0
}

func normalizeObservedWorldState(observed observedWorldState) observedWorldState {
	observed.AppName = strings.TrimSpace(observed.AppName)
	observed.PageName = strings.TrimSpace(observed.PageName)
	if platform, err := normalizeQuickActionPlatform(observed.Platform); err == nil {
		observed.Platform = platform
	} else {
		observed.Platform = ""
	}
	observed.VisibleText = uniqueNonEmpty(observed.VisibleText)
	observed.Dialogs = uniqueNonEmpty(observed.Dialogs)
	if observed.Confidence < 0 {
		observed.Confidence = 0
	}
	if observed.Confidence > 1 {
		observed.Confidence = 1
	}
	return observed
}

func screenshotFromStep(step schema.AgentStep, stepNumber int) (worldScreenshot, bool) {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(step.Observation), &result); err != nil {
		return worldScreenshot{}, false
	}
	if result.Width <= 0 || result.Height <= 0 || result.Data == "" {
		return worldScreenshot{}, false
	}
	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(imageBytes) == 0 {
		return worldScreenshot{}, false
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	size := result.Size
	if size <= 0 {
		size = len(imageBytes)
	}
	return worldScreenshot{
		SourceTool:   step.Action.Tool,
		ToolInput:    normalizeToolInput(step.Action.ToolInput),
		ActionOutput: strings.TrimSpace(result.ActionOutput),
		Width:        result.Width,
		Height:       result.Height,
		Format:       format,
		Size:         size,
		Data:         imageBytes,
		StepNumber:   stepNumber,
	}, true
}

func (s worldScreenshot) MIMEType() string {
	format := strings.TrimSpace(s.Format)
	if format == "" {
		format = "jpeg"
	}
	return "image/" + format
}

func (s roleLoopState) lastCandidateAnswer() string {
	for i := len(s.ExecutionResults) - 1; i >= 0; i-- {
		if answer := strings.TrimSpace(s.ExecutionResults[i].CandidateAnswer); answer != "" {
			return answer
		}
	}
	return ""
}

func parsePlannerDecision(res *llms.ContentResponse, fallbackStep string) plannerDecision {
	raw := contentResponseText(res)

	// Try JSON parsing first
	var decision plannerDecision
	if decodeRoleJSON(raw, &decision) == nil {
		decision.Objective = strings.TrimSpace(decision.Objective)
		decision.CompletionCriteria = uniqueNonEmpty(decision.CompletionCriteria)
		decision.Plan = uniqueNonEmpty(decision.Plan)
		decision.NextStep = strings.TrimSpace(decision.NextStep)
		decision.Reason = strings.TrimSpace(decision.Reason)
		decision.ObservedState = normalizeObservedWorldState(decision.ObservedState)
		return decision
	}

	// If planner incorrectly returned tool calls, extract the description as next_step
	if res != nil && len(res.Choices) > 0 && res.Choices[0] != nil {
		choice := res.Choices[0]
		var toolDesc string

		// Check for tool calls in the response
		for _, toolCall := range choice.ToolCalls {
			if toolCall.FunctionCall != nil {
				var toolInput map[string]interface{}
				if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &toolInput); err == nil {
					if desc, ok := toolInput["description"].(string); ok && desc != "" {
						toolDesc = desc
						break
					}
				}
			}
		}

		// Check legacy FuncCall format
		if toolDesc == "" && choice.FuncCall != nil {
			var toolInput map[string]interface{}
			if err := json.Unmarshal([]byte(choice.FuncCall.Arguments), &toolInput); err == nil {
				if desc, ok := toolInput["description"].(string); ok && desc != "" {
					toolDesc = desc
				}
			}
		}

		if toolDesc != "" {
			return plannerDecision{
				Objective:          strings.TrimSpace(fallbackStep),
				CompletionCriteria: uniqueNonEmpty([]string{"Satisfy every explicit requirement in the original user request."}),
				Plan:               uniqueNonEmpty([]string{toolDesc}),
				NextStep:           toolDesc,
				Reason:             "planner incorrectly returned tool_call instead of JSON; extracted description field as next_step",
			}
		}
	}

	// Final fallback: use text content or user input
	text := strings.TrimSpace(extractFinalAnswer(raw))
	if text == "" {
		text = strings.TrimSpace(fallbackStep)
	}
	return plannerDecision{
		Objective:          strings.TrimSpace(fallbackStep),
		CompletionCriteria: uniqueNonEmpty([]string{"Satisfy every explicit requirement in the original user request."}),
		Plan:               uniqueNonEmpty([]string{text}),
		NextStep:           text,
		Reason:             "planner returned non-JSON content",
	}
}

func parseVerifierDecision(raw, fallbackAnswer string) verifierDecision {
	var decision verifierDecision
	if decodeRoleJSON(raw, &decision) == nil {
		decision.FinalAnswer = strings.TrimSpace(decision.FinalAnswer)
		decision.Reason = strings.TrimSpace(decision.Reason)
		decision.ObservedState = normalizeObservedWorldState(decision.ObservedState)
		if decision.CanFinish && decision.FinalAnswer == "" {
			decision.CanFinish = false
			decision.NeedsReplan = true
			if decision.Reason == "" {
				decision.Reason = "verifier approved finish without final_answer"
			}
		}
		return decision
	}

	text := strings.TrimSpace(extractMarkedFinalAnswer(raw))
	if text == "" {
		return verifierDecision{
			CanFinish:   false,
			NeedsReplan: true,
			Reason:      "verifier returned non-JSON content without explicit final answer",
		}
	}
	return verifierDecision{
		CanFinish:   true,
		FinalAnswer: text,
		Reason:      "verifier returned explicit non-JSON final answer",
	}
}

func extractMarkedFinalAnswer(content string) string {
	if idx := strings.LastIndex(content, "Final Answer:"); idx >= 0 {
		return strings.TrimSpace(content[idx+len("Final Answer:"):])
	}
	return ""
}

func decodeRoleJSON(raw string, out any) error {
	raw = stripMarkdownCodeFence(raw)
	if err := json.Unmarshal([]byte(raw), out); err == nil {
		return nil
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return fmt.Errorf("no JSON object in role response")
	}
	return json.Unmarshal([]byte(raw[start:end+1]), out)
}

func contentResponseText(res *llms.ContentResponse) string {
	if res == nil || len(res.Choices) == 0 || res.Choices[0] == nil {
		return ""
	}
	return strings.TrimSpace(res.Choices[0].Content)
}

func roleResponseDebugText(res *llms.ContentResponse) string {
	if res == nil || len(res.Choices) == 0 || res.Choices[0] == nil {
		return "(empty response)"
	}
	choice := res.Choices[0]
	var parts []string
	if text := strings.TrimSpace(choice.Content); text != "" {
		parts = append(parts, normalizeRoleOutputContent(text))
	}
	for _, toolCall := range choice.ToolCalls {
		if toolCall.FunctionCall == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"tool_call: %s input=%s",
			toolCall.FunctionCall.Name,
			toolCall.FunctionCall.Arguments,
		))
	}
	if choice.FuncCall != nil {
		parts = append(parts, fmt.Sprintf(
			"tool_call: %s input=%s",
			choice.FuncCall.Name,
			choice.FuncCall.Arguments,
		))
	}
	if len(parts) == 0 {
		return "(empty response)"
	}
	return strings.Join(parts, "\n")
}

func normalizeRoleOutputContent(raw string) string {
	raw = stripMarkdownCodeFence(raw)
	if json.Valid([]byte(raw)) {
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, []byte(raw)); err == nil {
			return compacted.String()
		}
	}
	return raw
}

func stripMarkdownCodeFence(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	newline := strings.IndexByte(raw, '\n')
	if newline < 0 {
		return raw
	}
	body := strings.TrimSpace(raw[newline+1:])
	if idx := strings.LastIndex(body, "```"); idx >= 0 && strings.TrimSpace(body[idx:]) == "```" {
		body = strings.TrimSpace(body[:idx])
	}
	return body
}

func (e *roleCollaborativeExecutor) emitRoleOutput(ctx context.Context, role RoleName, content string) {
	if e.CallbacksHandler == nil {
		return
	}
	handler, ok := e.CallbacksHandler.(roleOutputHandler)
	if !ok {
		return
	}
	handler.HandleRoleOutput(ctx, string(role), content)
}

func firstNonEmptyStep(values []string, fallback string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

type streamingFuncHandler interface {
	HandleStreamingFunc(context.Context, []byte)
}

func (e *roleCollaborativeExecutor) streamFinalAnswer(ctx context.Context, finalAnswer string) {
	if finalAnswer == "" {
		return
	}
	streamer, ok := e.CallbacksHandler.(streamingFuncHandler)
	if !ok {
		return
	}
	streamer.HandleStreamingFunc(ctx, []byte(finalAnswer))
}

func (e *roleCollaborativeExecutor) GetInputKeys() []string {
	return []string{"input", "history"}
}

func (e *roleCollaborativeExecutor) GetOutputKeys() []string {
	return []string{e.OutputKey}
}

func (e *roleCollaborativeExecutor) GetMemory() schema.Memory {
	return e.Memory
}

func (e *roleCollaborativeExecutor) GetCallbackHandler() callbacks.Handler {
	return e.CallbacksHandler
}
