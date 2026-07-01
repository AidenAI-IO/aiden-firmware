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
	Model                  llms.Model
	Profiles               RoleProfiles
	Tools                  []langtools.Tool
	Memory                 schema.Memory
	CallbacksHandler       callbacks.Handler
	MaxIterations          int
	TodoReminderToolCalls  int
	ToolCallSpeech         bool
	ConversationHistory    []llms.MessageContent
	InputAttachments       []InputAttachment
	OutputKey              string
	Recorder               *EpisodeRecorder
	ScreenshotPruning      ScreenshotPruningConfig
	VisualArtifacts        *visualArtifactStore
	InitialWorldState      worldState
	SteerProvider          func(context.Context) (RunSteerMessage, bool)
	FinalSteerProvider     func(context.Context) (RunSteerMessage, bool)
	SteerInterrupt         func() <-chan struct{}
	SteerWaiter            func(context.Context) (RunSteerMessage, bool, error)
	handledSteerInterrupt  <-chan struct{}
	EnvironmentBridge      *EnvironmentBridgeClient
	EnvironmentBridgeTools []string
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
	Speech        string             `json:"speech,omitempty"`
	Text          string             `json:"text,omitempty"`
	NeedsReplan   bool               `json:"needs_replan,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	ObservedState observedWorldState `json:"observed_state,omitempty"`
}

type defaultFinalReview struct {
	CanFinish         bool   `json:"can_finish"`
	FinalAnswer       string `json:"final_answer,omitempty"`
	NeedsHumanHandoff bool   `json:"needs_human_handoff,omitempty"`
	HandoffReason     string `json:"handoff_reason,omitempty"`
	HandoffDetails    string `json:"handoff_details,omitempty"`
	SuggestedAction   string `json:"suggested_action,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type roleExecutionResult struct {
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	ToolError       *ToolError
	CandidateAnswer string
}

type planStepResult struct {
	StepIndex      int
	StepText       string
	Outcome        string
	Summary        string
	KeyInfo        []string
	CanFinish      bool
	NeedsReplan    bool
	VerifierReason string
}

type roleLoopState struct {
	Phase                          loopPhase
	ForceSimpleLoop                bool
	Todo                           TodoState
	PlanStepIndex                  int
	PlanCommitted                  bool
	PlanExhausted                  bool
	DraftPlan                      plannerDecision
	Objective                      string
	CompletionCriteria             []string
	Plan                           []string
	NextStep                       string
	PlannerReason                  string
	PlanCommitRequired             bool
	World                          worldState
	ToolSteps                      []schema.AgentStep
	StepToolSteps                  []schema.AgentStep
	StepExecutionResults           []roleExecutionResult
	StepExecutionActive            bool
	ExecutorStepOutcome            string
	ExecutorStepSummary            string
	ExecutorStepKeyInfo            []string
	PlanStepResults                []planStepResult
	ExecutionResults               []roleExecutionResult
	PlannerEvidence                []roleExecutionResult
	VerifierResults                []verifierDecision
	SteerMessages                  []RunSteerMessage
	DefaultToolCallsSinceTodoTouch int
	PendingTodoReminder            string
	TodoReminderToolCalls          int
}

type worldState struct {
	// LatestScreenshot is verifier-only visual evidence. Planner and executor
	// receive screenshots through tool scratchpads/results so their world-state
	// prompt prefix stays stable for model cache reuse.
	LatestScreenshot  *worldScreenshot
	Observation       *worldStateObservation
	DeviceEnvironment *worldDeviceEnvironment
}

type worldDeviceEnvironment struct {
	PhoneEnvironment
	UpdatedAt time.Time
}

type worldScreenshot struct {
	SourceTool    string
	ToolInput     string
	ActionOutput  string
	Width         int
	Height        int
	Format        string
	Size          int
	ScreenshotRef string
	Data          []byte
	StepNumber    int
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

type plannerDecisionHandler interface {
	HandlePlannerDecision(ctx context.Context, decision plannerDecision)
}

type verifierDecisionHandler interface {
	HandleVerifierDecision(ctx context.Context, decision verifierDecision)
}

type steerMessageHandler interface {
	HandleSteerMessage(ctx context.Context, steer RunSteerMessage)
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
	deviceEnvironment *PhoneEnvironment,
	steerProviders ...func(context.Context) (RunSteerMessage, bool),
) *roleCollaborativeExecutor {
	if mem == nil {
		mem = memory.NewSimple()
	}
	var steerProvider func(context.Context) (RunSteerMessage, bool)
	if len(steerProviders) > 0 {
		steerProvider = steerProviders[0]
	}
	world := worldState{}
	world.UpdateDeviceEnvironment(deviceEnvironment)
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
		VisualArtifacts:   newVisualArtifactStore(recorder),
		InitialWorldState: world,
		SteerProvider:     steerProvider,
	}
}

