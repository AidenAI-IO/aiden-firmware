package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
)

const agentLoopOutputKey = "output"

var errSteerInterruptToolCancel = errors.New("steer interrupt tool cancel")

type AgentLoop struct {
	Model                  llms.Model
	Profile                RoleProfile
	Memory                 schema.Memory
	CallbacksHandler       callbacks.Handler
	MaxIterations          int
	Recorder               *EpisodeRecorder
	ScreenshotPruning      ScreenshotPruningConfig
	EnvironmentBridge      *EnvironmentBridgeClient
	EnvironmentBridgeTools []string
	SteerInterrupt         func() <-chan struct{}
	SteerProvider          func(context.Context) (RunSteerMessage, bool)
	SteerWaiter            func(context.Context) (RunSteerMessage, bool, error)
	TerminationPolicy      *TerminationPolicy
	DevicePlatform         string
	PointerMode            string
	contextManager         *contextmanager.ContextManager
}

func NewAgentLoop(
	model llms.Model,
	profile RoleProfile,
	memory schema.Memory,
	maxIterations int,
	callbacksHandler callbacks.Handler,
	recorder *EpisodeRecorder,
	screenshotPruning ScreenshotPruningConfig,
	contextManager *contextmanager.ContextManager,
) *AgentLoop {
	if contextManager == nil {
		log.Fatalf("context manager is nil")
	}
	return &AgentLoop{
		Model:             model,
		Profile:           profile,
		Memory:            memory,
		CallbacksHandler:  callbacksHandler,
		MaxIterations:     maxIterations,
		Recorder:          recorder,
		ScreenshotPruning: screenshotPruning,
		contextManager:    contextManager,
	}
}

func (l *AgentLoop) Run(ctx context.Context, input string, options ...chains.ChainCallOption) (string, error) {
	llmExecutor := executor.NewLLMExecutor(l.Model, l.contextManager)
	llmExecutor.SetMessageGuard(guardMessagesWithinContextWindow)

	agentTools := l.Profile.Tools
	toolSpecs := NewToolSpecs(agentTools)
	parser := &FunctionAgent{
		Tools:             agentTools,
		OutputKey:         agentLoopOutputKey,
		ScreenshotPruning: l.ScreenshotPruning,
	}
	callOptions := chains.GetLLMCallOptions(options...)
	policy := l.loopGuardPolicy()
	transientMessages := newTransientMessageScope(l.contextManager)
	defer transientMessages.Clear()

	for i := 0; i < l.MaxIterations; i++ {
		if decision := policy.CheckBeforeIteration(ctx, i+1, l.MaxIterations); decision.Stop {
			answer, err := l.stopWithDecision(ctx, policy, decision)
			return answer, err
		}
		answer, done, err := l.runIteration(ctx, i+1, callOptions, llmExecutor, parser, toolSpecs, policy, transientMessages)
		if err != nil {
			return "", err
		}
		if done {
			return answer, nil
		}
	}

	return l.stopWithDecision(ctx, policy, policy.BudgetExhausted("max_iterations budget exhausted"))
}

func (l *AgentLoop) loopGuardPolicy() *TerminationPolicy {
	if l != nil && l.TerminationPolicy != nil {
		return l.TerminationPolicy
	}
	return NewTerminationPolicy(DefaultTerminationPolicyConfig())
}

