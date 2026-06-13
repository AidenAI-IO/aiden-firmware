package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type ToolCall struct {
	Spec        ToolSpec
	Action      schema.AgentAction
	Input       string
	Description string
	StartedAt   time.Time
}

type ToolResult struct {
	Output    string
	Summary   string
	IsError   bool
	Error     error
	Terminate bool
	Duration  time.Duration
}

type ToolCallExecution struct {
	Specs    *ToolSpecs
	Action   schema.AgentAction
	Before   BeforeToolCallHook
	After    AfterToolCallHook
	Callback callbacks.Handler
}

type ToolCallExecutionResult struct {
	Call   ToolCall
	Result ToolResult
	Step   schema.AgentStep
	Error  error
}

type BeforeToolCallHook func(context.Context, ToolCall) (ToolResult, bool)
type AfterToolCallHook func(context.Context, ToolCall, ToolResult) ToolResult

type toolExecutionHookHandler interface {
	BeforeToolCall(ctx context.Context, call ToolCall) (ToolResult, bool)
	AfterToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult
}

type toolResultCallbackHandler interface {
	HandleToolCallResult(ctx context.Context, call ToolCall, result ToolResult)
}

type toolCallStartCallbackHandler interface {
	HandleToolCallStart(ctx context.Context, call ToolCall)
}

func executeToolCall(ctx context.Context, execution ToolCallExecution) ToolCallExecutionResult {
	action := execution.Action
	startedAt := time.Now()
	spec, ok := execution.Specs.Lookup(action.Tool)
	if !ok || spec.Tool == nil {
		call := invalidToolCall(action, startedAt)
		emitToolStart(ctx, execution.Callback, call)
		result := invalidToolResult(call)
		emitToolResult(ctx, execution.Callback, call, result)
		return resultForToolCall(call, result, nil)
	}

	input := spec.NormalizeInput(action.ToolInput)
	action.Tool = spec.Name
	action.ToolInput = input
	call := ToolCall{
		Spec:        spec,
		Action:      action,
		Input:       input,
		Description: toolDescriptionFromAction(action),
		StartedAt:   startedAt,
	}

	emitToolStart(ctx, execution.Callback, call)
	if allowed, result := runBeforeToolCallHook(ctx, execution, call); !allowed {
		result = normalizeRejectedToolResult(spec.Name, result)
		result.Duration = time.Since(call.StartedAt)
		result = runAfterToolCallHook(ctx, execution, call, result)
		emitToolResult(ctx, execution.Callback, call, result)
		return resultForToolCall(call, result, nil)
	}

	if err := spec.ValidateInput(input); err != nil {
		result := ToolResult{
			Output:  fmt.Sprintf("error: %s failed: %v", spec.Name, err),
			IsError: true,
			Error:   err,
		}
		result.Duration = time.Since(call.StartedAt)
		result = runAfterToolCallHook(ctx, execution, call, result)
		emitToolResult(ctx, execution.Callback, call, result)
		return resultForToolCall(call, result, nil)
	}

	output, err := spec.Tool.Call(ctx, input)
	result := ToolResult{
		Output:   output,
		IsError:  err != nil || toolOutputLooksLikeError(output),
		Error:    err,
		Duration: time.Since(call.StartedAt),
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result = runAfterToolCallHook(ctx, execution, call, result)
			emitToolResult(ctx, execution.Callback, call, result)
			return ToolCallExecutionResult{Call: call, Result: result, Error: err}
		}
		result.Output = fmt.Sprintf("error: %s failed: %v", spec.Name, err)
	}

	result = runAfterToolCallHook(ctx, execution, call, result)
	emitToolResult(ctx, execution.Callback, call, result)
	return resultForToolCall(call, result, nil)
}

func invalidToolCall(action schema.AgentAction, startedAt time.Time) ToolCall {
	action.ToolInput = normalizeToolInput(action.ToolInput)
	toolName := strings.TrimSpace(action.Tool)
	return ToolCall{
		Spec:        ToolSpec{Name: toolName},
		Action:      action,
		Input:       action.ToolInput,
		Description: toolDescriptionFromAction(action),
		StartedAt:   startedAt,
	}
}

func invalidToolResult(call ToolCall) ToolResult {
	toolName := strings.TrimSpace(call.Action.Tool)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Spec.Name)
	}
	output := fmt.Sprintf("%s is not a valid tool, try another one", toolName)
	if toolName == "" {
		output = "requested tool is not a valid tool, try another one"
	}
	return ToolResult{Output: output, IsError: true, Duration: time.Since(call.StartedAt)}
}