func (e *roleCollaborativeExecutor) Call(ctx context.Context, inputValues map[string]any, options ...chains.ChainCallOption) (map[string]any, error) {
	if e.VisualArtifacts != nil {
		defer e.VisualArtifacts.Close()
	}
	inputs, err := executorInputsToString(inputValues)
	if err != nil {
		return nil, err
	}
	if _, ok := inputs["history"]; !ok {
		inputs["history"] = ""
	}
	for _, key := range []string{sessionContextInputKey, rootRequestInputKey, latestUserInputKey} {
		if _, ok := inputs[key]; !ok {
			inputs[key] = ""
		}
	}

	plannerToolSpecs := toolSpecsForRole(RolePlanner, e.Tools)
	executorToolSpecs := toolSpecsForRole(RoleExecutor, e.Tools)
	state := roleLoopState{
		Phase:                 phaseDefault,
		ForceSimpleLoop:       true,
		Todo:                  TodoState{Mode: TodoModeNone},
		World:                 e.InitialWorldState,
		TodoReminderToolCalls: e.TodoReminderToolCalls,
	}
	for i := 0; i < e.MaxIterations; i++ {
		consumedInterrupt, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
		if err != nil {
			return nil, err
		}
		if consumedInterrupt {
			continue
		}
		switch state.Phase {
		case phaseDecision, phaseDefault, phasePlan:
			turn, err := e.callPlannerTurn(ctx, inputs, &state, plannerToolSpecs, options...)
			if err != nil {
				return nil, err
			}
			switch turn.Kind {
			case plannerTurnSteer:
				continue
			case plannerTurnFinish:
				consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
				if err != nil {
					return nil, err
				}
				if consumed {
					continue
				}
				canFinish := state.Phase == phaseDecision || state.Phase == phaseDefault || state.canAcceptPlannerFinal(turn.Answer)
				if canFinish {
					consumed, err = e.consumeFinalPendingSteer(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					if state.shouldReviewDefaultFinal(plannerToolSpecs) {
						review, err := e.callDefaultFinalReview(ctx, inputs, state, turn.Answer, options...)
						if err != nil {
							return nil, err
						}
						if review.NeedsHumanHandoff {
							handoffTurn, err := e.executeDefaultFinalHandoff(ctx, &state, plannerToolSpecs, review)
							if err != nil {
								return nil, err
							}
							if handoffTurn.Step != nil {
								state.ToolSteps = append(state.ToolSteps, *handoffTurn.Step)
								state.World.UpdateFromStep(*handoffTurn.Step, len(state.ToolSteps), e.Tools, e.VisualArtifacts)
							}
							if handoffTurn.Kind == plannerTurnTool {
								consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
								if err != nil {
									return nil, err
								}
								if consumed {
									continue
								}
								if _, err := e.consumePendingSteer(ctx, inputs, &state); err != nil {
									return nil, err
								}
								state.noteDefaultToolCallAndMaybeTodoReminder()
							}
							if handoffTurn.Kind == plannerTurnSleep {
								consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
								if err != nil {
									return nil, err
								}
								if consumed {
									continue
								}
								consumed, err = e.consumeFinalPendingSteer(ctx, inputs, &state)
								if err != nil {
									return nil, err
								}
								if consumed {
									continue
								}
								answer := runPausingToolFinalAnswer(handoffTurn.Step)
								if e.Recorder != nil {
									e.Recorder.RecordDefaultFinish(answer)
								}
								return e.finishRunWithoutStreaming(ctx, answer, answer, &state)
							}
							continue
						}
						if !review.CanFinish {
							state.Phase = phasePlan
							state.PlanCommitRequired = true
							state.PlannerReason = defaultString(review.Reason, "default final review requested replanning")
							state.VerifierResults = append(state.VerifierResults, verifierDecision{
								CanFinish:   false,
								NeedsReplan: true,
								Reason:      state.PlannerReason,
							})
							if e.Recorder != nil {
								e.Recorder.RecordLoopPhase(phasePlan, state.PlannerReason)
							}
							continue
						}
						if answer := strings.TrimSpace(review.FinalAnswer); answer != "" {
							turn.Answer = answer
						}
					}
					if e.Recorder != nil {
						e.Recorder.RecordDefaultFinish(turn.Answer)
					}
					return e.finishRun(ctx, turn.Answer, turn.Answer, &state)
				}
				consumed, err = e.consumePendingSteer(ctx, inputs, &state)
				if err != nil {
					return nil, err
				}
				if consumed {
					continue
				}
				continue
			case plannerTurnUseSimpleMode:
				continue
			case plannerTurnEnterPlan:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
				}
				if e.Recorder != nil {
					e.Recorder.RecordLoopPhase(phasePlan, "enter_plan_mode")
				}
			case plannerTurnCommitPlan:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
				}
				if state.Todo.Mode != TodoModeNone && len(state.Todo.Items) > 0 {
					e.emitTodoUpdate(ctx, state.Todo, state.Todo.SummaryText(), false)
				}
				if e.Recorder != nil {
					e.Recorder.RecordPlannerDecision(turn.CommittedPlan)
					e.Recorder.RecordLoopPhase(phaseExecution, "commit_plan")
				}
				if handler, ok := e.CallbacksHandler.(plannerDecisionHandler); ok {
					handler.HandlePlannerDecision(ctx, turn.CommittedPlan)
				}
			case plannerTurnSetTodo:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
				}
				if turn.Todo.Mode != TodoModeNone && len(turn.Todo.Items) > 0 {
					e.emitTodoUpdate(ctx, turn.Todo, turn.Todo.CurrentSpeech(), turn.TodoSpeechEligible)
				}
			case plannerTurnCancelPlan:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
				}
				if e.Recorder != nil {
					e.Recorder.RecordLoopPhase(phaseDefault, "cancel_plan")
				}
			case plannerTurnTool, plannerTurnInvalidMeta, plannerTurnSleep:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
					state.World.UpdateFromStep(*turn.Step, len(state.ToolSteps), e.Tools, e.VisualArtifacts)
				}
				if turn.Kind == plannerTurnTool {
					consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					if _, err := e.consumePendingSteer(ctx, inputs, &state); err != nil {
						return nil, err
					}
					state.noteDefaultToolCallAndMaybeTodoReminder()
				}
				if turn.Kind == plannerTurnSleep {
					consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					consumed, err = e.consumeFinalPendingSteer(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					answer := runPausingToolFinalAnswer(turn.Step)
					if e.Recorder != nil {
						e.Recorder.RecordDefaultFinish(answer)
					}
					return e.finishRunWithoutStreaming(ctx, answer, answer, &state)
				}
			}
		case phaseExecution:
			if !state.StepExecutionActive {
				state.syncNextStepFromPlanIndex()
				state.beginStepExecution()
				if todo, ok := state.startCurrentTodoStep(); ok {
					e.emitTodoUpdate(ctx, todo, todo.CurrentSpeech(), true)
				}
			}
			turn, err := e.callExecutorTurn(ctx, inputs, &state, executorToolSpecs, options...)
			if err != nil {
				return nil, err
			}
			switch turn.Kind {
			case executorTurnSteer:
				continue
			case executorTurnTool, executorTurnInvalidMeta, executorTurnSleep:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
					state.StepToolSteps = append(state.StepToolSteps, *turn.Step)
					state.World.UpdateFromStep(*turn.Step, len(state.ToolSteps), e.Tools, e.VisualArtifacts)
				}
				if turn.Kind == executorTurnTool {
					execution := roleExecutionResult{Action: turn.Action, Step: turn.Step, ToolError: turn.ToolError}
					state.StepExecutionResults = append(state.StepExecutionResults, execution)
					state.ExecutionResults = append(state.ExecutionResults, execution)
					if e.Recorder != nil {
						e.Recorder.RecordExecution(execution)
					}
					consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					if _, err := e.consumePendingSteer(ctx, inputs, &state); err != nil {
						return nil, err
					}
				}
				if turn.Kind == executorTurnSleep {
					consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					consumed, err = e.consumeFinalPendingSteer(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						continue
					}
					answer := runPausingToolFinalAnswer(turn.Step)
					if e.Recorder != nil {
						e.Recorder.RecordDefaultFinish(answer)
					}
					return e.finishRunWithoutStreaming(ctx, answer, answer, &state)
				}
			case executorTurnFinishStep, executorTurnAbortStep:
				if turn.Step != nil {
					state.ToolSteps = append(state.ToolSteps, *turn.Step)
					state.StepToolSteps = append(state.StepToolSteps, *turn.Step)
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
				if handler, ok := e.CallbacksHandler.(verifierDecisionHandler); ok {
					handler.HandleVerifierDecision(ctx, verification)
				}
				stepSummary := strings.TrimSpace(state.ExecutorStepSummary)
				state.recordPlanStepResult(verification)
				if verification.NeedsReplan {
					if todo, ok := state.blockCurrentTodoStep(verification.Reason); ok {
						e.emitTodoUpdate(ctx, todo, todo.CurrentSpeech(), false)
					}
					state.clearStepExecution()
					state.Phase = phasePlan
					state.PlanExhausted = false
					if e.Recorder != nil {
						e.Recorder.RecordLoopPhase(phasePlan, verification.Reason)
					}
					continue
				}
				if verification.CanFinish {
					if todo, ok := state.finishCurrentTodoStep(); ok {
						e.emitTodoUpdate(ctx, todo, todo.CurrentSpeech(), false)
					}
					state.clearStepExecution()
					finalAnswer := strings.TrimSpace(verification.FinalAnswer)
					if finalAnswer == "" {
						finalAnswer = stepSummary
					}
					consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						state.Phase = phaseDefault
						continue
					}
					consumed, err = e.consumeFinalPendingSteer(ctx, inputs, &state)
					if err != nil {
						return nil, err
					}
					if consumed {
						state.Phase = phaseDefault
						continue
					}
					return e.finishRun(ctx, finalAnswer, verification.Reason, &state)
				}
				doneTodo, doneChanged := state.finishCurrentTodoStep()
				state.clearStepExecution()
				if exhausted := state.advancePlanStepOrExhaust(); exhausted {
					if doneChanged {
						e.emitTodoUpdate(ctx, doneTodo, doneTodo.CurrentSpeech(), false)
					}
					state.Phase = phasePlan
					if e.Recorder != nil {
						e.Recorder.RecordLoopPhase(phasePlan, "plan_exhausted")
					}
					continue
				}
				if todo, ok := state.startCurrentTodoStep(); ok {
					e.emitTodoUpdate(ctx, todo, todo.CurrentSpeech(), true)
				} else if doneChanged {
					e.emitTodoUpdate(ctx, doneTodo, doneTodo.CurrentSpeech(), false)
				}
			}
		}
	}

	if e.CallbacksHandler != nil {
		e.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
			ReturnValues: map[string]any{e.OutputKey: agents.ErrNotFinished.Error()},
		})
	}
	return map[string]any{e.OutputKey: ""}, agents.ErrNotFinished
}

func (e *roleCollaborativeExecutor) callPlannerTurn(
	ctx context.Context,
	inputs map[string]string,
	state *roleLoopState,
	toolSpecs *ToolSpecs,
	options ...chains.ChainCallOption,
) (plannerTurnResult, error) {
	messages := e.roleMessages(e.Profiles.Planner, inputs, *state, "")
	state.PendingTodoReminder = ""
	plannerTools := appendSimpleTodoMetaTools(toolsForRole(RolePlanner, e.Tools))
	parser := &FunctionAgent{
		Tools:     plannerTools,
		OutputKey: e.OutputKey,
	}
	callOptions := append(chains.GetLLMCallOptions(options...), llms.WithTools(parser.toolsAsLLM()))
	generate := func() (*llms.ContentResponse, error) {
		return e.generateRoleContent(ctx, RolePlanner, messages, callOptions...)
	}
	// Planner turns with tool schemas are tool-capable. Do not provider-stream
	// their content as final speech before ParseOutput proves the response is
	// not a tool call; finishRun streams confirmed final answers after parsing.
	if diagnostics, ok := e.CallbacksHandler.(finalStreamingDecisionLogger); ok {
		diagnostics.LogFinalStreamingDecision(ctx, RolePlanner, false,
			fmt.Sprintf("tool_capable_agent_turn phase=%s single_agent=true tools=%d", state.Phase, len(plannerTools)))
	}
	res, err := generate()
	if err != nil {
		return plannerTurnResult{}, err
	}
	e.emitRoleOutput(ctx, RolePlanner, roleResponseDebugText(res))
	if consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, state); err != nil {
		return plannerTurnResult{}, err
	} else if consumed {
		return plannerTurnResult{Kind: plannerTurnSteer}, nil
	}

	actions, finish, err := parser.ParseOutput(res)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		return plannerTurnResult{
			Kind: plannerTurnTool,
			Step: &schema.AgentStep{Observation: err.Error()},
		}, nil
	}
	if err != nil {
		return plannerTurnResult{}, err
	}
	if len(actions) == 0 && finish == nil {
		return plannerTurnResult{}, agents.ErrAgentNoReturn
	}
	if len(actions) == 0 {
		answer := ""
		if finish != nil {
			if value, ok := finish.ReturnValues[e.OutputKey].(string); ok {
				answer = value
			}
		}
		if strings.TrimSpace(answer) == "" {
			return plannerTurnResult{}, agents.ErrAgentNoReturn
		}
		if state.Phase == phaseDefault && looksLikeInternalJSONPlanFinal(answer) {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				Step: &schema.AgentStep{
					Action:      schema.AgentAction{Tool: "final_answer", Log: strings.TrimSpace(answer)},
					Observation: "final answer looked like an internal JSON plan; use available tools, set_todo, or enter plan mode instead of returning the plan as the user-facing answer",
				},
			}, nil
		}
		if state.Phase == phasePlan && state.PlanCommitRequired {
			return plannerCommitRequiredTurn(schema.AgentAction{
				Tool: toolCommitPlan,
				Log:  strings.TrimSpace(answer),
			}), nil
		}
		if state.Phase == phasePlan && !state.canAcceptPlannerFinal(answer) {
			return plannerPlanModeToolRejectedTurn(schema.AgentAction{
				Tool: toolCommitPlan,
				Log:  strings.TrimSpace(answer),
			}), nil
		}
		return plannerTurnResult{Kind: plannerTurnFinish, Answer: answer}, nil
	}

	action := actions[0]
	if state.Phase == phasePlan && !isLoopMetaTool(action.Tool) {
		if isPlannerReadOnlyTool(action.Tool, toolSpecs) {
			return e.executePlannerToolAction(ctx, state, toolSpecs, action)
		}
		if state.PlanCommitRequired {
			return plannerCommitRequiredTurn(action), nil
		}
		return plannerPlanModeToolRejectedTurn(action), nil
	}
	if isLoopMetaTool(action.Tool) {
		if state.Phase == phasePlan && state.PlanCommitRequired &&
			!toolNameEqual(action.Tool, toolCommitPlan) &&
			!toolNameEqual(action.Tool, toolEnterPlanMode) {
			return plannerCommitRequiredTurn(action), nil
		}
		if e.CallbacksHandler != nil {
			e.CallbacksHandler.HandleAgentAction(ctx, action)
		}
		if !toolNameEqual(action.Tool, toolSetTodo) {
			return plannerTurnResult{
				Kind: plannerTurnInvalidMeta,
				Step: &schema.AgentStep{
					Action:      action,
					Observation: "plan mode tools are disabled in single-agent mode; use available tools directly or return a final answer",
				},
			}, nil
		}
		turn := e.handlePlannerMetaTool(state.Phase, state, action)
		if turn.InvalidMetaStep != nil {
			turn.Step = turn.InvalidMetaStep
		}
		return turn, nil
	}

	return e.executePlannerToolAction(ctx, state, toolSpecs, action)
}