func (l *AgentLoop) runIteration(ctx context.Context, iteration int, callOptions []llms.CallOption, llmExecutor *executor.LLMExecutor, parser *FunctionAgent, toolSpecs *ToolSpecs, policy *TerminationPolicy, transientMessages *transientMessageScope) (string, bool, error) {
	iterationStartTime := time.Now()
	toolCallsInIteration := 0
	if l.Recorder != nil {
		l.Recorder.RecordEvent(TaskEpisodeEvent{
			Type: runEventIterationStart,
			Ts:   iterationStartTime.Format(time.RFC3339Nano),
			Metadata: map[string]interface{}{
				"iteration": iteration,
			},
		})
		defer func() {
			iterDuration := time.Since(iterationStartTime).Milliseconds()
			l.Recorder.RecordEvent(TaskEpisodeEvent{
				Type:       runEventIterationEnd,
				Ts:         time.Now().Format(time.RFC3339Nano),
				DurationMs: &iterDuration,
				Metadata: map[string]interface{}{
					"iteration":  iteration,
					"tool_calls": toolCallsInIteration,
				},
			})
		}()
	}

	// Problem 4: Check for pending steer before LLM call
	if _, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor); err != nil {
		return "", false, err
	} else if hasPending {
		policy.ResetForSteer()
		return "", false, nil
	}

	// Problem 4: Support interrupting LLM call during generation
	llmCtx, llmCancel := context.WithCancelCause(ctx)
	defer llmCancel(nil)

	if l.SteerInterrupt != nil {
		interruptCh := l.SteerInterrupt()
		if interruptCh != nil {
			done := make(chan struct{})
			go func() {
				select {
				case <-interruptCh:
					llmCancel(errSteerInterruptToolCancel)
				case <-llmCtx.Done():
				case <-done:
				}
			}()
			defer close(done)
		}
	}

	turnOptions := append([]llms.CallOption{}, callOptions...)
	turnOptions = append(turnOptions, llms.WithTools(parser.toolsAsLLM()))
	contentResp, err := llmExecutor.GenerateContent(llmCtx, turnOptions...)
	if err != nil {
		// If LLM was canceled due to interrupt, check for pending steer
		if errors.Is(err, context.Canceled) || errors.Is(err, errSteerInterruptToolCancel) {
			if _, hasPending, persistErr := l.consumeAndPersistSteer(ctx, llmExecutor); persistErr != nil {
				return "", false, persistErr
			} else if hasPending {
				policy.ResetForSteer()
				return "", false, nil
			}
			if l.SteerWaiter != nil {
				waitCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				waitedSteer, hasText, waitErr := l.SteerWaiter(waitCtx)
				cancel()
				if waitErr == nil && hasText {
					if persistErr := l.persistSteer(ctx, llmExecutor, waitedSteer); persistErr != nil {
						return "", false, persistErr
					}
					policy.ResetForSteer()
					return "", false, nil
				}
			}
		}
		return "", false, err
	}
	l.emitRoleOutput(ctx, roleResponseDebugText(contentResp))
	if answer := l.touchPointerModeMismatchContentFinalAnswer(contentResp); answer != "" {
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, true, err
	}

	actions, finish, err := parser.ParseOutput(contentResp)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		if err := llmExecutor.AppendMessage(contextmanager.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0])); err != nil {
			return "", false, err
		}
		decision := policy.RecordParseFailure()
		if decision.Stop {
			return l.finishStopDecision(ctx, policy, decision)
		}
		l.applyLoopGuardDecision(decision, transientMessages)

		// Problem 4: Check for pending steer after parse failure (continue iteration)
		if steer, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor); err != nil {
			return "", false, err
		} else if hasPending {
			policy.ResetForSteer()
			log.Printf("[steer] LLM parse failed, steer injected (length=%d)\n", len(steer.Content))
			return "", false, nil
		}

		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if finish != nil {
		// Don't check steer after finish - let this turn complete normally.
		// Steer will be processed as a new turn.
		if err := llmExecutor.AppendMessage(contextmanager.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0])); err != nil {
			return "", false, err
		}
		answer, _ := finish.ReturnValues[agentLoopOutputKey].(string)
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return "", false, agents.ErrAgentNoReturn
		}
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, true, err
	}
	if len(actions) == 0 {
		return "", false, agents.ErrAgentNoReturn
	}

	// Problem 4: Check for pending steer after LLM returns actions (before tool execution)
	if steer, hasPending := l.checkPendingSteer(ctx); hasPending {
		policy.ResetForSteer()
		// Append LLM response first, then steer, so context order is correct:
		// assistant(tool_call) → user(steer)
		if err := llmExecutor.AppendMessage(contextmanager.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0])); err != nil {
			return "", false, err
		}
		if err := l.persistSteer(ctx, llmExecutor, steer); err != nil {
			return "", false, err
		}
		log.Printf("[steer] LLM action interrupted, steer injected (length=%d)\n", len(steer.Content))
		return "", false, nil
	}

	action := actions[0]
	if err := llmExecutor.AppendMessage(contextmanager.ConvertChoiceToContextManagerMessage(choiceWithOnlyToolCall(*contentResp.Choices[0], action.ToolID))); err != nil {
		return "", false, err
	}
	toolExecution := l.executeToolCall(ctx, ToolCallExecution{
		Specs:  toolSpecs,
		Action: action,
		Before: func(ctx context.Context, call ToolCall) (ToolResult, bool) {
			result, allowed := policy.BeforeToolCall(call.Spec.Name, call.Input)
			if !allowed {
				return result, false
			}
			return DefaultBeforeToolCall(ctx, call)
		},
		Callback:               l.CallbacksHandler,
		EnvironmentBridge:      l.EnvironmentBridge,
		EnvironmentBridgeTools: l.EnvironmentBridgeTools,
	})
	if l.Recorder != nil {
		l.Recorder.RecordExecution(ToolCallExecutionResult{
			Call:   toolExecution.Call,
			Step:   toolExecution.Step,
			Result: toolExecution.Result,
			Error:  toolExecution.Error,
		})
	}
	toolCallsInIteration++
	appendErr := appendToolExecutionMessages(llmExecutor, parser, toolExecution.Step)
	if toolExecution.Error != nil {
		if appendErr != nil {
			return "", false, errors.Join(toolExecution.Error, appendErr)
		}
		// If the tool was canceled by steering, consume an already queued steer or
		// briefly wait for the out-of-band capture to finish.
		if errors.Is(toolExecution.Error, context.Canceled) {
			steer, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor)
			if err != nil {
				return "", false, err
			}
			if !hasPending && l.SteerWaiter != nil {
				waitCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				waitedSteer, hasText, waitErr := l.SteerWaiter(waitCtx)
				cancel()
				if waitErr == nil && hasText {
					if err := l.persistSteer(ctx, llmExecutor, waitedSteer); err != nil {
						return "", false, err
					}
					steer = waitedSteer
					hasPending = true
				}
			}
			if hasPending {
				policy.ResetForSteer()
				log.Printf("[steer] tool canceled but pending steer exists (length=%d), continuing iteration\n", len(steer.Content))
				return "", false, nil
			}
		}

		decision := policy.AfterToolCall(
			toolExecution.Call.Spec.Name,
			toolExecution.Call.Input,
			toolExecution.Error.Error(),
			true,
		)
		if decision.Stop {
			return l.finishStopDecision(ctx, policy, decision)
		}
		l.applyLoopGuardDecision(decision, transientMessages)
		return "", false, nil
	}
	if appendErr != nil {
		return "", false, appendErr
	}
	if answer := l.touchPointerModeMismatchFinalAnswer(toolExecution.Step); answer != "" {
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, true, err
	}

	// Record tool execution in termination policy BEFORE checking steer
	// This ensures all tool calls are tracked for loop detection
	decision := policy.AfterToolCall(
		toolExecution.Call.Spec.Name,
		toolExecution.Call.Input,
		toolExecution.Step.Observation,
		toolExecution.Result.IsError(),
	)

	// Check for pending steer after tool execution (soft interrupt decision point)
	if _, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor); err != nil {
		return "", false, err
	} else if hasPending {
		policy.ResetForSteer()
		// Continue iteration - LLM will see tool result + steer content and decide
		return "", false, nil
	}

	if isRunPausingTool(toolExecution.Call.Action.Tool) && !toolExecution.Result.IsError() {
		answer := runPausingToolFinalAnswer(&toolExecution.Step)
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, true, err
	}
	if decision.Stop {
		return l.finishStopDecision(ctx, policy, decision)
	}
	l.applyLoopGuardDecision(decision, transientMessages)

	return "", false, nil
}

