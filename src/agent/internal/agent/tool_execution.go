package agent

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type ToolCall struct {
	Spec      ToolSpec
	Action    schema.AgentAction
	Input     string
	Content   string
	StartedAt time.Time
}

type ToolResult struct {
	Output    string
	Summary   string
	Error     *ToolError
	Terminate bool
	Duration  time.Duration
}

func (r ToolResult) IsError() bool { return r.Error != nil }

type ToolCallExecution struct {
	Specs                  *ToolSpecs
	Action                 schema.AgentAction
	Before                 BeforeToolCallHook
	After                  AfterToolCallHook
	Callback               callbacks.Handler
	EnvironmentBridge      *EnvironmentBridgeClient
	EnvironmentBridgeTools []string // Tool name globs to forward; empty forwards nothing (see shouldForwardToEnvironmentBridge)
	VisualArtifacts        *visualArtifactStore
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

// shouldForwardToEnvironmentBridge determines whether a tool call should be forwarded to the
// environment bridge. A tool is forwarded when its name matches any pattern in
// environmentBridgeTools. Patterns use shell-style globbing (path.Match), so "*" matches
// every tool, "keyboard_*" matches all keyboard tools, and an exact name like
// "screenshot" matches only that tool. An empty environmentBridgeTools list forwards
// nothing, so the caller is responsible for supplying the device-tool patterns.
func shouldForwardToEnvironmentBridge(toolName string, environmentBridgeTools []string) bool {
	for _, pattern := range environmentBridgeTools {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		// path.Match only errors on malformed patterns; treat a malformed
		// pattern as a literal exact-match fallback so a stray character never
		// silently forwards or drops a tool.
		if matched, err := path.Match(pattern, toolName); err == nil {
			if matched {
				return true
			}
			continue
		}
		if pattern == toolName {
			return true
		}
	}
	return false
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
		Spec:      spec,
		Action:    action,
		Input:     input,
		Content:   toolContentFromAction(action),
		StartedAt: startedAt,
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
		msg := err.Error()
		result := ToolResult{
			Output:   msg,
			Error:    NewToolError(CodeInvalidArguments, msg),
			Duration: time.Since(call.StartedAt),
		}
		result = runAfterToolCallHook(ctx, execution, call, result)
		emitToolResult(ctx, execution.Callback, call, result)
		return resultForToolCall(call, result, nil)
	}

	// If environment bridge is enabled, forward the call to the bridge. When the
	// HTTP call succeeds the remote ToolResult is passed through verbatim because
	// the remote ran the same executeToolCall path and already produced the same
	// structured response the LLM would see locally. Only transport failures are
	// formatted here, mirroring how a local tool error is surfaced.
	//
	// Only forward tools whose name matches one of the configured EnvironmentBridgeTools
	// patterns (see shouldForwardToEnvironmentBridge). Everything else runs locally.
	if execution.EnvironmentBridge != nil && shouldForwardToEnvironmentBridge(spec.Name, execution.EnvironmentBridgeTools) {
		remote, err := execution.EnvironmentBridge.CallTool(ctx, spec.Name, input)
		if err != nil {
			code := CodeToolExecutionFailed
			if errors.Is(err, context.Canceled) {
				code = CodeCanceled
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = CodeDeadlineExceeded
			}
			toolErr := NewToolError(code, err.Error())
			result := ToolResult{
				Output:   toolErr.Message,
				Error:    toolErr,
				Duration: time.Since(call.StartedAt),
			}
			result = runAfterToolCallHook(ctx, execution, call, result)
			emitToolResult(ctx, execution.Callback, call, result)
			return ToolCallExecutionResult{Call: call, Result: result, Error: err}
		}
		result := *remote
		result.Duration = time.Since(call.StartedAt)
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.Canceled) {
				result.Error = NewToolError(CodeCanceled, ctxErr.Error())
			} else if errors.Is(ctxErr, context.DeadlineExceeded) {
				result.Error = NewToolError(CodeDeadlineExceeded, ctxErr.Error())
			} else {
				result.Error = NewToolError(CodeToolExecutionFailed, ctxErr.Error())
			}
			result.Output = result.Error.Message
			result = runAfterToolCallHook(ctx, execution, call, result)
			emitToolResult(ctx, execution.Callback, call, result)
			return ToolCallExecutionResult{Call: call, Result: result, Error: ctxErr}
		}
		result = runAfterToolCallHook(ctx, execution, call, result)
		emitToolResult(ctx, execution.Callback, call, result)
		return resultForToolCall(call, result, nil)
	}

	ctx2, _ := WithToolError(ctx)
	output, err := spec.Tool.Call(ctx2, input)
	var toolErr *ToolError
	hardErr := ctx.Err()
	if hardErr != nil {
		if errors.Is(hardErr, context.Canceled) {
			toolErr = NewToolError(CodeCanceled, hardErr.Error())
		} else if errors.Is(hardErr, context.DeadlineExceeded) {
			toolErr = NewToolError(CodeDeadlineExceeded, hardErr.Error())
		} else {
			toolErr = NewToolError(CodeToolExecutionFailed, hardErr.Error())
		}
	} else if err != nil {
		if errors.Is(err, context.Canceled) {
			toolErr = NewToolError(CodeCanceled, err.Error())
		} else if errors.Is(err, context.DeadlineExceeded) {
			toolErr = NewToolError(CodeDeadlineExceeded, err.Error())
		} else {
			toolErr = NewToolError(CodeToolExecutionFailed, err.Error())
		}
	} else if attached := ToolErrorFromContext(ctx2); attached != nil {
		// Tool surfaced a structured error via SetToolError. Adopt it directly
		// so the LLM-facing Output (already equal to attached.Message) and the
		// downstream Error stay aligned.
		toolErr = attached
	}
	result := ToolResult{
		Output:   output,
		Error:    toolErr,
		Duration: time.Since(call.StartedAt),
	}
	if err == nil && toolErr != nil {
		result.Output = toolErr.Message
	}
	if err != nil || hardErr != nil {
		result.Output = toolErr.Message
		if hardErr != nil {
			result = runAfterToolCallHook(ctx, execution, call, result)
			emitToolResult(ctx, execution.Callback, call, result)
			return ToolCallExecutionResult{Call: call, Result: result, Error: hardErr}
		}
	}

	result = runAfterToolCallHook(ctx, execution, call, result)
	emitToolResult(ctx, execution.Callback, call, result)
	return resultForToolCall(call, result, nil)
}