func looksLikeInternalJSONPlanFinal(answer string) bool {
	text := strings.TrimSpace(answer)
	if !strings.HasPrefix(text, "{") {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return false
	}
	keys := map[string]bool{}
	for key := range payload {
		keys[strings.ToLower(strings.TrimSpace(key))] = true
	}
	if keys["plan"] && (keys["objective"] || keys["completion_criteria"] || keys["next_step"] || keys["reason"]) {
		return true
	}
	mode, _ := payload["mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "plan" || mode == "simple" {
		finalAnswer, _ := payload["final_answer"].(string)
		return strings.TrimSpace(finalAnswer) == ""
	}
	return false
}

func (e *roleCollaborativeExecutor) executePlannerToolAction(
	ctx context.Context,
	state *roleLoopState,
	toolSpecs *ToolSpecs,
	action schema.AgentAction,
) (plannerTurnResult, error) {
	toolExecution := e.executeToolCall(ctx, ToolCallExecution{
		Specs:                  toolSpecs,
		Action:                 action,
		Callback:               e.CallbacksHandler,
		EnvironmentBridge:      e.EnvironmentBridge,
		EnvironmentBridgeTools: e.EnvironmentBridgeTools,
		VisualArtifacts:        e.VisualArtifacts,
	})
	if toolExecution.Error != nil && !(errors.Is(toolExecution.Error, context.Canceled) && e.steerInterruptSignaled(ctx)) {
		return plannerTurnResult{}, toolExecution.Error
	}
	execution := roleExecutionResult{
		Action:    &toolExecution.Step.Action,
		Step:      &toolExecution.Step,
		ToolError: toolExecution.Result.Error,
	}
	state.ExecutionResults = append(state.ExecutionResults, execution)
	state.PlannerEvidence = append(state.PlannerEvidence, execution)
	if e.Recorder != nil {
		e.Recorder.RecordPlannerExecution(execution)
	}
	kind := plannerTurnTool
	if isRunPausingTool(toolExecution.Step.Action.Tool) && !toolExecution.Result.IsError() {
		kind = plannerTurnSleep
	}
	return plannerTurnResult{Kind: kind, Step: &toolExecution.Step}, nil
}

func (e *roleCollaborativeExecutor) finishRun(ctx context.Context, finalAnswer, log string, state *roleLoopState) (map[string]any, error) {
	return e.finishRunWithStreaming(ctx, finalAnswer, log, state, true)
}

func (e *roleCollaborativeExecutor) finishRunWithoutStreaming(ctx context.Context, finalAnswer, log string, state *roleLoopState) (map[string]any, error) {
	return e.finishRunWithStreaming(ctx, finalAnswer, log, state, false)
}

func (e *roleCollaborativeExecutor) finishRunWithStreaming(ctx context.Context, finalAnswer, log string, state *roleLoopState, stream bool) (map[string]any, error) {
	finalAnswer = strings.TrimSpace(finalAnswer)
	if e.CallbacksHandler != nil {
		if stream {
			e.streamFinalAnswer(ctx, finalAnswer)
		}
		e.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
			ReturnValues: map[string]any{e.OutputKey: finalAnswer},
			Log:          log,
		})
	}
	if state != nil && state.Todo.Mode != TodoModeNone && len(state.Todo.Items) > 0 {
		e.emitTodoClosed(ctx, state.Todo, "final_answer")
	}
	return map[string]any{e.OutputKey: finalAnswer}, nil
}

type todoUpdateHandler interface {
	HandleTodoUpdate(ctx context.Context, todo TodoState, content string, speechEligible bool)
}

type todoClosedHandler interface {
	HandleTodoClosed(ctx context.Context, todo TodoState, reason string)
}

func (e *roleCollaborativeExecutor) emitTodoUpdate(ctx context.Context, todo TodoState, content string, speechEligible bool) {
	if e == nil {
		return
	}
	content = strings.TrimSpace(content)
	snapshot := todo.Clone()
	if e.Recorder != nil {
		e.Recorder.RecordTodoUpdate(snapshot, content, speechEligible)
	}
	if e.CallbacksHandler == nil {
		return
	}
	handler, ok := e.CallbacksHandler.(todoUpdateHandler)
	if !ok {
		return
	}
	handler.HandleTodoUpdate(ctx, snapshot, content, speechEligible)
}

func (e *roleCollaborativeExecutor) emitTodoClosed(ctx context.Context, todo TodoState, reason string) {
	if e == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	snapshot := todo.Clone()
	if e.Recorder != nil {
		e.Recorder.RecordTodoClosed(snapshot, reason)
	}
	if e.CallbacksHandler == nil {
		return
	}
	handler, ok := e.CallbacksHandler.(todoClosedHandler)
	if !ok {
		return
	}
	handler.HandleTodoClosed(ctx, snapshot, reason)
}

func (e *roleCollaborativeExecutor) executeToolCall(ctx context.Context, execution ToolCallExecution) ToolCallExecutionResult {
	interruptCh := (<-chan struct{})(nil)
	if e != nil && e.SteerInterrupt != nil {
		interruptCh = e.SteerInterrupt()
	}
	if interruptCh == nil {
		return executeToolCall(ctx, execution)
	}

	toolCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-interruptCh:
			cancel()
		case <-toolCtx.Done():
		case <-done:
		}
	}()
	result := executeToolCall(toolCtx, execution)
	close(done)
	cancel()
	return result
}

func (e *roleCollaborativeExecutor) consumePendingSteer(ctx context.Context, inputs map[string]string, state *roleLoopState) (bool, error) {
	if e == nil || e.SteerProvider == nil || state == nil {
		return false, nil
	}
	steer, ok := e.SteerProvider(ctx)
	if !ok {
		return false, nil
	}
	return e.appendSteerMessage(ctx, inputs, state, steer)
}

func (e *roleCollaborativeExecutor) consumeFinalPendingSteer(ctx context.Context, inputs map[string]string, state *roleLoopState) (bool, error) {
	if e == nil || state == nil {
		return false, nil
	}
	provider := e.FinalSteerProvider
	if provider == nil {
		provider = e.SteerProvider
	}
	if provider == nil {
		return false, nil
	}
	steer, ok := provider(ctx)
	if !ok {
		return false, nil
	}
	return e.appendSteerMessage(ctx, inputs, state, steer)
}

func (e *roleCollaborativeExecutor) consumeSteerInterruptIfSignaled(ctx context.Context, inputs map[string]string, state *roleLoopState) (bool, error) {
	if e == nil || e.SteerInterrupt == nil || e.SteerWaiter == nil || state == nil {
		return false, nil
	}
	interruptCh := e.SteerInterrupt()
	if interruptCh == nil {
		return false, nil
	}
	if e.handledSteerInterrupt == interruptCh {
		return false, nil
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-interruptCh:
	default:
		return false, nil
	}
	e.handledSteerInterrupt = interruptCh

	steer, ok, err := e.SteerWaiter(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if consumed, err := e.appendSteerMessage(ctx, inputs, state, steer); err != nil || !consumed {
		return consumed, err
	}
	state.Phase = phaseDefault
	state.clearStepExecution()
	state.PlanExhausted = false
	return true, nil
}

func (e *roleCollaborativeExecutor) steerInterruptSignaled(ctx context.Context) bool {
	if e == nil || e.SteerInterrupt == nil {
		return false
	}
	interruptCh := e.SteerInterrupt()
	if interruptCh == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-interruptCh:
		return true
	default:
		return false
	}
}

