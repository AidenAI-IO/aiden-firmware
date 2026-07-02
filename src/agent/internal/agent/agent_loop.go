package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
	VisualArtifacts        *visualArtifactStore
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
	if l.VisualArtifacts != nil {
		defer l.VisualArtifacts.Close()
	}
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
		VisualArtifacts:   l.VisualArtifacts,
	}
	callOptions := chains.GetLLMCallOptions(options...)

	for i := 0; i < l.MaxIterations; i++ {
		turnOptions := append([]llms.CallOption{}, callOptions...)
		turnOptions = append(turnOptions, llms.WithTools(parser.toolsAsLLM()))
		_, contentResp, err := llmExecutor.Generate(ctx, turnOptions...)
		if err != nil {
			return "", err
		}
		l.emitRoleOutput(ctx, roleResponseDebugText(contentResp))

		actions, finish, err := parser.ParseOutput(contentResp)
		if errors.Is(err, agents.ErrUnableToParseOutput) {
			llmExecutor.AppendMessage(toolResultMessage("", "", err.Error()))
			continue
		}
		if err != nil {
			return "", err
		}
		if finish != nil {
			answer, _ := finish.ReturnValues[agentLoopOutputKey].(string)
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return "", agents.ErrAgentNoReturn
			}
			if l.Recorder != nil {
				l.Recorder.RecordDefaultFinish(answer)
			}
			return l.finishRun(ctx, answer)
		}
		if len(actions) == 0 {
			return "", agents.ErrAgentNoReturn
		}

		action := actions[0]
		toolExecution := l.executeToolCall(ctx, ToolCallExecution{
			Specs:                  toolSpecs,
			Action:                 action,
			Callback:               l.CallbacksHandler,
			EnvironmentBridge:      l.EnvironmentBridge,
			EnvironmentBridgeTools: l.EnvironmentBridgeTools,
			VisualArtifacts:        l.VisualArtifacts,
		})
		if toolExecution.Error != nil {
			return "", toolExecution.Error
		}
		if l.Recorder != nil {
			l.Recorder.RecordPlannerExecution(roleExecutionResult{
				Action:    &toolExecution.Step.Action,
				Step:      &toolExecution.Step,
				ToolError: toolExecution.Result.Error,
			})
		}
		if isRunPausingTool(toolExecution.Step.Action.Tool) && !toolExecution.Result.IsError() {
			answer := runPausingToolFinalAnswer(&toolExecution.Step)
			if l.Recorder != nil {
				l.Recorder.RecordDefaultFinish(answer)
			}
			return l.finishRun(ctx, answer)
		}

		appendToolExecutionMessages(llmExecutor, parser, toolExecution.Step)
	}

	return "", agents.ErrNotFinished
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
		handler.HandleRoleOutput(ctx, string(RoleAgent), content)
	}
}

type roleOutputHandler interface {
	HandleRoleOutput(ctx context.Context, role, content string)
}

type roleExecutionResult struct {
	Action          *schema.AgentAction
	Step            *schema.AgentStep
	ToolError       *ToolError
	CandidateAnswer string
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
		llmExecutor.AppendMessage(visualFollowupMessageFromLLMContent(followup))
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
