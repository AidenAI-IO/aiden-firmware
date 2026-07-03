package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aiden-agent/internal/agent/context_manager"
	"aiden-agent/internal/agent/executor"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
)

const agentLoopOutputKey = "output"

type AgentLoop struct {
	Model                  llms.Model
	Profile                RoleProfile
	Memory                 schema.Memory
	CallbacksHandler       callbacks.Handler
	MaxIterations          int
	ConversationHistory    []llms.MessageContent
	InputAttachments       []InputAttachment
	Recorder               *EpisodeRecorder
	ScreenshotPruning      ScreenshotPruningConfig
	EnvironmentBridge      *EnvironmentBridgeClient
	EnvironmentBridgeTools []string
	SteerInterrupt         func() <-chan struct{}
	ContextManager         *context_manager.ContextManager
}

func NewAgentLoop(
	model llms.Model,
	profile RoleProfile,
	memory schema.Memory,
	maxIterations int,
	attachments []InputAttachment,
	callbacksHandler callbacks.Handler,
	recorder *EpisodeRecorder,
	screenshotPruning ScreenshotPruningConfig,
) *AgentLoop {
	return &AgentLoop{
		Model:               model,
		Profile:             profile,
		Memory:              memory,
		CallbacksHandler:    callbacksHandler,
		MaxIterations:       maxIterations,
		ConversationHistory: nil,
		InputAttachments:    attachments,
		Recorder:            recorder,
		ScreenshotPruning:   screenshotPruning,
	}
}

func (l *AgentLoop) Run(ctx context.Context, input string, options ...chains.ChainCallOption) (string, error) {
	inputs, err := loadAgentLoopInputs(ctx, l.Memory, input)
	if err != nil {
		return "", err
	}

	contextManager := l.ContextManager
	if contextManager == nil {
		contextManager = context_manager.NewContextManager()
		l.ContextManager = contextManager
	}
	preparePlannerContextManager(
		contextManager,
		l.Profile.SystemPrompt,
		l.ConversationHistory,
		inputs["input"],
		l.InputAttachments,
	)

	llmExecutor := executor.NewLLMExecutor(l.Model, contextManager)

	agentTools := l.Profile.Tools
	toolSpecs := NewToolSpecs(agentTools)
	parser := &FunctionAgent{
		Tools:             agentTools,
		OutputKey:         agentLoopOutputKey,
		ScreenshotPruning: l.ScreenshotPruning,
	}
	callOptions := chains.GetLLMCallOptions(options...)

	for i := 0; i < l.MaxIterations; i++ {
		answer, done, err := l.runIteration(ctx, i+1, callOptions, llmExecutor, parser, toolSpecs)
		if err != nil {
			return "", err
		}
		if done {
			return answer, nil
		}
	}

	return "", agents.ErrNotFinished
}

func (l *AgentLoop) runIteration(ctx context.Context, iteration int, callOptions []llms.CallOption, llmExecutor *executor.LLMExecutor, parser *FunctionAgent, toolSpecs *ToolSpecs) (string, bool, error) {
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

	turnOptions := append([]llms.CallOption{}, callOptions...)
	turnOptions = append(turnOptions, llms.WithTools(parser.toolsAsLLM()))
	contentResp, err := llmExecutor.GenerateContent(ctx, turnOptions...)
	if err != nil {
		return "", false, err
	}
	l.emitRoleOutput(ctx, roleResponseDebugText(contentResp))

	actions, finish, err := parser.ParseOutput(contentResp)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		llmExecutor.AppendMessage(context_manager.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0]))
		llmExecutor.AppendMessage(toolResultMessage("", "", err.Error()))
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if finish != nil {
		llmExecutor.AppendMessage(context_manager.ConvertChoiceToContextManagerMessage(*contentResp.Choices[0]))
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

	action := actions[0]
	llmExecutor.AppendMessage(context_manager.ConvertChoiceToContextManagerMessage(choiceWithOnlyToolCall(*contentResp.Choices[0], action.ToolID)))
	toolExecution := l.executeToolCall(ctx, ToolCallExecution{
		Specs:                  toolSpecs,
		Action:                 action,
		Callback:               l.CallbacksHandler,
		EnvironmentBridge:      l.EnvironmentBridge,
		EnvironmentBridgeTools: l.EnvironmentBridgeTools,
	})
	if toolExecution.Error != nil {
		return "", false, toolExecution.Error
	}
	if l.Recorder != nil {
		l.Recorder.RecordExecution(ToolCallExecutionResult{
			Call:   toolExecution.Call,
			Step:   toolExecution.Step,
			Result: toolExecution.Result,
			Error:  toolExecution.Error,
		})
	}
	toolCallsInIteration++
	if isRunPausingTool(toolExecution.Call.Action.Tool) && !toolExecution.Result.IsError() {
		answer := runPausingToolFinalAnswer(&toolExecution.Step)
		if l.Recorder != nil {
			l.Recorder.RecordDefaultFinish(answer)
		}
		answer, err = l.finishRun(ctx, answer)
		return answer, true, err
	}

	appendToolExecutionMessages(llmExecutor, parser, toolExecution.Step)
	return "", false, nil
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

func (l *AgentLoop) executeToolCall(ctx context.Context, execution ToolCallExecution) ToolCallExecutionResult {
	interruptCh := (<-chan struct{})(nil)
	if l != nil && l.SteerInterrupt != nil {
		interruptCh = l.SteerInterrupt()
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

func appendToolExecutionMessages(llmExecutor *executor.LLMExecutor, parser *FunctionAgent, step schema.AgentStep) {
	if llmExecutor == nil {
		return
	}

	toolContent := step.Observation
	var followups []llms.MessageContent
	if parser != nil && parser.isVisualObservationTool(step.Action.Tool) {
		if content, visualFollowups := parser.observationMessagesForStep(step, true); len(visualFollowups) > 0 {
			toolContent = content
			followups = visualFollowups
		}
	}

	llmExecutor.AppendMessage(toolResultMessage(
		step.Action.ToolID,
		step.Action.Tool,
		toolContent,
	))
	for _, followup := range followups {
		llmExecutor.AppendMessage(visualFollowupMessageFromLLMContent(llmExecutor.ContextManager(), followup))
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