func (e *roleCollaborativeExecutor) appendSteerMessage(ctx context.Context, inputs map[string]string, state *roleLoopState, steer RunSteerMessage) (bool, error) {
	if e == nil || state == nil {
		return false, nil
	}
	if steer.Timestamp.IsZero() {
		steer.Timestamp = time.Now()
	}
	if appender, ok := e.Memory.(steerConversationAppender); ok {
		if err := appender.AppendSteerMessage(ctx, inputs["input"], steer); err != nil {
			return false, err
		}
	}
	state.SteerMessages = append(state.SteerMessages, steer)
	if handler, ok := e.CallbacksHandler.(steerMessageHandler); ok {
		handler.HandleSteerMessage(ctx, steer)
	}
	return true, nil
}

func (e *roleCollaborativeExecutor) callExecutorTurn(
	ctx context.Context,
	inputs map[string]string,
	state *roleLoopState,
	toolSpecs *ToolSpecs,
	options ...chains.ChainCallOption,
) (executorTurnResult, error) {
	messages := e.roleMessages(e.Profiles.Executor, inputs, *state, "Executor task: work on the current next_step across multiple tool calls if needed, then call finish_step when the step is ready for verification or abort_step if blocked.")
	executorTools := appendExecutorMetaTools(toolsForRole(RoleExecutor, e.Tools))
	parser := &FunctionAgent{
		Tools:     executorTools,
		OutputKey: e.OutputKey,
	}
	callOptions := append(chains.GetLLMCallOptions(options...), llms.WithTools(parser.toolsAsLLM()))
	res, err := e.generateRoleContent(ctx, RoleExecutor, messages, callOptions...)
	if err != nil {
		return executorTurnResult{}, err
	}
	e.emitRoleOutput(ctx, RoleExecutor, roleResponseDebugText(res))
	if consumed, err := e.consumeSteerInterruptIfSignaled(ctx, inputs, state); err != nil {
		return executorTurnResult{}, err
	} else if consumed {
		return executorTurnResult{Kind: executorTurnSteer}, nil
	}

	actions, finish, err := parser.ParseOutput(res)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		return executorTurnResult{
			Kind: executorTurnTool,
			Step: &schema.AgentStep{Observation: err.Error()},
		}, nil
	}
	if err != nil {
		return executorTurnResult{}, err
	}
	if len(actions) == 0 && finish == nil {
		return executorTurnResult{}, agents.ErrAgentNoReturn
	}
	if len(actions) == 0 {
		answer := ""
		if finish != nil {
			if value, ok := finish.ReturnValues[e.OutputKey].(string); ok {
				answer = value
			}
		}
		if hasTaggedTTSOutput(answer) {
			payload, _ := json.Marshal(map[string]any{
				"summary":  strings.TrimSpace(answer),
				"key_info": []string{strings.TrimSpace(answer)},
				"reason":   "executor returned tagged final output",
			})
			action := schema.AgentAction{
				Tool:      toolFinishStep,
				ToolInput: string(payload),
				Log:       strings.TrimSpace(answer),
			}
			if e.CallbacksHandler != nil {
				e.CallbacksHandler.HandleAgentAction(ctx, action)
			}
			turn := e.handleExecutorMetaTool(state, action)
			if turn.InvalidMetaStep != nil {
				turn.Step = turn.InvalidMetaStep
			}
			return turn, nil
		}
		observation := "Call finish_step when the step is ready for verification or abort_step if blocked."
		if answer != "" {
			observation = fmt.Sprintf("%s Plain text output alone does not enter verification: %s", observation, answer)
		}
		return executorTurnResult{
			Kind: executorTurnInvalidMeta,
			Step: &schema.AgentStep{Observation: observation},
		}, nil
	}

	action := actions[0]
	if isExecutorMetaTool(action.Tool) {
		if e.CallbacksHandler != nil {
			e.CallbacksHandler.HandleAgentAction(ctx, action)
		}
		turn := e.handleExecutorMetaTool(state, action)
		if turn.InvalidMetaStep != nil {
			turn.Step = turn.InvalidMetaStep
		}
		return turn, nil
	}

	toolExecution := e.executeToolCall(ctx, ToolCallExecution{
		Specs:                  toolSpecs,
		Action:                 action,
		Callback:               e.CallbacksHandler,
		EnvironmentBridge:      e.EnvironmentBridge,
		EnvironmentBridgeTools: e.EnvironmentBridgeTools,
		VisualArtifacts:        e.VisualArtifacts,
	})
	if toolExecution.Error != nil && !(errors.Is(toolExecution.Error, context.Canceled) && e.steerInterruptSignaled(ctx)) {
		return executorTurnResult{}, toolExecution.Error
	}
	actionCopy := toolExecution.Step.Action
	kind := executorTurnTool
	if isRunPausingTool(toolExecution.Step.Action.Tool) && !toolExecution.Result.IsError() {
		kind = executorTurnSleep
	}
	return executorTurnResult{
		Kind:      kind,
		Action:    &actionCopy,
		Step:      &toolExecution.Step,
		ToolError: toolExecution.Result.Error,
	}, nil
}

func (e *roleCollaborativeExecutor) callVerifier(ctx context.Context, inputs map[string]string, state roleLoopState, options ...chains.ChainCallOption) (verifierDecision, error) {
	messages := e.roleMessages(e.Profiles.Verifier, inputs, state, "Verifier task: decide whether the current executor step succeeded. Return the required JSON.")
	baseOptions := chains.GetLLMCallOptions(options...)
	generate := func(callOptions []llms.CallOption) (*llms.ContentResponse, error) {
		return e.generateRoleContent(ctx, RoleVerifier, messages, callOptions...)
	}
	var (
		res *llms.ContentResponse
		err error
	)
	if state.isFinalCommittedPlanStep() {
		callOptions := e.finalStreamingCallOptions(baseOptions)
		res, err = e.withFinalStreaming(ctx, func() (*llms.ContentResponse, error) {
			return generate(callOptions)
		})
	} else {
		res, err = generate(baseOptions)
	}
	if err != nil {
		return verifierDecision{}, err
	}
	e.emitRoleOutput(ctx, RoleVerifier, roleResponseDebugText(res))
	return parseVerifierDecision(contentResponseText(res), state.lastCandidateAnswer()), nil
}

func (s roleLoopState) shouldReviewDefaultFinal(toolSpecs *ToolSpecs) bool {
	// Single-agent mode finishes directly. The separate verifier/final-review
	// pass is intentionally disabled with the planner/executor/verifier split.
	return false
}

func (e *roleCollaborativeExecutor) callDefaultFinalReview(
	ctx context.Context,
	inputs map[string]string,
	state roleLoopState,
	candidateAnswer string,
	options ...chains.ChainCallOption,
) (defaultFinalReview, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(
				"You are a strict final-answer gate for an agent that operates external devices. " +
					"Return only JSON. Do not solve the task yourself and do not ask the user anything directly.",
			)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: buildRoleUserMessageParts(buildDefaultFinalReviewPrompt(inputs, state, candidateAnswer), e.InputAttachments, state.World, true, e.VisualArtifacts),
		},
	}
	res, err := e.generateRoleContent(ctx, RoleVerifier, messages, chains.GetLLMCallOptions(options...)...)
	if err != nil {
		return defaultFinalReview{}, err
	}
	e.emitRoleOutput(ctx, RoleVerifier, roleResponseDebugText(res))
	return parseDefaultFinalReview(contentResponseText(res), candidateAnswer), nil
}

func buildDefaultFinalReviewPrompt(inputs map[string]string, state roleLoopState, candidateAnswer string) string {
	var builder strings.Builder
	builder.WriteString("Default-mode final-answer review.\n")
	builder.WriteString("Decide whether the candidate final answer may end the run, or whether the runtime must continue.\n")
	builder.WriteString("Return only JSON: {\"can_finish\":true|false,\"final_answer\":\"optional revised final answer when can_finish is true\",\"needs_human_handoff\":true|false,\"handoff_reason\":\"authentication|login_method_selection|captcha|verification_code|sensitive_operation|redirect_confirmation|permission_confirmation|black_screen|ambiguous_situation|unsupported_action|stuck|other\",\"handoff_details\":\"specific device-side blocker\",\"suggested_action\":\"what the user should do on the device\",\"reason\":\"brief rationale\"}.\n\n")
	builder.WriteString("Language rule:\n")
	builder.WriteString("- Use ")
	builder.WriteString(defaultFinalReviewLanguageHint(inputs))
	builder.WriteString(" for every free-text JSON value that may be shown to the user or reused in a handoff message, including final_answer, handoff_details, suggested_action, and reason. Keep enum fields such as handoff_reason in English.\n\n")
	builder.WriteString("Rules:\n")
	builder.WriteString("- can_finish=true only when the original request is already satisfied by the evidence, or the candidate is a truthful terminal failure/status answer that needs no more user or device action.\n")
	builder.WriteString("- needs_human_handoff=true when progress is blocked on a human-only action on the target device: private credentials, choosing an account or login path, CAPTCHA, verification, biometric/identity checks, system/app redirect confirmations, permission dialogs, or judgment the tools cannot provide.\n")
	builder.WriteString("- For handoff, set can_finish=false and fill handoff_reason, handoff_details, and suggested_action. The user must complete the action on the device; do not ask for secrets in chat.\n")
	builder.WriteString("- If more tool work is possible and no human handoff is required, set can_finish=false and needs_human_handoff=false.\n")
	writeWorldStateWithOptions(&builder, state.World, worldStatePromptOptions{IncludeLatestScreenshot: true})
	writeRequestContextAndCriteria(&builder, inputs, state)
	writeSessionContext(&builder, inputs)
	writeExecutorEvidence(&builder, state)
	builder.WriteString("\n\nCandidate final answer:\n")
	if answer := strings.TrimSpace(candidateAnswer); answer != "" {
		builder.WriteString(answer)
	} else {
		builder.WriteString("(empty)")
	}
	return strings.TrimSpace(builder.String())
}