func (l *AgentLoop) finishStopDecision(ctx context.Context, policy *TerminationPolicy, decision TerminationDecision) (string, bool, error) {
	// Problem 5: When terminating with pending steer, inject steer as new user message
	// instead of wrapping it as final answer. Apply to all termination reasons except
	// external cancellation (where the context is already done and steer is irrelevant).
	if decision.Reason != StopReasonExternal {
		// Note: executor is not available here, but consumeAndPersistSteer handles nil executor
		if _, hasPending, err := l.consumeAndPersistSteer(ctx, nil); err != nil {
			return "", false, err
		} else if hasPending {
			policy.ResetForSteer()
			// Don't terminate - steer was consumed and persisted, continue iteration
			return "", false, nil
		}
	}
	answer, err := l.stopWithDecision(ctx, policy, decision)
	return answer, true, err
}

func (l *AgentLoop) applyLoopGuardDecision(decision TerminationDecision, transientMessages *transientMessageScope) {
	if transientMessages == nil || strings.TrimSpace(decision.Notice) == "" {
		return
	}
	transientMessages.Append(contextmanager.Message{
		Role:    contextmanager.MessageRoleNotice,
		Content: decision.Notice,
	})
}

func (l *AgentLoop) stopWithDecision(ctx context.Context, policy *TerminationPolicy, decision TerminationDecision) (string, error) {
	if decision.Reason == StopReasonExternal && ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	lastTool := ""
	if policy != nil {
		lastTool = policy.lastToolName
	}
	answer := formatLoopGuardStopMessage(decision, lastTool)
	if l != nil && l.Recorder != nil {
		l.Recorder.RecordEvent(TaskEpisodeEvent{
			Type: runEventLoopGuardStop,
			Ts:   time.Now().Format(time.RFC3339Nano),
			Metadata: map[string]interface{}{
				"reason":  string(decision.Reason),
				"message": decision.Message,
				"tier":    int(decision.Tier),
			},
		})
		l.Recorder.RecordDefaultFinish(answer)
	}
	return l.finishRun(ctx, answer)
}

