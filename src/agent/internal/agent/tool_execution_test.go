package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type toolExecutionCallbackRecorder struct {
	calls   []ToolCall
	results []ToolResult
	events  []string
}

func (r *toolExecutionCallbackRecorder) HandleText(ctx context.Context, text string) {}

func (r *toolExecutionCallbackRecorder) HandleLLMStart(ctx context.Context, prompts []string) {}

func (r *toolExecutionCallbackRecorder) HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent) {
}

func (r *toolExecutionCallbackRecorder) HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse) {
}

func (r *toolExecutionCallbackRecorder) HandleLLMError(ctx context.Context, err error) {}

func (r *toolExecutionCallbackRecorder) HandleChainStart(ctx context.Context, inputs map[string]any) {
}

func (r *toolExecutionCallbackRecorder) HandleChainEnd(ctx context.Context, outputs map[string]any) {}

func (r *toolExecutionCallbackRecorder) HandleChainError(ctx context.Context, err error) {}

func (r *toolExecutionCallbackRecorder) HandleToolStart(ctx context.Context, input string) {
	r.events = append(r.events, "start")
}

func (r *toolExecutionCallbackRecorder) HandleToolEnd(ctx context.Context, output string) {}

func (r *toolExecutionCallbackRecorder) HandleToolError(ctx context.Context, err error) {}

func (r *toolExecutionCallbackRecorder) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	r.calls = append(r.calls, ToolCall{Action: action})
}

func (r *toolExecutionCallbackRecorder) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {
}

func (r *toolExecutionCallbackRecorder) HandleRetrieverStart(ctx context.Context, query string) {}

func (r *toolExecutionCallbackRecorder) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
}

func (r *toolExecutionCallbackRecorder) HandleStreamingFunc(ctx context.Context, chunk []byte) {}

func (r *toolExecutionCallbackRecorder) BeforeToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	r.calls = append(r.calls, call)
	return ToolResult{}, true
}

func (r *toolExecutionCallbackRecorder) AfterToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
	return DefaultAfterToolCall(ctx, call, result)
}

func (r *toolExecutionCallbackRecorder) HandleToolCallResult(ctx context.Context, call ToolCall, result ToolResult) {
	r.events = append(r.events, "result")
	r.results = append(r.results, result)
}

func TestExecuteToolCallNormalizesBeforeValidationAndHooks(t *testing.T) {
	tool := &stubTool{name: "echo", description: "Echo text.", output: "ok"}
	specs := NewToolSpecs([]langtools.Tool{tool})

	var beforeInput string
	var afterInput string
	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello\nObservation:",
		},
		Before: func(ctx context.Context, call ToolCall) (ToolResult, bool) {
			beforeInput = call.Input
			return ToolResult{}, true
		},
		After: func(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
			afterInput = call.Input
			result.Output += "-after"
			return result
		},
	})

	if result.Error != nil {
		t.Fatalf("unexpected execution error: %v", result.Error)
	}
	if beforeInput != "hello" || afterInput != "hello" {
		t.Fatalf("hooks saw unnormalized input: before=%q after=%q", beforeInput, afterInput)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "hello" {
		t.Fatalf("tool inputs = %#v, want normalized input", tool.inputs)
	}
	if result.Step.Action.ToolInput != "hello" {
		t.Fatalf("step input = %q, want normalized input", result.Step.Action.ToolInput)
	}
	if result.Step.Observation != "ok-after" {
		t.Fatalf("observation = %q, want after hook output", result.Step.Observation)
	}
}

func TestExecuteToolCallUnwrapsCompatibleInputBeforeValidation(t *testing.T) {
	tool := &stubTool{name: "shell", description: "Run shell commands.", output: "ok"}
	specs := NewToolSpecs([]langtools.Tool{tool})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "shell",
			ToolInput: `{"__arg1":"{\"command\":\"pwd\"}","description":"run pwd"}`,
		},
	})

	if result.Error != nil {
		t.Fatalf("unexpected execution error: %v", result.Error)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != `{"command":"pwd"}` {
		t.Fatalf("tool inputs = %#v, want unwrapped JSON before validation", tool.inputs)
	}
}