func defaultFinalReviewLanguageHint(inputs map[string]string) string {
	if containsHanRunes(strings.TrimSpace(inputs["input"])) {
		return "Simplified Chinese, matching the latest user request"
	}
	return "the same language as the latest user request"
}

func parseDefaultFinalReview(raw, candidateAnswer string) defaultFinalReview {
	var review defaultFinalReview
	if decodeRoleJSON(raw, &review) != nil {
		return defaultFinalReview{
			CanFinish: false,
			Reason:    "default final review returned non-JSON; replanning required",
		}
	}
	review.FinalAnswer = strings.TrimSpace(review.FinalAnswer)
	review.Reason = strings.TrimSpace(review.Reason)
	review.HandoffReason = normalizeHandoffReason(review.HandoffReason)
	review.HandoffDetails = strings.TrimSpace(review.HandoffDetails)
	review.SuggestedAction = strings.TrimSpace(review.SuggestedAction)
	if review.NeedsHumanHandoff {
		review.CanFinish = false
		if review.HandoffDetails == "" {
			review.HandoffDetails = firstNonEmptyString([]string{
				review.Reason,
				"Human action is required before the device task can continue.",
			})
		}
		return review
	}
	if review.CanFinish {
		if review.FinalAnswer == "" {
			review.FinalAnswer = strings.TrimSpace(candidateAnswer)
		}
		return review
	}
	if review.Reason == "" {
		review.Reason = "default final review rejected the candidate final answer"
	}
	return review
}

func (e *roleCollaborativeExecutor) executeDefaultFinalHandoff(
	ctx context.Context,
	state *roleLoopState,
	toolSpecs *ToolSpecs,
	review defaultFinalReview,
) (plannerTurnResult, error) {
	if _, ok := toolSpecs.Lookup(toolHumanHandoffStep); !ok {
		return plannerTurnResult{}, fmt.Errorf("%s is required by default final review but is unavailable", toolHumanHandoffStep)
	}
	payload, _ := json.Marshal(HumanHandoffRequest{
		Reason:          normalizeHandoffReason(review.HandoffReason),
		Details:         firstNonEmptyString([]string{review.HandoffDetails, review.Reason, "Human action is required before the device task can continue."}),
		SuggestedAction: strings.TrimSpace(review.SuggestedAction),
	})
	action := schema.AgentAction{
		Tool:      toolHumanHandoffStep,
		ToolInput: string(payload),
		Log: formatToolActionLog(toolHumanHandoffStep, string(payload), firstNonEmptyString([]string{
			review.SuggestedAction,
			review.HandoffDetails,
			review.Reason,
		}), "\n"),
	}
	return e.executePlannerToolAction(ctx, state, toolSpecs, action)
}

func (e *roleCollaborativeExecutor) generateRoleContent(ctx context.Context, role RoleName, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, roleModelCallTimeout)
	defer cancel()
	callCtx = contextWithTelemetryRole(callCtx, role)
	// Opt this call into raw HTTP logging. Only the main conversation loop
	// flows through here; background tasks (summarization, profile rebuilds,
	// skill merge) share the same model and logger but stay unlogged so their
	// requests don't interleave with the main loop in the shared log file.
	callCtx = contextWithRawHTTPLog(callCtx)
	messages = e.guardMessagesWithinContextWindow(messages, options)
	res, err := e.Model.GenerateContent(callCtx, messages, options...)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s role model call timed out after %s", role, roleModelCallTimeout)
	}
	return res, err
}

func (e *roleCollaborativeExecutor) roleMessages(profile RoleProfile, inputs map[string]string, state roleLoopState, task string) []llms.MessageContent {
	messages := []llms.MessageContent{{
		Role:  llms.ChatMessageTypeSystem,
		Parts: roleSystemMessageParts(profile),
	}}
	var plannerStatePrompt string
	if profile.Name == RolePlanner {
		messages = append(messages, e.ConversationHistory...)
		plannerStatePrompt = buildRoleStatePrompt(profile.Name, inputs, state, task)
		if state.ForceSimpleLoop && strings.TrimSpace(plannerStatePrompt) != "" {
			messages = append(messages, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(plannerStatePrompt)},
			})
		}
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: plannerCurrentUserMessageParts(inputs, e.InputAttachments),
		})
	}

	if profile.Name == RoleExecutor && len(state.StepToolSteps) > 0 {
		scratchpad := (&FunctionAgent{Tools: appendExecutorMetaTools(toolsForRole(RoleExecutor, e.Tools)), ScreenshotPruning: e.ScreenshotPruning, VisualArtifacts: e.VisualArtifacts}).constructFunctionScratchPad(state.StepToolSteps)
		messages = append(messages, scratchpad...)
	} else if roleSeesToolScratchpad(profile.Name) && len(state.ToolSteps) > 0 {
		scratchpad := (&FunctionAgent{Tools: toolsForRole(profile.Name, e.Tools), ScreenshotPruning: e.ScreenshotPruning, VisualArtifacts: e.VisualArtifacts}).constructFunctionScratchPad(state.ToolSteps)
		messages = append(messages, scratchpad...)
	}

	if profile.Name == RolePlanner {
		if !state.ForceSimpleLoop && strings.TrimSpace(plannerStatePrompt) != "" {
			messages = append(messages, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(plannerStatePrompt)},
			})
		}
		for _, steer := range state.SteerMessages {
			messages = append(messages, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(steerHumanMessageContent(steer))},
			})
		}
		return messages
	}

	includeWorldStateLatestScreenshot := profile.Name == RoleVerifier
	statePrompt := buildRoleStatePromptWithOptions(profile.Name, inputs, state, task, roleStatePromptOptions{
		IncludeWorldStateLatestScreenshot: includeWorldStateLatestScreenshot,
	})
	messages = append(messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: buildRoleUserMessageParts(statePrompt, e.InputAttachments, state.World, includeWorldStateLatestScreenshot, e.VisualArtifacts),
	})
	for _, steer := range state.SteerMessages {
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(steerHumanMessageContent(steer))},
		})
	}
	return messages
}

func roleSystemMessageParts(profile RoleProfile) []llms.ContentPart {
	if strings.TrimSpace(profile.SystemPromptDynamicSuffix) == "" || strings.TrimSpace(profile.SystemPromptCachePrefix) == "" {
		return []llms.ContentPart{llms.TextPart(profile.SystemPrompt)}
	}
	return []llms.ContentPart{
		llms.TextPart(profile.SystemPromptCachePrefix),
		llms.TextPart("\n\n" + profile.SystemPromptDynamicSuffix),
	}
}

func plannerCurrentUserMessage(inputs map[string]string) string {
	for _, key := range []string{"input", latestUserInputKey, rootRequestInputKey} {
		if value := inputs[key]; strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func plannerCurrentUserMessageParts(inputs map[string]string, attachments []InputAttachment) []llms.ContentPart {
	parts := []llms.ContentPart{llms.TextPart(plannerCurrentUserMessage(inputs))}
	for _, attachment := range attachments {
		if len(attachment.Data) == 0 {
			continue
		}
		if attachment.Kind == AttachmentKindImage {
			parts = append(parts, buildImagePart(attachment.MIMEType, attachment.Data))
			continue
		}
		parts = append(parts, llms.BinaryPart(attachment.MIMEType, attachment.Data))
	}
	return parts
}

func steerHumanMessageContent(steer RunSteerMessage) string {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		content = "(empty steering message)"
	}
	return content
}

func hasProfileTool(profile RoleProfile, name string) bool {
	for _, tool := range profile.Tools {
		if tool != nil && tool.Name() == name {
			return true
		}
	}
	return false
}

func isPlannerReadOnlyTool(name string, specs *ToolSpecs) bool {
	if specs == nil {
		return false
	}
	spec, ok := specs.Lookup(name)
	return ok && isPlannerReadOnlyToolSpec(spec)
}

func isPlannerReadOnlyToolSpec(spec ToolSpec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Category)) {
	case "observation", "web":
		return true
	case "handoff":
		return true
	case "memory":
		switch spec.Name {
		case "recall_memory", "recall_device_memory", "recall_session_chunks", "inspect_episode":
			return true
		}
	case "skills":
		switch spec.Name {
		case "skill_list", "skill_read":
			return true
		}
	case "system":
		switch spec.Name {
		case "current_time", "weather":
			return true
		}
	}
	return false
}