type transientMessageScope struct {
	contextManager *contextmanager.ContextManager
	remove         []func()
}

func newTransientMessageScope(contextManager *contextmanager.ContextManager) *transientMessageScope {
	return &transientMessageScope{contextManager: contextManager}
}

func (s *transientMessageScope) Append(message contextmanager.Message) {
	if s == nil || s.contextManager == nil {
		return
	}
	s.remove = append(s.remove, s.contextManager.AppendTransientMessage(message))
}

func (s *transientMessageScope) Clear() {
	if s == nil {
		return
	}
	for _, remove := range s.remove {
		remove()
	}
	s.remove = nil
}

func loadAgentLoopInputs(ctx context.Context, memory schema.Memory, input string) (map[string]string, error) {
	inputValues := map[string]any{"input": input}
	if memory != nil {
		variables, err := memory.LoadMemoryVariables(ctx, inputValues)
		if err != nil {
			return nil, err
		}
		for key, value := range variables {
			if text, ok := value.(string); ok {
				inputValues[key] = text
			}
		}
	}
	result := map[string]string{"input": input}
	for _, key := range []string{"history"} {
		if value, ok := inputValues[key].(string); ok {
			result[key] = value
		} else {
			result[key] = ""
		}
	}
	if strings.TrimSpace(result["input"]) == "" {
		result["input"] = input
	}
	return result, nil
}

func (l *AgentLoop) checkPendingSteer(ctx context.Context) (RunSteerMessage, bool) {
	if l == nil || l.SteerProvider == nil {
		return RunSteerMessage{}, false
	}
	return l.SteerProvider(ctx)
}