func TestExecuteToolCallInvalidToolEmitsToolCallAndResult(t *testing.T) {
	recorder := &toolExecutionCallbackRecorder{}
	specs := NewToolSpecs([]langtools.Tool{&stubTool{name: "echo", description: "Echo.", output: "ok"}})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "missing",
			ToolInput: "hello",
		},
		Callback: recorder,
	})

	if !result.Result.IsError() {
		t.Fatalf("invalid tool should be an error result: %#v", result.Result)
	}
	if result.Call.Spec.Name != "missing" || result.Call.Input != "hello" {
		t.Fatalf("invalid tool call = %#v", result.Call)
	}
	if len(recorder.events) != 2 || recorder.events[0] != "start" || recorder.events[1] != "result" {
		t.Fatalf("callback lifecycle events = %#v, want start then result", recorder.events)
	}
	if len(recorder.results) != 1 || !recorder.results[0].IsError() {
		t.Fatalf("expected one error result callback, got %#v", recorder.results)
	}
}

func TestExecuteToolCallValidationFailureEmitsToolCallAndResult(t *testing.T) {
	recorder := &toolExecutionCallbackRecorder{}
	specs := NewToolSpecs([]langtools.Tool{&stubTool{name: "shell", description: "Run shell commands.", output: "ok"}})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "shell",
			ToolInput: `not-json`,
		},
		Callback: recorder,
	})

	if !result.Result.IsError() {
		t.Fatalf("invalid JSON should be an error result: %#v", result.Result)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].Input != "not-json" {
		t.Fatalf("expected one tool_call before validation failure, got %#v", recorder.calls)
	}
	if len(recorder.results) != 1 || !recorder.results[0].IsError() {
		t.Fatalf("expected one error result callback, got %#v", recorder.results)
	}
	if len(recorder.events) != 2 || recorder.events[0] != "start" || recorder.events[1] != "result" {
		t.Fatalf("callback lifecycle events = %#v, want start then result", recorder.events)
	}
	if recorder.results[0].Duration <= 0 {
		t.Fatalf("expected validation failure duration to be recorded, got %s", recorder.results[0].Duration)
	}
}

func TestExecuteToolCallBeforeMayRejectWithToolResult(t *testing.T) {
	recorder := &toolExecutionCallbackRecorder{}
	tool := &stubTool{name: "echo", description: "Echo text.", output: "should-not-run"}
	specs := NewToolSpecs([]langtools.Tool{tool})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
		},
		Before: func(ctx context.Context, call ToolCall) (ToolResult, bool) {
			return ToolResult{
				Output: "blocked by policy",
				Error:  NewToolError(CodeToolExecutionFailed, "blocked by policy"),
			}, false
		},
		Callback: recorder,
	})

	if len(tool.inputs) != 0 {
		t.Fatalf("tool should not have been called, inputs=%#v", tool.inputs)
	}
	if !result.Result.IsError() || result.Result.Output != "blocked by policy" {
		t.Fatalf("unexpected rejection result: %#v", result.Result)
	}
	if result.Step.Observation != "blocked by policy" {
		t.Fatalf("step observation = %q", result.Step.Observation)
	}
	if len(recorder.events) != 2 || recorder.events[0] != "start" || recorder.events[1] != "result" {
		t.Fatalf("callback lifecycle events = %#v, want start then result", recorder.events)
	}
}

func TestExecuteToolCallWrapsToolErrorsAndContinues(t *testing.T) {
	tool := &stubTool{name: "echo", description: "Echo text.", err: errors.New("boom")}
	specs := NewToolSpecs([]langtools.Tool{tool})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
		},
	})

	if result.Error != nil {
		t.Fatalf("tool failure should be returned as tool result, got execution error: %v", result.Error)
	}
	if !result.Result.IsError() {
		t.Fatalf("result should be marked as error: %#v", result.Result)
	}
	if result.Result.Error == nil || result.Result.Error.Code != CodeToolExecutionFailed || result.Result.Error.Message != "boom" {
		t.Fatalf("ToolError = %+v, want tool_execution_failed boom", result.Result.Error)
	}
	if result.Step.Observation != "boom" {
		t.Fatalf("observation = %q", result.Step.Observation)
	}
}