func roleSeesToolScratchpad(role RoleName) bool {
	return role == RolePlanner
}

func buildRoleStatePrompt(role RoleName, inputs map[string]string, state roleLoopState, task string) string {
	return buildRoleStatePromptWithOptions(role, inputs, state, task, roleStatePromptOptions{})
}

type roleStatePromptOptions struct {
	IncludeWorldStateLatestScreenshot bool
}

func buildRoleStatePromptWithOptions(role RoleName, inputs map[string]string, state roleLoopState, task string, options roleStatePromptOptions) string {
	switch role {
	case RolePlanner:
		return buildPlannerStatePrompt(inputs, state, task)
	case RoleExecutor:
		return buildExecutorStatePrompt(inputs, state, task)
	case RoleVerifier:
		return buildVerifierStatePromptWithOptions(inputs, state, task, worldStatePromptOptions{
			IncludeLatestScreenshot: options.IncludeWorldStateLatestScreenshot,
		})
	default:
		return buildPlannerStatePrompt(inputs, state, task)
	}
}

func buildPlannerStatePrompt(inputs map[string]string, state roleLoopState, task string) string {
	if state.ForceSimpleLoop {
		return buildSimpleLoopPlannerStatePrompt(inputs, state)
	}
	var builder strings.Builder
	builder.WriteString("Planner runtime context (synthetic; not a new user request):\n")
	builder.WriteString(task)
	writeLoopMode(&builder, state)
	writeWorldState(&builder, state.World)
	writeRequestContextAndCriteria(&builder, inputs, state)
	writeSessionContext(&builder, inputs)
	writeTodoState(&builder, state)
	writeTodoReminder(&builder, state)
	writeCurrentPlan(&builder, state)
	writePriorPlanStepResults(&builder, state)
	writeVerifierFeedback(&builder, state)
	return strings.TrimSpace(builder.String())
}

func buildSimpleLoopPlannerStatePrompt(inputs map[string]string, state roleLoopState) string {
	var builder strings.Builder
	writeWorldStateIfPresent(&builder, state.World)
	writeSimpleLoopRootRequest(&builder, inputs)
	writeTodoState(&builder, state)
	writeTodoReminder(&builder, state)
	content := strings.TrimSpace(builder.String())
	if content == "" {
		return ""
	}
	return "Agent runtime context (synthetic; not a new user request):\n" + content
}

func buildExecutorStatePrompt(inputs map[string]string, state roleLoopState, task string) string {
	var builder strings.Builder
	builder.WriteString(task)
	writeWorldState(&builder, state.World)
	writeRequestContextAndCriteria(&builder, inputs, state)
	writeSessionContext(&builder, inputs)
	if history := strings.TrimSpace(inputs["history"]); history != "" {
		builder.WriteString("\n\nConversation history:\n")
		builder.WriteString(history)
	}
	writeCommittedPlanForExecutor(&builder, state)
	writePlannerEvidenceForExecutor(&builder, state)
	writePriorPlanStepResults(&builder, state)
	builder.WriteString("\n\nPlanner-approved next_step:\n")
	if next := strings.TrimSpace(state.NextStep); next != "" {
		builder.WriteString(next)
	} else {
		builder.WriteString("(none)")
	}
	if len(state.StepExecutionResults) > 0 {
		builder.WriteString("\n\nCurrent step progress:\n")
		for i, result := range state.StepExecutionResults {
			writeExecutionResultLine(&builder, i+1, result)
		}
	}
	builder.WriteString("\n\nLoop mode:\n")
	builder.WriteString("- finish_step enters verifier review when this step is ready.\n")
	builder.WriteString("- abort_step enters verifier review when this step is blocked or failed.\n")
	builder.WriteString("- do not return a plain-text final answer; use finish_step or abort_step.\n")
	builder.WriteString("- include durable facts in finish_step key_info when later steps may need them.\n")
	builder.WriteString("- use planner-provided evidence when it already answers part of the current step; do not repeat the same direct tool call unless verification requires it.\n")
	builder.WriteString("- obey tool restrictions and output-format requirements from the original user request.\n")
	builder.WriteString("\nUse the original request, completion criteria, committed plan, and prior results to understand context, but execute only the current next_step.")
	return strings.TrimSpace(builder.String())
}

func buildVerifierStatePrompt(inputs map[string]string, state roleLoopState, task string) string {
	return buildVerifierStatePromptWithOptions(inputs, state, task, worldStatePromptOptions{})
}

func buildVerifierStatePromptWithOptions(inputs map[string]string, state roleLoopState, task string, worldOptions worldStatePromptOptions) string {
	var builder strings.Builder
	builder.WriteString(task)
	writeWorldStateWithOptions(&builder, state.World, worldOptions)
	writeRequestContextAndCriteria(&builder, inputs, state)
	writeSessionContext(&builder, inputs)
	writeCurrentPlan(&builder, state)
	writePriorPlanStepResults(&builder, state)
	writeCurrentStepForVerifier(&builder, state)
	writeStepExecutorEvidence(&builder, state)
	return strings.TrimSpace(builder.String())
}

func writeCurrentStepForVerifier(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nCurrent step under verification:\n")
	totalSteps := len(state.Plan)
	stepIndex := state.PlanStepIndex + 1
	if stepIndex < 1 {
		stepIndex = 1
	}
	isFinal := totalSteps > 0 && state.PlanStepIndex >= totalSteps-1
	builder.WriteString(fmt.Sprintf("- step_index: %d\n", stepIndex))
	builder.WriteString(fmt.Sprintf("- total_committed_steps: %d\n", totalSteps))
	builder.WriteString(fmt.Sprintf("- is_final_committed_step: %v\n", isFinal))
	if next := strings.TrimSpace(state.NextStep); next != "" {
		builder.WriteString("- step_text: ")
		builder.WriteString(next)
		builder.WriteByte('\n')
	} else {
		builder.WriteString("- step_text: (none)\n")
	}
}

func writeStepExecutorEvidence(builder *strings.Builder, state roleLoopState) {
	builder.WriteString("\n\nExecutor activity for this step:\n")
	if outcome := strings.TrimSpace(state.ExecutorStepOutcome); outcome != "" {
		builder.WriteString("- executor_outcome: ")
		builder.WriteString(outcome)
		builder.WriteByte('\n')
	}
	if summary := strings.TrimSpace(state.ExecutorStepSummary); summary != "" {
		builder.WriteString("- executor_summary: ")
		builder.WriteString(summary)
		builder.WriteByte('\n')
	}
	if len(state.ExecutorStepKeyInfo) > 0 {
		builder.WriteString("- executor_key_info: ")
		builder.WriteString(strings.Join(state.ExecutorStepKeyInfo, " | "))
		builder.WriteByte('\n')
	}
	if len(state.StepExecutionResults) == 0 {
		builder.WriteString("(no tool calls in this step)\n")
		return
	}
	for i, result := range state.StepExecutionResults {
		writeExecutionResultLine(builder, i+1, result)
	}
}

func writePriorPlanStepResults(builder *strings.Builder, state roleLoopState) {
	if len(state.PlanStepResults) == 0 {
		return
	}
	builder.WriteString("\n\nPrior step results (compact context from completed or attempted plan steps):\n")
	for i, result := range state.PlanStepResults {
		writePlanStepResultLine(builder, i+1, result)
	}
}

func writeCommittedPlanForExecutor(builder *strings.Builder, state roleLoopState) {
	if len(state.Plan) == 0 {
		return
	}
	builder.WriteString("\n\nCommitted plan:\n")
	for i, step := range state.Plan {
		label := " "
		if i == state.PlanStepIndex {
			label = "*"
		}
		builder.WriteString(fmt.Sprintf("%s %d. %s\n", label, i+1, step))
	}
	builder.WriteString("Only the starred/current step is assigned now. Use the rest of the plan for context only.\n")
}

func writePlannerEvidenceForExecutor(builder *strings.Builder, state roleLoopState) {
	if len(state.PlannerEvidence) == 0 {
		return
	}
	builder.WriteString("\n\nPlanner-provided evidence from before committed execution:\n")
	builder.WriteString("(Use these tool observations as known facts for the current step; avoid repeating them unless they are insufficient.)\n")
	for i, result := range state.PlannerEvidence {
		writeExecutionResultLine(builder, i+1, result)
	}
}