// consumeAndPersistSteer checks for pending steer, and if found, executes the
// three-step persistence pipeline: append to context manager, persist to memory,
// and emit event. Returns the steer message, whether one was found, and any error.
func (l *AgentLoop) consumeAndPersistSteer(
	ctx context.Context,
	executor *executor.LLMExecutor,
) (RunSteerMessage, bool, error) {
	steer, hasPending := l.checkPendingSteer(ctx)
	if !hasPending {
		return RunSteerMessage{}, false, nil
	}
	if err := l.persistSteer(ctx, executor, steer); err != nil {
		return steer, false, err
	}
	return steer, true, nil
}

func (l *AgentLoop) persistSteer(ctx context.Context, executor *executor.LLMExecutor, steer RunSteerMessage) error {
	// Step 1: Append to context manager
	if executor != nil {
		if err := executor.AppendMessage(contextmanager.Message{
			Role:    contextmanager.MessageRoleUser,
			Content: steer.Content,
		}); err != nil {
			return err
		}
	} else if l.contextManager != nil {
		if err := l.contextManager.AppendMessage(contextmanager.Message{
			Role:    contextmanager.MessageRoleUser,
			Content: steer.Content,
		}); err != nil {
			return err
		}
	}

	// Step 2: Persist to memory
	if appender, ok := l.Memory.(steerConversationAppender); ok {
		if err := appender.AppendSteerOnly(ctx, steer); err != nil {
			return err
		}
	}

	// Step 3: Emit event
	if l.CallbacksHandler != nil {
		if handler, ok := l.CallbacksHandler.(interface {
			HandleSteerMessage(context.Context, RunSteerMessage)
		}); ok {
			handler.HandleSteerMessage(ctx, steer)
		}
	}

	return nil
}

func formatSteerInterruptMessage(steer RunSteerMessage) string {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		return "User interrupted the current task."
	}
	return fmt.Sprintf("User interrupted: %s", content)
}

func (l *AgentLoop) executeToolCall(ctx context.Context, execution ToolCallExecution) ToolCallExecutionResult {
	interruptCh := (<-chan struct{})(nil)
	if l != nil && l.SteerInterrupt != nil {
		interruptCh = l.SteerInterrupt()
	}
	if interruptCh == nil {
		return executeToolCall(ctx, execution)
	}

	toolCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-interruptCh:
			cancel(errSteerInterruptToolCancel)
		case <-toolCtx.Done():
		case <-done:
		}
	}()
	result := executeToolCall(toolCtx, execution)
	close(done)
	cancel(nil)

	// If tool was canceled due to interrupt, check for pending steer before returning error
	// This prevents losing user's new instruction when tool is cancelable
	if result.Error != nil && ctx.Err() == context.Canceled {
		if steer, hasPending := l.checkPendingSteer(ctx); hasPending {
			// Tool was interrupted but we have a new steer to process
			// Return the error but the steer is preserved for next iteration
			log.Printf("[steer] tool canceled but pending steer exists (length=%d), will be processed\n", len(steer.Content))
		}
	}

	return result
}

func (l *AgentLoop) finishRun(ctx context.Context, finalAnswer string) (string, error) {
	finalAnswer = strings.TrimSpace(finalAnswer)
	if l.CallbacksHandler != nil {
		l.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
			ReturnValues: map[string]any{agentLoopOutputKey: finalAnswer},
			Log:          finalAnswer,
		})
	}
	return finalAnswer, nil
}

func (l *AgentLoop) emitRoleOutput(ctx context.Context, content string) {
	content = strings.TrimSpace(content)
	if content == "" || l.CallbacksHandler == nil {
		return
	}
	if handler, ok := l.CallbacksHandler.(roleOutputHandler); ok {
		handler.HandleRoleOutput(ctx, "agent", content)
	}
}

type roleOutputHandler interface {
	HandleRoleOutput(ctx context.Context, role, content string)
}

