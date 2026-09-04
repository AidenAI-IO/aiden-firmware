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
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
)

const agentLoopOutputKey = "output"

var errSteerInterruptToolCancel = errors.New("steer interrupt tool cancel")

type ContextBudgetGuard func(context.Context, *contextmanager.ContextManager, llms.CallOptions) (*contextmanager.ContextManager, bool, error)

type iterationOutcome uint8

const (
	iterationContinue iterationOutcome = iota
	iterationDone
	iterationRestartBudget
)

type AgentLoop struct {
	Model                    model.Model
	Profile                  RoleProfile
	SteerRecorder            steerConversationRecorder
	CallbacksHandler         callbacks.Handler
	MaxIterations            int
	Recorder                 *EpisodeRecorder
	ScreenshotPruning        executor.ScreenshotPruningConfig
	SteerInterrupt           func() <-chan struct{}
	SteerProvider            func(context.Context) (RunSteerMessage, bool)
	SteerWaiter              func(context.Context) (RunSteerMessage, bool, error)
	TerminationPolicy        *TerminationPolicy
	DevicePlatform           string
	PointerMode              string
	ToolResultObserver       ToolResultObserver
	ToolResultPolicy         ToolResultPolicy
	ContextBudgetGuard       ContextBudgetGuard
	ContextOverflowRecovery  func(context.Context, *contextmanager.ContextManager) (*contextmanager.ContextManager, bool, error)
	toolExecutionHookFactory func() toolExecutionHookHandler
	contextManager           *contextmanager.ContextManager
}

func NewAgentLoop(
	model model.Model,
	profile RoleProfile,
	maxIterations int,
	callbacksHandler callbacks.Handler,
	recorder *EpisodeRecorder,
	screenshotPruning executor.ScreenshotPruningConfig,
	contextManager *contextmanager.ContextManager,
) *AgentLoop {
	if contextManager == nil {
		log.Fatalf("context manager is nil")
	}
	return &AgentLoop{
		Model:             model,
		Profile:           profile,
		CallbacksHandler:  callbacksHandler,
		MaxIterations:     maxIterations,
		Recorder:          recorder,
		ScreenshotPruning: screenshotPruning,
		ToolResultPolicy:  NewToolResultPolicy(),
		contextManager:    contextManager,
	}
}

func (l *AgentLoop) outboundTransforms() []executor.OutboundMessageTransform {
	modelName := l.Model.Spec().Name
	modelProvider := l.Model.Spec().Provider
	return []executor.OutboundMessageTransform{
		executor.AnthropicScreenshotPruner{
			Enabled: IsAnthropicModel(modelProvider, modelName),
			Config:  l.ScreenshotPruning,
		},
	}
}

func (l *AgentLoop) Run(ctx context.Context, input string, options ...chains.ChainCallOption) (string, error) {
	llmExecutor := executor.NewLLMExecutor(l.Model, l.contextManager, l.outboundTransforms()...)

	agentTools := l.Profile.Tools
	toolSpecs := NewToolSpecs(agentTools)
	parser := &FunctionAgent{
		Tools:     agentTools,
		OutputKey: agentLoopOutputKey,
	}
	callOptions := chains.GetLLMCallOptions(options...)
	var toolExecutionHooks toolExecutionHookHandler
	if l.toolExecutionHookFactory != nil {
		toolExecutionHooks = l.toolExecutionHookFactory()
	}
	policy := l.loopGuardPolicy()
	contextOverflowRecoveryUsed := false

restartBudget:
	for {
		for i := 0; i < l.MaxIterations; i++ {
			if decision := policy.CheckBeforeIteration(ctx, i+1, l.MaxIterations); decision.Stop {
				answer, done, err := l.stopWithSteerCheck(ctx, llmExecutor, policy, decision)
				if err != nil {
					return "", err
				}
				if done {
					return answer, nil
				}
				continue restartBudget
			}
			answer, outcome, err := l.runIteration(ctx, i+1, callOptions, llmExecutor, parser, toolSpecs, toolExecutionHooks, policy, &contextOverflowRecoveryUsed)
			if err != nil {
				return "", err
			}
			if outcome == iterationDone {
				return answer, nil
			}
			if outcome == iterationRestartBudget {
				continue restartBudget
			}
		}

		answer, done, err := l.stopWithSteerCheck(ctx, llmExecutor, policy, policy.BudgetExhausted("max_iterations budget exhausted"))
		if err != nil {
			return "", err
		}
		if done {
			return answer, nil
		}
	}
}