func runBeforeToolCallHook(ctx context.Context, execution ToolCallExecution, call ToolCall) (bool, ToolResult) {
	if execution.Before != nil {
		result, allowed := execution.Before(ctx, call)
		if !allowed {
			return false, result
		}
	}
	if handler, ok := execution.Callback.(toolExecutionHookHandler); ok {
		result, allowed := handler.BeforeToolCall(ctx, call)
		if !allowed {
			return false, result
		}
	}
	return true, ToolResult{}
}

func runAfterToolCallHook(ctx context.Context, execution ToolCallExecution, call ToolCall, result ToolResult) ToolResult {
	if execution.After != nil {
		result = normalizeToolResult(execution.After(ctx, call, result))
	} else {
		result = normalizeToolResult(DefaultAfterToolCall(ctx, call, result))
	}
	if handler, ok := execution.Callback.(toolExecutionHookHandler); ok {
		return normalizeToolResult(handler.AfterToolCall(ctx, call, result))
	}
	return result
}

func normalizeRejectedToolResult(toolName string, result ToolResult) ToolResult {
	result = normalizeToolResult(result)
	if strings.TrimSpace(result.Output) == "" {
		if result.Error != nil {
			result.Output = "error: " + result.Error.Error()
		} else {
			result.Output = fmt.Sprintf("error: %s call rejected", toolName)
		}
	}
	result.IsError = true
	return result
}

func normalizeToolResult(result ToolResult) ToolResult {
	if result.Output == "" && result.Error != nil {
		result.Output = "error: " + result.Error.Error()
	}
	if toolOutputLooksLikeError(result.Output) {
		result.IsError = true
	}
	return result
}

func resultForToolCall(call ToolCall, result ToolResult, err error) ToolCallExecutionResult {
	stepOutput := result.Output
	return ToolCallExecutionResult{
		Call:   call,
		Result: result,
		Step: schema.AgentStep{
			Action:      call.Action,
			Observation: stepOutput,
		},
		Error: err,
	}
}

func emitToolStart(ctx context.Context, handler callbacks.Handler, call ToolCall) {
	if handler == nil {
		return
	}
	if rich, ok := handler.(toolCallStartCallbackHandler); ok {
		rich.HandleToolCallStart(ctx, call)
		return
	}
	if named, ok := handler.(namedToolCallbackHandler); ok {
		named.HandleNamedToolStart(ctx, call.Spec.Name, call.Input)
		return
	}
	handler.HandleToolStart(ctx, call.Input)
}

func emitToolResult(ctx context.Context, handler callbacks.Handler, call ToolCall, result ToolResult) {
	if handler == nil {
		return
	}
	if rich, ok := handler.(toolResultCallbackHandler); ok {
		rich.HandleToolCallResult(ctx, call, result)
		return
	}
	output := result.EventOutput()
	if named, ok := handler.(namedToolCallbackHandler); ok {
		if result.IsError && result.Error != nil {
			named.HandleNamedToolError(ctx, call.Spec.Name, call.Input, result.Error)
			return
		}
		named.HandleNamedToolEnd(ctx, call.Spec.Name, call.Input, output)
		return
	}
	if result.IsError && result.Error != nil {
		handler.HandleToolError(ctx, result.Error)
		return
	}
	handler.HandleToolEnd(ctx, output)
}

func (r ToolResult) EventOutput() string {
	if strings.TrimSpace(r.Summary) != "" {
		return r.Summary
	}
	return r.Output
}

func DefaultBeforeToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	return ToolResult{}, true
}

func DefaultAfterToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
	result = normalizeToolResult(result)
	if result.Summary == "" {
		if summary, ok := compactScreenshotObservation(call.Spec.Name, result.Output); ok {
			result.Summary = summary
		} else {
			result.Summary = compactToolObservation(result.Output)
		}
	}
	if call.Spec.Name == "enter_sleep" || call.Spec.Name == "screenshot" || returnsVisualObservation(call.Spec.Tool) {
		result.Terminate = true
	}
	return result
}

func returnsVisualObservation(tool langtools.Tool) bool {
	if visual, ok := tool.(visualObservationTool); ok {
		return visual.ReturnsVisualObservation()
	}
	return false
}