func invalidToolCall(action schema.AgentAction, startedAt time.Time) ToolCall {
	action.ToolInput = normalizeToolInput(action.ToolInput)
	toolName := strings.TrimSpace(action.Tool)
	return ToolCall{
		Spec:      ToolSpec{Name: toolName},
		Action:    action,
		Input:     action.ToolInput,
		Content:   toolContentFromAction(action),
		StartedAt: startedAt,
	}
}

func invalidToolResult(call ToolCall) ToolResult {
	toolName := strings.TrimSpace(call.Action.Tool)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Spec.Name)
	}
	var msg string
	if toolName == "" {
		msg = "requested tool is not a valid tool, try another one"
	} else {
		msg = fmt.Sprintf("%s is not a valid tool, try another one", toolName)
	}
	return ToolResult{
		Output:   msg,
		Error:    NewToolError(CodeToolNotFound, msg),
		Duration: time.Since(call.StartedAt),
	}
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
	if execution.VisualArtifacts != nil && call.Spec.Tool != nil {
		if visual, ok := call.Spec.Tool.(visualObservationTool); ok && visual.ReturnsVisualObservation() {
			output, externalized, err := execution.VisualArtifacts.ExternalizeObservation(result.Output)
			if err != nil {
				msg := "failed to store visual artifact: " + err.Error()
				result.Error = NewToolError(CodeToolExecutionFailed, msg)
				result.Output = msg
				result.Summary = result.Output
			} else if externalized {
				result.Output = output
				if summary, ok := compactScreenshotObservation(call.Spec.Name, output); ok {
					result.Summary = summary
				} else {
					result.Summary = compactToolObservation(output)
				}
			}
		}
	}
	if handler, ok := execution.Callback.(toolExecutionHookHandler); ok {
		result = normalizeToolResult(handler.AfterToolCall(ctx, call, result))
	}
	return result
}

func normalizeRejectedToolResult(toolName string, result ToolResult) ToolResult {
	result = normalizeToolResult(result)
	if strings.TrimSpace(result.Output) == "" {
		if result.Error != nil {
			result.Output = result.Error.Message
		} else {
			result.Output = fmt.Sprintf("%s call rejected", toolName)
		}
	}
	if result.Error == nil {
		result.Error = NewToolError(CodeToolExecutionFailed, result.Output)
	}
	// Ensure Output == Error.Message invariant.
	if result.Error != nil && result.Output != result.Error.Message {
		result.Error.Message = result.Output
	}
	return result
}

func normalizeToolResult(result ToolResult) ToolResult {
	if result.Error != nil {
		if result.Output == "" {
			result.Output = result.Error.Message
		} else if result.Output != result.Error.Message {
			result.Error.Message = result.Output
		}
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
		if result.Error != nil {
			named.HandleNamedToolError(ctx, call.Spec.Name, call.Input, result.Error)
			return
		}
		named.HandleNamedToolEnd(ctx, call.Spec.Name, call.Input, output)
		return
	}
	if result.Error != nil {
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
	if call.Spec.Name == "screenshot" || returnsVisualObservation(call.Spec.Tool) {
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