func choiceWithOnlyToolCall(choice llms.ContentChoice, toolID string) llms.ContentChoice {
	if len(choice.ToolCalls) == 0 {
		if choice.FuncCall != nil {
			choice.ToolCalls = []llms.ToolCall{{
				ID:           ensureToolCallID(toolID, 0),
				Type:         "function",
				FunctionCall: choice.FuncCall,
			}}
		}
		return choice
	}
	toolID = strings.TrimSpace(toolID)
	var firstValid *llms.ToolCall
	for i := range choice.ToolCalls {
		call := choice.ToolCalls[i]
		if call.FunctionCall == nil {
			continue
		}
		if firstValid == nil {
			firstValid = &call
		}
		if toolID != "" && strings.TrimSpace(call.ID) == toolID {
			choice.ToolCalls = []llms.ToolCall{call}
			choice.FuncCall = call.FunctionCall
			return choice
		}
	}
	if firstValid == nil {
		choice.ToolCalls = nil
		choice.FuncCall = nil
		return choice
	}
	choice.ToolCalls = []llms.ToolCall{*firstValid}
	choice.FuncCall = firstValid.FunctionCall
	return choice
}

type roleExecutionResult struct {
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	ToolError       *ToolError
	CandidateAnswer string
	ToolDuration    time.Duration
}

func roleResponseDebugText(res *llms.ContentResponse) string {
	if res == nil || len(res.Choices) == 0 || res.Choices[0] == nil {
		return ""
	}
	choice := res.Choices[0]
	parts := make([]string, 0, 2)
	if reasoning := strings.TrimSpace(choice.ReasoningContent); reasoning != "" {
		parts = append(parts, reasoning)
	}
	if content := strings.TrimSpace(choice.Content); content != "" {
		parts = append(parts, content)
	}
	if len(choice.ToolCalls) > 0 {
		names := make([]string, 0, len(choice.ToolCalls))
		for _, call := range choice.ToolCalls {
			if call.FunctionCall != nil && strings.TrimSpace(call.FunctionCall.Name) != "" {
				names = append(names, call.FunctionCall.Name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, "tool_calls="+strings.Join(names, ","))
		}
	}
	return strings.Join(parts, "\n")
}

func appendToolExecutionMessages(llmExecutor *executor.LLMExecutor, parser *FunctionAgent, step schema.AgentStep) error {
	if llmExecutor == nil {
		return fmt.Errorf("llm executor is nil")
	}

	toolContent := step.Observation
	var followups []llms.MessageContent
	if parser != nil && parser.isVisualObservationTool(step.Action.Tool) {
		if content, visualFollowups := parser.observationMessagesForStep(step, true); len(visualFollowups) > 0 {
			toolContent = content
			followups = visualFollowups
		}
	}

	if err := llmExecutor.AppendMessage(toolResultMessage(
		step.Action.ToolID,
		step.Action.Tool,
		toolContent,
	)); err != nil {
		return fmt.Errorf("failed to append tool result message: %w", err)
	}
	for _, followup := range followups {
		if err := llmExecutor.AppendMessage(visualFollowupMessageFromLLMContent(llmExecutor.ContextManager(), followup)); err != nil {
			return fmt.Errorf("failed to append visual followup message: %w", err)
		}
	}
	return nil
}

func (l *AgentLoop) touchPointerModeMismatchFinalAnswer(step schema.AgentStep) string {
	if !toolNameEqual(step.Action.Tool, "touch_gesture") {
		return ""
	}
	var payload struct {
		ScreenChanged *bool `json:"screen_changed"`
	}
	if err := json.Unmarshal([]byte(step.Observation), &payload); err != nil {
		return ""
	}
	if payload.ScreenChanged == nil || *payload.ScreenChanged {
		return ""
	}

	platform := strings.ToLower(strings.TrimSpace(l.DevicePlatform))
	pointerMode := strings.ToLower(strings.TrimSpace(l.PointerMode))
	return touchPointerModeMismatchGuidance(platform, pointerMode)
}

func (l *AgentLoop) touchPointerModeMismatchContentFinalAnswer(contentResp *llms.ContentResponse) string {
	if contentResp == nil || len(contentResp.Choices) == 0 || contentResp.Choices[0] == nil {
		return ""
	}
	choice := contentResp.Choices[0]
	if !choiceHasToolCall(choice, "touch_gesture") {
		return ""
	}
	content := strings.TrimSpace(choice.Content)
	if content == "" {
		return ""
	}
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "hid.pointer_mode") {
		return ""
	}
	if !strings.Contains(lower, "stop operation here") && !strings.Contains(lower, "touch mode likely does not match") {
		return ""
	}
	platform := strings.ToLower(strings.TrimSpace(l.DevicePlatform))
	pointerMode := strings.ToLower(strings.TrimSpace(l.PointerMode))
	return touchPointerModeMismatchGuidance(platform, pointerMode)
}