func writePlanStepResultLine(builder *strings.Builder, displayIndex int, result planStepResult) {
	outcome := strings.TrimSpace(result.Outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	builder.WriteString(fmt.Sprintf("%d. step_index=%d outcome=%s", displayIndex, result.StepIndex, outcome))
	if step := compactPromptLine(result.StepText, 220); step != "" {
		builder.WriteString(" step=\"")
		builder.WriteString(step)
		builder.WriteByte('"')
	}
	if summary := compactPromptLine(result.Summary, 320); summary != "" {
		builder.WriteString(" summary=\"")
		builder.WriteString(summary)
		builder.WriteByte('"')
	}
	if len(result.KeyInfo) > 0 {
		builder.WriteString(" key_info=")
		builder.WriteString(compactStringList(result.KeyInfo, 480))
	}
	if result.NeedsReplan {
		builder.WriteString(" needs_replan=true")
		if reason := compactPromptLine(result.VerifierReason, 240); reason != "" {
			builder.WriteString(" verifier_note=\"")
			builder.WriteString(reason)
			builder.WriteByte('"')
		}
	} else if result.CanFinish {
		builder.WriteString(" can_finish=true")
	}
	builder.WriteByte('\n')
}

func compactStringList(values []string, max int) string {
	values = uniqueNonEmpty(values)
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if line := compactPromptLine(value, 160); line != "" {
			parts = append(parts, line)
		}
	}
	return "[" + truncateForLog(strings.Join(parts, " | "), max) + "]"
}

func compactPromptLine(value string, max int) string {
	return truncateForLog(singleLineHistoryText(value), max)
}

func buildRoleUserMessageParts(input string, attachments []InputAttachment, world worldState, includeWorldStateLatestScreenshot bool, artifacts visualArtifactReader) []llms.ContentPart {
	parts := buildUserMessageParts(input, attachments)
	if !includeWorldStateLatestScreenshot || world.LatestScreenshot == nil {
		return parts
	}
	imageBytes := world.LatestScreenshot.Data
	if len(imageBytes) == 0 && strings.TrimSpace(world.LatestScreenshot.ScreenshotRef) != "" && artifacts != nil {
		imageBytes, _ = artifacts.ReadVisualArtifact(world.LatestScreenshot.ScreenshotRef)
	}
	if len(imageBytes) == 0 {
		return parts
	}
	return append(parts, buildImagePart(world.LatestScreenshot.MIMEType(), imageBytes))
}

type worldStatePromptOptions struct {
	IncludeLatestScreenshot bool
}

func writeWorldState(builder *strings.Builder, world worldState) {
	writeWorldStateWithOptions(builder, world, worldStatePromptOptions{})
}

func writeWorldStateIfPresent(builder *strings.Builder, world worldState) {
	if world.DeviceEnvironment == nil && world.Observation == nil {
		return
	}
	writeWorldState(builder, world)
}

func writeWorldStateWithOptions(builder *strings.Builder, world worldState, options worldStatePromptOptions) {
	builder.WriteString("\n\nWorld State (single-agent runtime):\n")
	if world.DeviceEnvironment != nil {
		env := world.DeviceEnvironment
		builder.WriteString("- Device environment: available")
		if platform := strings.TrimSpace(env.Platform); platform != "" {
			builder.WriteString(fmt.Sprintf(" platform=%s", platform))
		}
		if system := strings.TrimSpace(firstNonEmptyPhoneField(env.SystemName, env.Platform)); system != "" {
			builder.WriteString(fmt.Sprintf(" system=%s", system))
		}
		if version := strings.TrimSpace(env.SystemVersion); version != "" {
			builder.WriteString(fmt.Sprintf(" version=%s", version))
		}
		if env.IsTablet != nil {
			builder.WriteString(fmt.Sprintf(" tablet=%t", *env.IsTablet))
		}
		if !env.UpdatedAt.IsZero() {
			builder.WriteString(fmt.Sprintf(" updated_at=%s", env.UpdatedAt.UTC().Format(time.RFC3339)))
		}
		builder.WriteByte('\n')
		appendWorldStateLine(builder, "- Device source: ", joinNonEmpty(", ", env.Source, fieldLabel("captured_at", env.CapturedAt)))
		appendWorldStateLine(builder, "- Device locale: ", joinNonEmpty(", ", env.Locale, fieldLabel("language", env.Language), fieldLabel("region", env.Region), fieldLabel("timezone", env.TimeZone)))
		appendWorldStateLine(builder, "- Device time: ", joinNonEmpty(", ", fieldLabel("utc_offset", env.UTCOffset), intLabel("utc_offset_minutes", env.UTCOffsetMinutes), boolLabel("24h_clock", env.Uses24HourClock)))
		appendWorldStateLine(builder, "- Device hardware: ", joinNonEmpty(", ", fieldLabel("manufacturer", env.Manufacturer), fieldLabel("brand", env.Brand), fieldLabel("model", env.Model), fieldLabel("device_name", env.DeviceName)))
		appendWorldStateLine(builder, "- Device screen: ", formatPhoneScreen(env.Screen))
		appendWorldStateLine(builder, "- Device battery: ", formatPhoneBattery(env.Battery))
		if apps := availableAppNames(env.ThirdPartyApps, 12); len(apps) > 0 {
			builder.WriteString("- Confirmed third-party apps: ")
			builder.WriteString(strings.Join(apps, ", "))
			builder.WriteByte('\n')
		} else if apps := availableAppNames(env.AvailableApps, 12); len(apps) > 0 {
			builder.WriteString("- Confirmed third-party apps: ")
			builder.WriteString(strings.Join(apps, ", "))
			builder.WriteByte('\n')
		}
	}
	if options.IncludeLatestScreenshot {
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
			if options.IncludeLatestScreenshot && obs.ScreenshotStep > 0 {
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

func appendWorldStateLine(builder *strings.Builder, prefix, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	builder.WriteString(prefix)
	builder.WriteString(value)
	builder.WriteByte('\n')
}

func intLabel(label string, value *int) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%s=%d", label, *value)
}

func writeRequestContextAndCriteria(builder *strings.Builder, inputs map[string]string, state roleLoopState) {
	rootRequest := strings.TrimSpace(inputs[rootRequestInputKey])
	if rootRequest == "" {
		rootRequest = strings.TrimSpace(inputs["input"])
	}
	latestUserMessage := strings.TrimSpace(inputs[latestUserInputKey])
	if latestUserMessage == "" {
		latestUserMessage = strings.TrimSpace(inputs["input"])
	}
	builder.WriteString("\n\nOriginal user request / root request:\n")
	builder.WriteString(rootRequest)
	if latestUserMessage != "" && latestUserMessage != rootRequest {
		builder.WriteString("\n\nLatest user message:\n")
		builder.WriteString(latestUserMessage)
	}
	if len(state.CompletionCriteria) == 0 {
		return
	}
	builder.WriteString("\n\nCompletion criteria:\n")
	for _, criterion := range state.CompletionCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			builder.WriteString("- ")
			builder.WriteString(criterion)
			builder.WriteByte('\n')
		}
	}
}

func writeSimpleLoopRootRequest(builder *strings.Builder, inputs map[string]string) {
	currentInput := strings.TrimSpace(plannerCurrentUserMessage(inputs))
	rootRequest := strings.TrimSpace(inputs[rootRequestInputKey])
	if rootRequest == "" {
		rootRequest = currentInput
	}
	if rootRequest == "" || rootRequest == currentInput {
		return
	}
	builder.WriteString("\n\nOriginal user request / root request:\n")
	builder.WriteString(rootRequest)
}