func TestExecuteToolCallBuiltInStringErrorToolsReturnStructuredErrors(t *testing.T) {
	tests := []struct {
		name     string
		tool     langtools.Tool
		input    string
		wantCode string
	}{
		{
			name:     "shell missing command",
			tool:     &ShellTool{},
			input:    `{}`,
			wantCode: CodeInvalidArguments,
		},
		{
			name:     "image diff missing after",
			tool:     &ImageDiffTool{},
			input:    `{"before":"x"}`,
			wantCode: CodeInvalidArguments,
		},
		{
			name:     "web search unconfigured",
			tool:     &WebSearchTool{provider: "brave"},
			input:    `{"query":"aiden"}`,
			wantCode: CodeModuleUnavailable,
		},
		{
			name:     "current time invalid timezone",
			tool:     &CurrentTimeTool{},
			input:    `{"timezone":"Not/AZone"}`,
			wantCode: CodeInvalidArguments,
		},
		{
			name:     "keyboard tap empty keys",
			tool:     &KeyboardTapTool{},
			input:    `{"keys":[]}`,
			wantCode: CodeInvalidArguments,
		},
		{
			name:     "touch gesture missing type",
			tool:     &TouchGestureTool{},
			input:    `{}`,
			wantCode: CodeInvalidArguments,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := executeToolCall(context.Background(), ToolCallExecution{
				Specs:  NewToolSpecs([]langtools.Tool{tc.tool}),
				Action: schema.AgentAction{Tool: tc.tool.Name(), ToolInput: tc.input},
			})
			if result.Result.Error == nil {
				t.Fatalf("expected structured ToolError, got result %#v", result.Result)
			}
			if result.Result.Error.Code != tc.wantCode {
				t.Fatalf("Error.Code = %q, want %q (error=%+v)", result.Result.Error.Code, tc.wantCode, result.Result.Error)
			}
			if result.Result.Output != result.Result.Error.Message {
				t.Fatalf("Output must equal Error.Message, got %q vs %q", result.Result.Output, result.Result.Error.Message)
			}
		})
	}
}

func TestExecuteToolCallPropagatesContextErrors(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(err.Error(), func(t *testing.T) {
			recorder := &toolExecutionCallbackRecorder{}
			tool := &stubTool{name: "echo", description: "Echo text.", err: err}
			specs := NewToolSpecs([]langtools.Tool{tool})

			result := executeToolCall(context.Background(), ToolCallExecution{
				Specs: specs,
				Action: schema.AgentAction{
					Tool:      "echo",
					ToolInput: "hello",
				},
				Callback: recorder,
			})

			if !errors.Is(result.Error, err) {
				t.Fatalf("execution error = %v, want %v", result.Error, err)
			}
			if result.Result.Error == nil {
				t.Fatalf("result.Error is nil, want structured error for %v", err)
			}
			wantCode := CodeCanceled
			if errors.Is(err, context.DeadlineExceeded) {
				wantCode = CodeDeadlineExceeded
			}
			if result.Result.Error.Code != wantCode {
				t.Fatalf("result error code = %q, want %q", result.Result.Error.Code, wantCode)
			}
			if result.Result.Output != result.Result.Error.Message {
				t.Fatalf("Output must equal Error.Message, got %q vs %q", result.Result.Output, result.Result.Error.Message)
			}
			if len(recorder.events) != 2 || recorder.events[0] != "start" || recorder.events[1] != "result" {
				t.Fatalf("callback lifecycle events = %#v, want start then result", recorder.events)
			}
			if len(recorder.results) != 1 || recorder.results[0].Error == nil || recorder.results[0].Error.Code != wantCode {
				t.Fatalf("terminal result callback = %#v, want error code %v", recorder.results, wantCode)
			}
			if recorder.results[0].Output != recorder.results[0].Error.Message {
				t.Fatalf("callback Output must equal Error.Message, got %q vs %q", recorder.results[0].Output, recorder.results[0].Error.Message)
			}
		})
	}
}