func choiceHasToolCall(choice *llms.ContentChoice, toolName string) bool {
	if choice == nil {
		return false
	}
	for _, call := range choice.ToolCalls {
		if call.FunctionCall != nil && toolNameEqual(call.FunctionCall.Name, toolName) {
			return true
		}
	}
	return false
}

func touchPointerModeMismatchGuidance(platform, pointerMode string) string {
	switch {
	case platform == "android" && pointerMode == "absolute":
		return `touch_gesture produced no visible screen change, and the device is configured as Android with hid.pointer_mode="absolute". Stop operation here because the touch mode likely does not match the target. Please switch hid.pointer_mode to "touchscreen", restart the agent, and retry.`
	case (platform == "ios" || platform == "ipados") && pointerMode == "touchscreen":
		return `touch_gesture produced no visible screen change, and the device is configured as iOS with hid.pointer_mode="touchscreen". Stop operation here because the touch mode likely does not match the target. Please switch hid.pointer_mode to "absolute", restart the agent, and retry.`
	default:
		return ""
	}
}

func toolNameEqual(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func isRunPausingTool(name string) bool {
	return toolNameEqual(name, toolWaitForWakeup) || toolNameEqual(name, toolHumanHandoffStep)
}

func runPausingToolFinalAnswer(step *schema.AgentStep) string {
	if step != nil && toolNameEqual(step.Action.Tool, toolHumanHandoffStep) {
		return humanHandoffFinalAnswer(step)
	}
	return waitForWakeupFinalAnswer(step)
}

func humanHandoffFinalAnswer(step *schema.AgentStep) string {
	if step != nil {
		if content := toolContentFromAction(step.Action); content != "" {
			return content
		}
		if message := humanHandoffMessageFromInput(step.Action.ToolInput); message != "" {
			return message
		}
		if message := humanHandoffMessageFromObservation(step.Observation); message != "" {
			return message
		}
	}
	return waitForWakeupFinalAnswer(step)
}

func humanHandoffMessageFromInput(input string) string {
	var req HumanHandoffRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &req); err != nil {
		return ""
	}
	return firstNonEmptyString([]string{req.SuggestedAction, req.Details})
}

func humanHandoffMessageFromObservation(observation string) string {
	var payload struct {
		Message         string `json:"message"`
		SuggestedAction string `json:"suggested_action"`
		Details         string `json:"details"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(observation)), &payload); err != nil {
		return ""
	}
	return firstNonEmptyString([]string{payload.Message, payload.SuggestedAction, payload.Details})
}

func waitForWakeupFinalAnswer(step *schema.AgentStep) string {
	if step != nil {
		if content := toolContentFromAction(step.Action); content != "" {
			return content
		}
		if message := toolObservationMessage(step.Observation); message != "" {
			return message
		}
	}
	return "I will wait for the next wakeup."
}

func toolObservationMessage(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" || !strings.HasPrefix(observation, "{") {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(observation), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Message)
}