func writeSessionContext(builder *strings.Builder, inputs map[string]string) {
	contextView := strings.TrimSpace(inputs[sessionContextInputKey])
	if contextView == "" {
		return
	}
	builder.WriteString("\n\n")
	builder.WriteString(contextView)
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

func writeTodoState(builder *strings.Builder, state roleLoopState) {
	if state.Todo.Mode == TodoModeNone || len(state.Todo.Items) == 0 {
		return
	}
	builder.WriteString("\n\nCurrent todo state:\n")
	builder.WriteString(fmt.Sprintf("- mode: %s\n", state.Todo.Mode))
	builder.WriteString(fmt.Sprintf("- revision: %d\n", state.Todo.Revision))
	if objective := strings.TrimSpace(state.Todo.Objective); objective != "" {
		builder.WriteString("- objective: ")
		builder.WriteString(objective)
		builder.WriteByte('\n')
	}
	for _, item := range state.Todo.Items {
		marker := " "
		if item.ID == state.Todo.CurrentID {
			marker = "*"
		}
		builder.WriteString(fmt.Sprintf("%s %d. [%s] %s\n", marker, item.StepIndex, item.Status, item.Text))
	}
	switch state.Todo.Mode {
	case TodoModeSimple:
		if state.Phase == phaseDefault {
			builder.WriteString("Use set_todo if this todo state is stale; otherwise continue normally.\n")
		} else {
			builder.WriteString("This todo was created in single-agent mode; a committed plan will replace it.\n")
		}
	case TodoModePlanned:
		builder.WriteString("This todo state is derived from the committed plan and updates through plan execution.\n")
	}
}

func writeTodoReminder(builder *strings.Builder, state roleLoopState) {
	reminder := strings.TrimSpace(state.PendingTodoReminder)
	if reminder == "" {
		return
	}
	builder.WriteString("\n\nTodo reminder:\n")
	builder.WriteString(reminder)
	builder.WriteByte('\n')
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
	if (result.Data == "" && strings.TrimSpace(result.ScreenshotRef) == "") || result.Width <= 0 || result.Height <= 0 {
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

func (s *roleLoopState) recordPlanStepResult(verification verifierDecision) {
	stepText := strings.TrimSpace(s.NextStep)
	if stepText == "" && s.PlanStepIndex >= 0 && s.PlanStepIndex < len(s.Plan) {
		stepText = strings.TrimSpace(s.Plan[s.PlanStepIndex])
	}
	s.PlanStepResults = append(s.PlanStepResults, planStepResult{
		StepIndex:      s.PlanStepIndex + 1,
		StepText:       stepText,
		Outcome:        strings.TrimSpace(s.ExecutorStepOutcome),
		Summary:        strings.TrimSpace(s.ExecutorStepSummary),
		KeyInfo:        uniqueNonEmpty(s.ExecutorStepKeyInfo),
		CanFinish:      verification.CanFinish,
		NeedsReplan:    verification.NeedsReplan,
		VerifierReason: strings.TrimSpace(verification.Reason),
	})
}

func (s roleLoopState) latestExecutionResult() (roleExecutionResult, bool) {
	if len(s.ExecutionResults) == 0 {
		return roleExecutionResult{}, false
	}
	return s.ExecutionResults[len(s.ExecutionResults)-1], true
}

func (s *worldState) UpdateFromStep(step schema.AgentStep, stepNumber int, tools []langtools.Tool, artifacts ...visualArtifactReader) {
	var reader visualArtifactReader
	if len(artifacts) > 0 {
		reader = artifacts[0]
	}
	screenshot, ok := screenshotFromVisualStep(step, stepNumber, tools, reader)
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

func (s *worldState) UpdateDeviceEnvironment(env *PhoneEnvironment) {
	if env == nil {
		return
	}
	cloned := clonePhoneEnvironment(*env)
	now := time.Now()
	s.DeviceEnvironment = &worldDeviceEnvironment{
		PhoneEnvironment: cloned,
		UpdatedAt:        now,
	}
	platform := normalizeObservedWorldState(observedWorldState{Platform: cloned.Platform}).Platform
	if platform == "" {
		return
	}
	if s.Observation == nil {
		s.Observation = &worldStateObservation{
			observedWorldState: observedWorldState{Platform: platform, Confidence: 1},
			SourceRole:         RoleVerifier,
			ObservedAt:         now,
		}
		return
	}
	if strings.TrimSpace(s.Observation.Platform) == "" {
		s.Observation.Platform = platform
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
	visual, ok := parseScreenshotObservation(step.Observation)
	if !ok {
		return worldScreenshot{}, false
	}
	return worldScreenshotFromVisualObservation(step, stepNumber, visual), true
}

func screenshotFromVisualStep(step schema.AgentStep, stepNumber int, tools []langtools.Tool, artifacts visualArtifactReader) (worldScreenshot, bool) {
	visual, ok := (&FunctionAgent{Tools: tools, VisualArtifacts: artifacts}).visualScreenshotObservation(step)
	if !ok {
		return worldScreenshot{}, false
	}
	return worldScreenshotFromVisualObservation(step, stepNumber, visual), true
}

func worldScreenshotFromVisualObservation(step schema.AgentStep, stepNumber int, visual visualScreenshotObservation) worldScreenshot {
	result := visual.Result
	screenshot := worldScreenshot{
		SourceTool:    step.Action.Tool,
		ToolInput:     normalizeToolInput(step.Action.ToolInput),
		ActionOutput:  strings.TrimSpace(result.ActionOutput),
		Width:         result.Width,
		Height:        result.Height,
		Format:        result.Format,
		Size:          result.Size,
		ScreenshotRef: result.ScreenshotRef,
		StepNumber:    stepNumber,
	}
	if strings.TrimSpace(result.ScreenshotRef) == "" {
		screenshot.Data = visual.ImageBytes
	}
	return screenshot
}

func (s worldScreenshot) MIMEType() string {
	return screenshotMIMEType(s.Format)
}

func (s roleLoopState) lastCandidateAnswer() string {
	if summary := strings.TrimSpace(s.ExecutorStepSummary); summary != "" {
		return summary
	}
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
				CompletionCriteria: uniqueNonEmpty([]string{defaultCompletionCriterion}),
				Plan:               uniqueNonEmpty([]string{toolDesc}),
				NextStep:           toolDesc,
				Reason:             "planner incorrectly returned tool_call instead of JSON; extracted description field as next_step",
			}
		}
	}

	// Final fallback: use text content or user input
	text := strings.TrimSpace(finalizeAssistantOutput(raw))
	if text == "" {
		text = strings.TrimSpace(fallbackStep)
	}
	return plannerDecision{
		Objective:          strings.TrimSpace(fallbackStep),
		CompletionCriteria: uniqueNonEmpty([]string{defaultCompletionCriterion}),
		Plan:               uniqueNonEmpty([]string{text}),
		NextStep:           text,
		Reason:             "planner returned non-JSON content",
	}
}

func parseVerifierDecision(raw, fallbackAnswer string) verifierDecision {
	var decision verifierDecision
	if decodeRoleJSON(raw, &decision) == nil {
		decision.FinalAnswer = strings.TrimSpace(decision.FinalAnswer)
		decision.Speech = strings.TrimSpace(decision.Speech)
		decision.Text = strings.TrimSpace(decision.Text)
		if decision.Text != "" && decision.FinalAnswer == "" {
			decision.FinalAnswer = decision.Text
		}
		decision.Reason = strings.TrimSpace(decision.Reason)
		decision.ObservedState = normalizeObservedWorldState(decision.ObservedState)
		if decision.NeedsReplan {
			decision.CanFinish = false
			if decision.Reason == "" {
				decision.Reason = "verifier requested replan"
			}
			return decision
		}
		if decision.CanFinish && decision.FinalAnswer == "" {
			decision.CanFinish = false
			decision.NeedsReplan = true
			if decision.Reason == "" {
				decision.Reason = "verifier approved finish without final_answer"
			}
		}
		return decision
	}

	if !hasTaggedTTSOutput(raw) {
		return verifierDecision{
			CanFinish:   false,
			NeedsReplan: true,
			Reason:      "verifier returned non-JSON content without tagged final output",
		}
	}
	return verifierDecision{
		CanFinish:   true,
		FinalAnswer: finalizeAssistantOutput(raw),
		Reason:      "verifier returned tagged final output",
	}
}

func hasTaggedTTSOutput(content string) bool {
	return extractTTSText(content) != ""
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
	seenLines := map[string]bool{}
	addPart := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		parts = append(parts, part)
		for _, line := range strings.Split(part, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				seenLines[line] = true
			}
		}
	}
	addToolCall := func(name, input string) {
		line := fmt.Sprintf("tool_call: %s input=%s", name, input)
		if seenLines[strings.TrimSpace(line)] {
			return
		}
		addPart(line)
	}
	if text := strings.TrimSpace(choice.Content); text != "" {
		addPart(normalizeRoleOutputContent(text))
	}
	for _, toolCall := range choice.ToolCalls {
		if toolCall.FunctionCall == nil {
			continue
		}
		addToolCall(toolCall.FunctionCall.Name, toolCall.FunctionCall.Arguments)
	}
	if choice.FuncCall != nil {
		addToolCall(choice.FuncCall.Name, choice.FuncCall.Arguments)
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

type finalStreamingController interface {
	EnableFinalStreaming(context.Context)
	DisableFinalStreaming(context.Context)
	HasFinalStreamingToken(context.Context) bool
}

type providerFinalStreamingController interface {
	ProviderFinalStreamingEnabled() bool
}

type finalStreamingDecisionLogger interface {
	LogFinalStreamingDecision(context.Context, RoleName, bool, string)
}

func (e *roleCollaborativeExecutor) withFinalStreaming(ctx context.Context, fn func() (*llms.ContentResponse, error)) (*llms.ContentResponse, error) {
	controller, ok := e.CallbacksHandler.(finalStreamingController)
	if !ok {
		return fn()
	}
	controller.EnableFinalStreaming(ctx)
	defer controller.DisableFinalStreaming(ctx)
	return fn()
}

func (e *roleCollaborativeExecutor) finalStreamingCallOptions(options []llms.CallOption) []llms.CallOption {
	controller, ok := e.CallbacksHandler.(providerFinalStreamingController)
	if !ok || !controller.ProviderFinalStreamingEnabled() {
		return options
	}
	streamer, ok := e.CallbacksHandler.(streamingFuncHandler)
	if !ok {
		return options
	}
	var current llms.CallOptions
	for _, option := range options {
		option(&current)
	}
	if current.StreamingFunc != nil {
		return options
	}
	callOptions := append([]llms.CallOption{}, options...)
	callOptions = append(callOptions, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		streamer.HandleStreamingFunc(ctx, chunk)
		return nil
	}))
	return callOptions
}

func (e *roleCollaborativeExecutor) streamFinalAnswer(ctx context.Context, finalAnswer string) {
	if finalAnswer == "" {
		return
	}
	if controller, ok := e.CallbacksHandler.(finalStreamingController); ok {
		if controller.HasFinalStreamingToken(ctx) {
			return
		}
		controller.EnableFinalStreaming(ctx)
		defer controller.DisableFinalStreaming(ctx)
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

func executorInputsToString(inputValues map[string]any) (map[string]string, error) {
	inputs := make(map[string]string, len(inputValues))
	for key, value := range inputValues {
		valueStr, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s", agents.ErrExecutorInputNotString, key)
		}
		inputs[key] = valueStr
	}
	return inputs, nil
}