func TestDefaultAfterToolCallSummarizesScreenshotAndMarksTerminate(t *testing.T) {
	image := []byte("jpeg")
	output := `{"action_output":"ok","width":320,"height":240,"format":"jpeg","size":4,"data":"` +
		base64.StdEncoding.EncodeToString(image) + `"}`
	tool := &stubTool{name: "screenshot", description: "Take screenshot.", output: output, visual: true}
	specs := NewToolSpecs([]langtools.Tool{tool})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "screenshot",
			ToolInput: `{}`,
		},
		After: DefaultAfterToolCall,
	})

	if result.Error != nil {
		t.Fatalf("unexpected execution error: %v", result.Error)
	}
	if !result.Result.Terminate {
		t.Fatalf("screenshot result should be marked terminate: %#v", result.Result)
	}
	if result.Result.Output != output {
		t.Fatalf("raw screenshot output should remain available to the agent")
	}
	if strings.Contains(result.Result.Summary, base64.StdEncoding.EncodeToString(image)) {
		t.Fatalf("after hook should summarize screenshot data, got raw output")
	}
	if !strings.Contains(result.Result.Summary, "screenshot observation") {
		t.Fatalf("unexpected screenshot summary: %q", result.Result.Summary)
	}
	if result.Step.Observation != result.Result.Output {
		t.Fatalf("step observation should preserve raw output")
	}
}

func TestExecuteToolCallBeforeHookRejectWithEmptyOutputSatisfiesInvariant(t *testing.T) {
	// Regression: normalizeRejectedToolResult called normalizeToolResult which back-filled
	// Output with "error: " + Error.Message, breaking Output == Error.Message invariant.
	tool := &stubTool{name: "echo", description: "Echo text.", output: "should-not-run"}
	specs := NewToolSpecs([]langtools.Tool{tool})

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs: specs,
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
		},
		Before: func(ctx context.Context, call ToolCall) (ToolResult, bool) {
			// Hook returns Error but leaves Output empty — the bug case.
			return ToolResult{
				Error: NewToolError(CodeToolExecutionFailed, "denied"),
			}, false
		},
	})

	if len(tool.inputs) != 0 {
		t.Fatalf("tool should not have been called, inputs=%#v", tool.inputs)
	}
	if strings.TrimSpace(result.Result.Output) == "" {
		t.Fatalf("Output must be non-empty so LLM sees a message; got empty string")
	}
	if result.Result.Error == nil {
		t.Fatalf("Error must be set on a rejected result")
	}
	if result.Result.Output != result.Result.Error.Message {
		t.Fatalf("Output == Error.Message invariant violated: Output=%q Error.Message=%q",
			result.Result.Output, result.Result.Error.Message)
	}
}

func TestNormalizeToolResultAlignsNonEmptyErrorMessageToOutput(t *testing.T) {
	result := normalizeToolResult(ToolResult{
		Output: "policy override message",
		Error:  NewToolError(CodeToolExecutionFailed, "original message"),
	})
	if result.Error == nil {
		t.Fatal("expected structured error")
	}
	if result.Output != result.Error.Message {
		t.Fatalf("Output == Error.Message invariant violated: Output=%q Error.Message=%q", result.Output, result.Error.Message)
	}
}

func TestToolResultErrorIsStructured(t *testing.T) {
	ok := ToolResult{Output: "done", Summary: "ok"}
	if ok.IsError() {
		t.Errorf("success result reported IsError")
	}
	if ok.Error != nil {
		t.Errorf("success result has non-nil Error: %+v", ok.Error)
	}

	err := ToolResult{
		Output: "WeChat is not installed",
		Error:  NewToolError(CodeAppNotInstalled, "WeChat is not installed"),
	}
	if !err.IsError() {
		t.Errorf("failure result did not report IsError")
	}
	if err.Error.Code != CodeAppNotInstalled {
		t.Errorf("Error.Code = %q", err.Error.Code)
	}
	if err.Output != err.Error.Message {
		t.Errorf("Output must equal Error.Message on failure; got %q vs %q", err.Output, err.Error.Message)
	}
}