func (l *AgentLoop) loopGuardPolicy() *TerminationPolicy {
	if l != nil && l.TerminationPolicy != nil {
		return l.TerminationPolicy
	}
	return NewTerminationPolicy(DefaultTerminationPolicyConfig())
}

// stopWithSteerCheck checks for pending steer before terminating, giving steer priority.
// The done result is false when a steer was consumed and the caller should restart
// the iteration budget so the new instruction can be processed.
func (l *AgentLoop) stopWithSteerCheck(ctx context.Context, executor *executor.LLMExecutor, policy *TerminationPolicy, decision TerminationDecision) (string, bool, error) {
	// Check for pending steer before terminating (except for external cancellation)
	if decision.Reason != StopReasonExternal {
		if _, hasPending, err := l.consumeAndPersistSteer(ctx, executor); err != nil {
			return "", false, err
		} else if hasPending {
			policy.ResetForSteer()
			return "", false, nil
		}
	}
	answer, err := l.stopWithDecision(ctx, policy, decision)
	return answer, true, err
}

func (l *AgentLoop) runIteration(ctx context.Context, iteration int, callOptions []llms.CallOption, llmExecutor *executor.LLMExecutor, parser *FunctionAgent, toolSpecs *ToolSpecs, toolExecutionHooks toolExecutionHookHandler, policy *TerminationPolicy, contextOverflowRecoveryUsed *bool) (string, iterationOutcome, error) {
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
		return "", iterationContinue, err
	} else if hasPending {
		policy.ResetForSteer()
		return "", iterationRestartBudget, nil
	}

	turnOptions := append([]llms.CallOption{}, callOptions...)
	turnOptions = append(turnOptions, llms.WithTools(parser.toolsAsLLM()))
	if l.ContextBudgetGuard != nil {
		var resolvedOptions llms.CallOptions
		for _, option := range turnOptions {
			if option != nil {
				option(&resolvedOptions)
			}
		}
		newManager, changed, err := l.ContextBudgetGuard(ctx, llmExecutor.ContextManager(), resolvedOptions)
		if changed {
			if newManager == nil {
				return "", iterationContinue, fmt.Errorf("guard context budget before model request: context manager is nil")
			}
			l.contextManager = newManager
			llmExecutor.ReplaceContextManager(newManager)
		}
		if err != nil {
			return "", iterationContinue, fmt.Errorf("guard context budget before model request: %w", err)
		}
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

	contentResp, err := llmExecutor.GenerateContent(contextWithRawHTTPLog(llmCtx), turnOptions...)
	if err != nil {
		l.abortStreamingResponse(ctx)
		if contextOverflowRecoveryUsed != nil && !*contextOverflowRecoveryUsed && isProviderContextExceededError(err) && l.ContextOverflowRecovery != nil {
			*contextOverflowRecoveryUsed = true
			newManager, compacted, recoveryErr := l.ContextOverflowRecovery(ctx, llmExecutor.ContextManager())
			if recoveryErr != nil {
				return "", iterationContinue, fmt.Errorf("compact context after provider context overflow: %w", recoveryErr)
			}
			if compacted {
				if newManager == nil {
					return "", iterationContinue, fmt.Errorf("compact context after provider context overflow: context manager is nil")
				}
				l.contextManager = newManager
				llmExecutor.ReplaceContextManager(newManager)
				log.Printf("[context] provider context limit exceeded; compacted context and retrying\n")
				return "", iterationRestartBudget, nil
			}
		}
		// If LLM was canceled due to interrupt, check for pending steer
		if errors.Is(err, context.Canceled) || errors.Is(err, errSteerInterruptToolCancel) {
			steerInterrupted := errors.Is(context.Cause(llmCtx), errSteerInterruptToolCancel)
			if _, hasPending, persistErr := l.consumeAndPersistSteer(ctx, llmExecutor); persistErr != nil {
				return "", iterationContinue, persistErr
			} else if hasPending {
				policy.ResetForSteer()
				return "", iterationRestartBudget, nil
			}
			// Only wait for out-of-band steer if cancellation was triggered by steer interrupt,
			// not by external cancellation (shutdown, timeout, etc.)
			if steerInterrupted && l.SteerWaiter != nil {
				// Use ctx (not Background) so outer cancellation can abort the wait
				waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				waitedSteer, hasText, waitErr := l.SteerWaiter(waitCtx)
				cancel()
				if waitErr == nil && hasText {
					if persistErr := l.persistSteer(ctx, llmExecutor, waitedSteer); persistErr != nil {
						return "", iterationContinue, persistErr
					}
					policy.ResetForSteer()
					return "", iterationRestartBudget, nil
				}
			}
			if steerInterrupted {
				// A queued steer may have been canceled after the interrupt fired.
				// Retry the original task with the request's rearmed signal channel.
				return "", iterationRestartBudget, nil
			}
		}
		return "", iterationContinue, err
	}
	l.finishStreamingResponse(ctx)
	l.emitRoleOutput(ctx, roleResponseDebugText(contentResp))
	if answer := l.touchPointerModeMismatchContentFinalAnswer(contentResp); answer != "" {
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, iterationDone, err
	}

	actions, finish, err := parser.ParseOutput(contentResp)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		choiceMessage := messages.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0])
		if choiceMessage.Role != messages.MessageRoleToolCall {
			if err := llmExecutor.AppendMessage(choiceMessage); err != nil {
				return "", iterationContinue, err
			}
		}
		decision := policy.RecordParseFailure()
		if decision.Stop {
			return l.finishStopDecision(ctx, policy, decision)
		}
		l.applyLoopGuardDecision(decision)

		// Problem 4: Check for pending steer after parse failure (continue iteration)
		if steer, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor); err != nil {
			return "", iterationContinue, err
		} else if hasPending {
			policy.ResetForSteer()
			log.Printf("[steer] LLM parse failed, steer injected (length=%d)\n", len(steer.Content))
			return "", iterationRestartBudget, nil
		}

		return "", iterationContinue, nil
	}
	if err != nil {
		return "", iterationContinue, err
	}
	if finish != nil {
		// Don't check steer after finish - let this turn complete normally.
		// Steer will be processed as a new turn.
		if err := llmExecutor.AppendMessage(messages.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0])); err != nil {
			return "", iterationContinue, err
		}
		answer, _ := finish.ReturnValues[agentLoopOutputKey].(string)
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return "", iterationContinue, agents.ErrAgentNoReturn
		}
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, iterationDone, err
	}
	if len(actions) == 0 {
		return "", iterationContinue, agents.ErrAgentNoReturn
	}

	// Problem 4: Check for pending steer after LLM returns actions (before tool execution)
	if steer, hasPending := l.checkPendingSteer(ctx); hasPending {
		policy.ResetForSteer()
		if err := l.persistSteer(ctx, llmExecutor, steer); err != nil {
			return "", iterationContinue, err
		}
		log.Printf("[steer] LLM action interrupted, steer injected (length=%d)\n", len(steer.Content))
		return "", iterationRestartBudget, nil
	}

	action := actions[0]
	toolCallMessage := messages.ConvertChoiceToContextManagerMessage(choiceWithOnlyToolCall(*contentResp.Choices[0], action.ToolID))
	var after AfterToolCallHook
	if toolExecutionHooks != nil {
		after = func(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
			result = DefaultAfterToolCall(ctx, call, result)
			return toolExecutionHooks.AfterToolCall(ctx, call, result)
		}
	}
	toolCtx := WithEpisodeRecorder(ctx, l.Recorder)
	toolExecution := l.executeToolCall(toolCtx, ToolCallExecution{
		Specs:  toolSpecs,
		Action: action,
		Before: func(ctx context.Context, call ToolCall) (ToolResult, bool) {
			result, allowed := policy.BeforeToolCall(call.Spec.Name, call.Input)
			if !allowed {
				return result, false
			}
			result, allowed = DefaultBeforeToolCall(ctx, call)
			if !allowed || toolExecutionHooks == nil {
				return result, allowed
			}
			return toolExecutionHooks.BeforeToolCall(ctx, call)
		},
		After:          after,
		Callback:       l.CallbacksHandler,
		ResultObserver: l.ToolResultObserver,
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
	prepared := PreparedToolResult{
		Content:  toolExecution.Step.Observation,
		Complete: true,
	}
	isVisualObservation := parser != nil && parser.isVisualObservationTool(toolExecution.Step.Action.Tool)
	if !isVisualObservation {
		resultPolicy := l.ToolResultPolicy
		if resultPolicy == nil {
			resultPolicy = NewToolResultPolicy()
		}
		preparedResult, prepareErr := resultPolicy.Prepare(ctx, ToolResultPrepareInput{
			Call:            toolExecution.Call,
			Result:          toolExecution.Result,
			ActionCompleted: toolExecution.ActionCompleted,
			ContextManager:  llmExecutor.ContextManager(),
			ModelSpec:       l.Model.Spec(),
			CallOptions:     turnOptions,
		})
		if prepareErr != nil {
			log.Printf("[tool_result] preparation failed for %s: %v\n", toolExecution.Call.Spec.Name, prepareErr)
			prepared = failedPreparedToolResult(toolExecution.Result, toolExecution.ActionCompleted)
		} else {
			prepared = preparedResult
		}
	}
	if l.Recorder != nil && !isVisualObservation {
		l.Recorder.RecordEvent(TaskEpisodeEvent{
			Type:     runEventToolResultContext,
			Role:     "agent",
			ToolName: toolExecution.Call.Spec.Name,
			Metadata: map[string]interface{}{
				"original_bytes":         prepared.OriginalBytes,
				"original_chars":         prepared.OriginalChars,
				"estimated_tokens":       prepared.EstimatedTokens,
				"context_bytes":          prepared.ContextBytes,
				"context_tokens":         prepared.ContextTokens,
				"processing_reason":      prepared.Reason,
				"artifactized":           prepared.ArtifactPath != "",
				"artifact_complete":      prepared.ArtifactComplete,
				"artifact_store_error":   prepared.ArtifactStoreError,
				"processing_duration_ms": prepared.ProcessingDurationMs,
			},
		})
	}
	appendErr := appendToolExecutionMessages(llmExecutor, parser, toolCallMessage, toolExecution.Step, prepared)
	if toolExecution.Error != nil {
		if appendErr != nil {
			return "", iterationContinue, errors.Join(toolExecution.Error, appendErr)
		}
		// If the tool was canceled by steering, consume an already queued steer or
		// briefly wait for the out-of-band capture to finish.
		if toolExecution.InterruptedBySteer && errors.Is(toolExecution.Error, context.Canceled) {
			steer, hasPending, err := l.consumeAndPersistSteer(ctx, llmExecutor)
			if err != nil {
				return "", iterationContinue, err
			}
			// Only wait for out-of-band steer if cancellation was triggered by steer interrupt.
			// Check if outer ctx is still valid - if it's canceled, this is external cancellation.
			if !hasPending && ctx.Err() == nil && l.SteerWaiter != nil {
				// Use ctx (not Background) so outer cancellation can abort the wait
				waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				waitedSteer, hasText, waitErr := l.SteerWaiter(waitCtx)
				cancel()
				if waitErr == nil && hasText {
					if err := l.persistSteer(ctx, llmExecutor, waitedSteer); err != nil {
						return "", iterationContinue, err
					}
					steer = waitedSteer
					hasPending = true
				}
			}
			if hasPending {
				policy.ResetForSteer()
				log.Printf("[steer] tool canceled but pending steer exists (length=%d), restarting iteration budget\n", len(steer.Content))
				return "", iterationRestartBudget, nil
			}
			if ctx.Err() == nil {
				// The steer signal interrupted the tool, but its queued message was
				// canceled before consumption. Retry the original task with the
				// request's rearmed signal channel and a fresh iteration budget.
				return "", iterationRestartBudget, nil
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
		l.applyLoopGuardDecision(decision)
		return "", iterationContinue, nil
	}
	if appendErr != nil {
		return "", iterationContinue, appendErr
	}
	if answer := l.touchPointerModeMismatchFinalAnswer(toolExecution.Step); answer != "" {
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, iterationDone, err
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
		return "", iterationContinue, err
	} else if hasPending {
		policy.ResetForSteer()
		// Restart the budget so the LLM can process the tool result and steer content.
		return "", iterationRestartBudget, nil
	}

	if isRunPausingTool(toolExecution.Call.Action.Tool) && !toolExecution.Result.IsError() {
		answer := runPausingToolFinalAnswer(&toolExecution.Step)
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, iterationDone, err
	}
	if decision.Stop {
		return l.finishStopDecision(ctx, policy, decision)
	}
	l.applyLoopGuardDecision(decision)

	return "", iterationContinue, nil
}

func (l *AgentLoop) finishStopDecision(ctx context.Context, policy *TerminationPolicy, decision TerminationDecision) (string, iterationOutcome, error) {
	// Problem 5: When terminating with pending steer, inject steer as new user message
	// instead of wrapping it as final answer. Apply to all termination reasons except
	// external cancellation (where the context is already done and steer is irrelevant).
	if decision.Reason != StopReasonExternal {
		// Note: executor is not available here, but consumeAndPersistSteer handles nil executor
		if _, hasPending, err := l.consumeAndPersistSteer(ctx, nil); err != nil {
			return "", iterationContinue, err
		} else if hasPending {
			policy.ResetForSteer()
			// Don't terminate; restart the iteration budget for the new instruction.
			return "", iterationRestartBudget, nil
		}
	}
	answer, err := l.stopWithDecision(ctx, policy, decision)
	return answer, iterationDone, err
}

func (l *AgentLoop) applyLoopGuardDecision(decision TerminationDecision) {
	if strings.TrimSpace(decision.Notice) == "" {
		return
	}
	if err := l.contextManager.AppendMessage(messages.Message{
		Role:    messages.MessageRoleNotice,
		Content: decision.Notice,
	}); err != nil {
		log.Printf("[loop guard] failed to append notice message: %v", err)
	}
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
	// Normalize once so the model context, the recorded steer, and the emitted
	// event all carry the same text. Whitespace-only input would otherwise reach
	// the model as an empty message while being recorded as the placeholder.
	steer.Content = steerHumanMessageContent(steer)

	// Step 1: Append to context manager
	if executor != nil {
		if err := executor.AppendMessage(messages.Message{
			Role:    messages.MessageRoleUser,
			Content: steer.Content,
		}); err != nil {
			return err
		}
	} else if l.contextManager != nil {
		if err := l.contextManager.AppendMessage(messages.Message{
			Role:    messages.MessageRoleUser,
			Content: steer.Content,
		}); err != nil {
			return err
		}
	}

	// Step 2: Track the steer for session event persistence.
	if l.SteerRecorder != nil {
		if err := l.SteerRecorder.RecordSteer(steer); err != nil {
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
	result.InterruptedBySteer = errors.Is(context.Cause(toolCtx), errSteerInterruptToolCancel)
	close(done)
	cancel(nil)

	// If tool was canceled due to interrupt, check for pending steer before returning error
	// This prevents losing user's new instruction when tool is cancelable
	if result.Error != nil && result.InterruptedBySteer {
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

type streamingResponseBoundaryHandler interface {
	FinishStreamingResponse(context.Context)
	AbortStreamingResponse(context.Context)
}

func (l *AgentLoop) finishStreamingResponse(ctx context.Context) {
	if l == nil || l.CallbacksHandler == nil {
		return
	}
	if handler, ok := l.CallbacksHandler.(streamingResponseBoundaryHandler); ok {
		handler.FinishStreamingResponse(ctx)
	}
}

func (l *AgentLoop) abortStreamingResponse(ctx context.Context) {
	if l == nil || l.CallbacksHandler == nil {
		return
	}
	if handler, ok := l.CallbacksHandler.(streamingResponseBoundaryHandler); ok {
		handler.AbortStreamingResponse(ctx)
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

func appendToolExecutionMessages(llmExecutor *executor.LLMExecutor, parser *FunctionAgent, toolCall messages.Message, step schema.AgentStep, prepared PreparedToolResult) error {
	if llmExecutor == nil {
		return fmt.Errorf("llm executor is nil")
	}

	toolContent := prepared.Content
	var followups []llms.MessageContent
	if parser != nil && parser.isVisualObservationTool(step.Action.Tool) {
		if content, visualFollowups := parser.observationMessagesForStep(step, true); len(visualFollowups) > 0 {
			toolContent = content
			followups = visualFollowups
			prepared = PreparedToolResult{Content: content, Complete: true}
		}
	}
	prepared.Content = toolContent

	contextMessages := []messages.Message{toolCall, toolResultMessage(
		step.Action.ToolID,
		step.Action.Tool,
		prepared,
	)}
	for _, followup := range followups {
		contextMessages = append(contextMessages, visualFollowupMessageFromLLMContent(llmExecutor.ContextManager(), followup))
	}
	if err := llmExecutor.AppendMessages(contextMessages); err != nil {
		return fmt.Errorf("failed to append tool call and result messages: %w", err)
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
	if !strings.Contains(lower, "hid.pointer_mode") &&
		!strings.Contains(lower, "device.device_type") &&
		!strings.Contains(lower, "[device].device_type") {
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
		return `touch_gesture produced no visible screen change, and the connected platform is Android while [device].device_type derives hid.pointer_mode="absolute". Stop operation here because the touch mode likely does not match the target. Please switch [device].device_type to "Android", restart the agent, and retry.`
	case (platform == "ios" || platform == "ipados") && pointerMode == "touchscreen":
		return `touch_gesture produced no visible screen change, and the connected platform is iOS while [device].device_type derives hid.pointer_mode="touchscreen". Stop operation here because the touch mode likely does not match the target. Please switch [device].device_type to "iOS", restart the agent, and retry.`
	default:
		return ""
	}
}

func toolNameEqual(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func isRunPausingTool(name string) bool {
	return toolNameEqual(name, toolWaitForWakeup) || toolNameEqual(name, toolUserActionStep)
}

func runPausingToolFinalAnswer(step *schema.AgentStep) string {
	if step != nil && toolNameEqual(step.Action.Tool, toolUserActionStep) {
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
