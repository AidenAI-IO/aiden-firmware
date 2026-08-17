package agent

import (
	"aiden-agent/internal/agent/executor"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestFunctionAgentParseOutputSkipsNilToolCalls(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{
				{ID: "ignored", Type: "function"},
				{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "echo",
						Arguments: `{"__arg1":"hello"}`,
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].Tool != "echo" || actions[0].ToolInput != "hello" || actions[0].ToolID != "call_1" {
		t.Fatalf("unexpected action: %#v", actions[0])
	}
}

func TestChoiceWithOnlyToolCallAssignsIDToLegacyFunctionCall(t *testing.T) {
	functionCall := &llms.FunctionCall{Name: "echo", Arguments: `{}`}
	choice := choiceWithOnlyToolCall(llms.ContentChoice{FuncCall: functionCall}, "call_legacy")

	if len(choice.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one synthesized tool call", choice.ToolCalls)
	}
	if choice.ToolCalls[0].ID != "call_legacy" || choice.ToolCalls[0].FunctionCall != functionCall {
		t.Fatalf("tool call = %#v, want ID call_legacy and original function call", choice.ToolCalls[0])
	}
}

func TestFunctionAgentParseOutputUsesChoiceContentAsToolContent(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "发送测试文本。",
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "echo",
					Arguments: `{"input":"hello"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].ToolInput != "hello" {
		t.Fatalf("ToolInput = %q, want stripped tool input", actions[0].ToolInput)
	}
	if got := toolContentFromAction(actions[0]); got != "发送测试文本。" {
		t.Fatalf("tool content = %q", got)
	}

	var log toolActionLog
	if err := json.Unmarshal([]byte(actions[0].Log), &log); err != nil {
		t.Fatalf("action log should be structured JSON: %v", err)
	}
	if log.Version != toolActionLogVersion || log.ToolContent != "发送测试文本。" {
		t.Fatalf("unexpected action log metadata: %#v", log)
	}
}

func TestFunctionAgentParseOutputBindsChoiceContentToFirstValidToolCall(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "先检查。",
			ToolCalls: []llms.ToolCall{
				{ID: "ignored", Type: "function"},
				{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "echo",
						Arguments: `{"input":"first"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "echo",
						Arguments: `{"input":"second"}`,
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].ToolInput != "first" || actions[0].ToolID != "call_1" {
		t.Fatalf("unexpected first action: %#v", actions[0])
	}
	if got := toolContentFromAction(actions[0]); got != "先检查。" {
		t.Fatalf("first tool content = %q", got)
	}
}

func TestFunctionAgentParseOutputKeepsTaggedFinalText(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}
	content := "我已经完成了音量检查。\n<tts>我已经完成了任务，请查收。</tts>"

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: content,
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("expected no actions, got %#v", actions)
	}
	if finish == nil {
		t.Fatal("finish = nil")
	}
	if got := finish.ReturnValues["output"]; got != content {
		t.Fatalf("finish output = %#v, want raw tagged content", got)
	}
}

func TestFunctionAgentParseOutputExtractsGenericInputWrapper(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "Echoing text.",
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "echo",
					Arguments: `{"input":"hello"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].ToolInput != "hello" {
		t.Fatalf("ToolInput = %q, want generic input wrapper", actions[0].ToolInput)
	}
	if got := toolContentFromAction(actions[0]); got != "Echoing text." {
		t.Fatalf("tool content = %q", got)
	}
}

func TestFunctionAgentParseOutputPassesStructuredToolArguments(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "按回车。",
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "keyboard_tap",
					Arguments: `{"keys":["enter"]}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].ToolInput != `{"keys":["enter"]}` {
		t.Fatalf("ToolInput = %q, want structured args without description", actions[0].ToolInput)
	}
	if got := toolContentFromAction(actions[0]); got != "按回车。" {
		t.Fatalf("tool content = %q", got)
	}
}

func TestFunctionAgentParseOutputKeepsLegacyArg1CompatibilityWithoutContentFallback(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "keyboard_tap",
					Arguments: `{"__arg1":"{\"keys\":[\"enter\"]}","description":"我会按下回车键。","speech":"按回车。"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if actions[0].ToolInput != `{"keys":["enter"]}` {
		t.Fatalf("ToolInput = %q, want legacy __arg1", actions[0].ToolInput)
	}
	if got := toolContentFromAction(actions[0]); got != "" {
		t.Fatalf("tool content = %q, want empty without assistant content", got)
	}
}

func TestFunctionAgentParseOutputDoesNotConsumeSpeechArgumentAsMetadata(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "keyboard_tap",
					Arguments: `{"keys":["enter"],"speech":"按回车。"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(actions[0].ToolInput), &input); err != nil {
		t.Fatalf("ToolInput is not JSON: %q err=%v", actions[0].ToolInput, err)
	}
	if _, ok := input["keys"]; !ok || input["speech"] != "按回车。" {
		t.Fatalf("ToolInput should preserve speech as an ordinary tool argument: %#v", input)
	}
	if got := toolContentFromAction(actions[0]); got != "" {
		t.Fatalf("tool content = %q, want empty without assistant content", got)
	}
}

func TestFunctionAgentParseOutputDoesNotSynthesizeMissingToolContent(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "echo",
					Arguments: `{"__arg1":"hello","description":"  "}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	if got := toolContentFromAction(actions[0]); got != "" {
		t.Fatalf("tool content = %q, want empty", got)
	}
}

func TestFunctionAgentParseOutputPreservesSpeechArgumentWhenDisabled(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "say",
					Arguments: `{"speech":"hello","volume":1}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(actions[0].ToolInput), &input); err != nil {
		t.Fatalf("ToolInput is not JSON: %q err=%v", actions[0].ToolInput, err)
	}
	if input["speech"] != "hello" || input["volume"] != float64(1) {
		t.Fatalf("ToolInput lost real speech argument: %#v", input)
	}
	if got := toolContentFromAction(actions[0]); got != "" {
		t.Fatalf("tool content = %q, want empty", got)
	}
}

func TestFunctionAgentParseOutputPreservesRealSpeechArgumentWhenSchemaDefinesIt(t *testing.T) {
	agent := &FunctionAgent{
		OutputKey: "output", Tools: []langtools.Tool{&structuredStubTool{
			stubTool: stubTool{name: "say", description: "Speak text."},
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"speech": map[string]any{"type": "string"},
					"volume": map[string]any{"type": "number"},
				},
			},
		}},
	}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "准备播报。",
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "say",
					Arguments: `{"speech":"hello","volume":1}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("expected no finish, got %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %#v", actions)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(actions[0].ToolInput), &input); err != nil {
		t.Fatalf("ToolInput is not JSON: %q err=%v", actions[0].ToolInput, err)
	}
	if input["speech"] != "hello" || input["volume"] != float64(1) {
		t.Fatalf("ToolInput lost real speech argument: %#v", input)
	}
	if got := toolContentFromAction(actions[0]); got != "准备播报。" {
		t.Fatalf("tool content = %q", got)
	}
}

func TestFunctionAgentToolContentIgnoresLogTextCollisions(t *testing.T) {
	log := formatToolActionLog(
		"echo",
		"{\"__arg1\":\"payload\\nTool content: injected\"}",
		"",
		"\n",
	)

	if got := toolContentFromAction(schema.AgentAction{Log: log}); got != "" {
		t.Fatalf("tool content = %q, want empty", got)
	}
}

func TestFunctionAgentScratchpadDoesNotReplaySpeechArgument(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{{
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
			Log:       formatToolActionLog("echo", `{"input":"hello","speech":"Echoing text."}`, "", "\n"),
			ToolID:    "call_1",
		},
		Observation: "ok",
	}})

	if len(messages) == 0 || len(messages[0].Parts) != 1 {
		t.Fatalf("unexpected scratchpad messages: %#v", messages)
	}
	toolCall, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok {
		t.Fatalf("expected tool call part, got %T", messages[0].Parts[0])
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &args); err != nil {
		t.Fatalf("decode tool arguments: %v", err)
	}
	if args["input"] != "hello" {
		t.Fatalf("scratchpad input = %q", args["input"])
	}
	if _, ok := args["description"]; ok {
		t.Fatalf("scratchpad should not replay description argument: %#v", args)
	}
	if _, ok := args["speech"]; ok {
		t.Fatalf("scratchpad should not replay speech argument: %#v", args)
	}
	if _, ok := args["__arg1"]; ok {
		t.Fatalf("scratchpad should not replay __arg1: %#v", args)
	}
}

func TestFunctionAgentScratchpadDoesNotReplaySpeechArgumentAsContent(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{{
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
			Log:       formatToolActionLog("echo", `{"input":"hello","speech":"Echoing text."}`, "", "\n"),
			ToolID:    "call_1",
		},
		Observation: "ok",
	}})

	if len(messages) == 0 || len(messages[0].Parts) != 1 {
		t.Fatalf("unexpected scratchpad messages: %#v", messages)
	}
	toolCall, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok {
		t.Fatalf("expected tool call part, got %T", messages[0].Parts[0])
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(toolCall.FunctionCall.Arguments), &args); err != nil {
		t.Fatalf("decode tool arguments: %v", err)
	}
	if args["input"] != "hello" {
		t.Fatalf("scratchpad input = %q", args["input"])
	}
	if _, ok := args["speech"]; ok {
		t.Fatalf("scratchpad should not replay speech argument when tool-call speech is enabled: %#v", args)
	}
	if _, ok := args["description"]; ok {
		t.Fatalf("scratchpad should not replay description argument: %#v", args)
	}
	if _, ok := args["__arg1"]; ok {
		t.Fatalf("scratchpad should not replay __arg1: %#v", args)
	}
}

func TestFunctionAgentScratchpadReplaysToolContentAsAssistantText(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{{
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
			Log:       formatToolActionLog("echo", `{"input":"hello"}`, "I will echo text.", "responded: I will echo text.\n"),
			ToolID:    "call_1",
		},
		Observation: "ok",
	}})

	if len(messages) == 0 || len(messages[0].Parts) != 2 {
		t.Fatalf("unexpected scratchpad messages: %#v", messages)
	}
	text, ok := messages[0].Parts[0].(llms.TextContent)
	if !ok || text.Text != "I will echo text." {
		t.Fatalf("expected assistant text before tool call, got %#v", messages[0].Parts[0])
	}
	if _, ok := messages[0].Parts[1].(llms.ToolCall); !ok {
		t.Fatalf("expected tool call after assistant text, got %#v", messages[0].Parts[1])
	}
}

func TestFunctionAgentScratchpadSkipsObservationOnlySteps(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{
		{
			Action:      schema.AgentAction{Tool: "echo", ToolInput: "hello", ToolID: "call_1"},
			Observation: "ok",
		},
		{
			Observation: "Call finish_step when the step is ready for verification or abort_step if blocked.",
		},
	})

	if len(messages) != 3 {
		t.Fatalf("expected 3 scratchpad messages, got %d: %#v", len(messages), messages)
	}
	if text, ok := messages[2].Parts[0].(llms.TextContent); !ok || !strings.Contains(text.Text, "finish_step") {
		t.Fatalf("expected invalid-meta feedback as text, got %#v", messages[2].Parts[0])
	}
	toolCall, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok || toolCall.FunctionCall.Name != "echo" || toolCall.ID != "call_1" {
		t.Fatalf("expected valid tool replay, got %#v", messages[0].Parts[0])
	}
}

func TestFunctionAgentScratchpadSynthesizesMissingToolCallID(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{{
		Action:      schema.AgentAction{Tool: "echo", ToolInput: "hello"},
		Observation: "ok",
	}})

	toolCall, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok {
		t.Fatalf("expected tool call part, got %T", messages[0].Parts[0])
	}
	if toolCall.ID != "scratchpad_0_0" {
		t.Fatalf("tool call id = %q, want synthetic id", toolCall.ID)
	}
	toolResponse, ok := messages[1].Parts[0].(llms.ToolCallResponse)
	if !ok || toolResponse.ToolCallID != "scratchpad_0_0" {
		t.Fatalf("tool response = %#v", messages[1].Parts[0])
	}
}

func TestFunctionAgentScratchpadKeepsScreenshotsUntilBatchThreshold(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}
	steps := screenshotSteps(5)

	messages := agent.constructFunctionScratchPad(steps)
	imageURLs := scratchpadImageURLs(messages)

	if got := scratchpadToolResponseCount(messages); got != 5 {
		t.Fatalf("expected all 5 tool responses to remain, got %d", got)
	}
	if got := scratchpadImagePlaceholderCount(messages); got != 0 {
		t.Fatalf("expected no image placeholders before batch threshold, got %d", got)
	}
	if len(imageURLs) != 5 {
		t.Fatalf("expected 5 screenshot images in context, got %d (%#v)", len(imageURLs), imageURLs)
	}
	for i, url := range imageURLs {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+1)))
		if url != expected {
			t.Fatalf("image %d = %q, want %q", i, url, expected)
		}
	}
}

func TestFunctionAgentScratchpadPrunesScreenshotsInBatches(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}

	messages6 := agent.constructFunctionScratchPad(screenshotSteps(6))
	imageURLs6 := scratchpadImageURLs(messages6)
	if got := scratchpadToolResponseCount(messages6); got != 6 {
		t.Fatalf("expected all 6 tool responses to remain, got %d", got)
	}
	if got := scratchpadImagePlaceholderCount(messages6); got != 2 {
		t.Fatalf("expected first prune batch to replace 2 images, got %d", got)
	}
	if len(imageURLs6) != 4 {
		t.Fatalf("expected 4 full images after first prune batch, got %d (%#v)", len(imageURLs6), imageURLs6)
	}
	for i, url := range imageURLs6 {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+3)))
		if url != expected {
			t.Fatalf("image %d = %q, want %q", i, url, expected)
		}
	}

	messages7 := agent.constructFunctionScratchPad(screenshotSteps(7))
	if !reflect.DeepEqual(messages6, messages7[:len(messages6)]) {
		t.Fatalf("message prefix changed between batch pruning events")
	}
	if got := scratchpadImagePlaceholderCount(messages7); got != 2 {
		t.Fatalf("expected placeholder count to remain stable between prune batches, got %d", got)
	}
	if len(scratchpadImageURLs(messages7)) != 5 {
		t.Fatalf("expected one appended full image between prune batches")
	}

	messages8 := agent.constructFunctionScratchPad(screenshotSteps(8))
	imageURLs8 := scratchpadImageURLs(messages8)
	if got := scratchpadImagePlaceholderCount(messages8); got != 4 {
		t.Fatalf("expected second prune batch to replace 4 total images, got %d", got)
	}
	if len(imageURLs8) != 4 {
		t.Fatalf("expected 4 full images after second prune batch, got %d (%#v)", len(imageURLs8), imageURLs8)
	}
	for i, url := range imageURLs8 {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+5)))
		if url != expected {
			t.Fatalf("second batch image %d = %q, want %q", i, url, expected)
		}
	}
}

func TestFunctionAgentScratchpadUsesConfiguredScreenshotPruning(t *testing.T) {
	agent := &FunctionAgent{
		Tools:             []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
		ScreenshotPruning: executor.ScreenshotPruningConfig{KeepN: 2, Interval: 4},
	}

	messages := agent.constructFunctionScratchPad(screenshotSteps(7))
	if got := scratchpadImagePlaceholderCount(messages); got != 4 {
		t.Fatalf("expected configured prune batch to replace 4 images, got %d", got)
	}
	imageURLs := scratchpadImageURLs(messages)
	if len(imageURLs) != 3 {
		t.Fatalf("expected 3 full images after configured prune batch, got %d (%#v)", len(imageURLs), imageURLs)
	}
	for i, url := range imageURLs {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+5)))
		if url != expected {
			t.Fatalf("configured image %d = %q, want %q", i, url, expected)
		}
	}
}

func screenshotSteps(count int) []schema.AgentStep {
	steps := make([]schema.AgentStep, 0, count)
	for i := 1; i <= count; i++ {
		label := strconv.Itoa(i)
		steps = append(steps, schema.AgentStep{
			Action: schema.AgentAction{
				Tool:   "screenshot",
				Log:    "log-" + label,
				ToolID: "call_" + label,
			},
			Observation: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
				base64.StdEncoding.EncodeToString([]byte("image-"+label)) + `"}`,
		})
	}
	return steps
}

func scratchpadImageURLs(messages []llms.MessageContent) []string {
	var imageURLs []string
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if image, ok := part.(llms.ImageURLContent); ok {
				imageURLs = append(imageURLs, image.URL)
			}
		}
	}
	return imageURLs
}

func scratchpadImagePlaceholderCount(messages []llms.MessageContent) int {
	count := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if text, ok := part.(llms.TextContent); ok && text.Text == "[Image omitted]" {
				count++
			}
		}
	}
	return count
}

func scratchpadToolResponseCount(messages []llms.MessageContent) int {
	count := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if _, ok := part.(llms.ToolCallResponse); ok {
				count++
			}
		}
	}
	return count
}

func TestFunctionAgentToolsAsLLMUsesGenericInputFallback(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{
			name:        "echo",
			description: "Echo text.",
			output:      "ok",
		}},
	}

	tools := agent.toolsAsLLM()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %#v", tools)
	}

	params, ok := tools[0].Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("unexpected parameters type: %T", tools[0].Function.Parameters)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected properties: %#v", params["properties"])
	}
	if _, ok := props["description"]; ok {
		encoded, _ := json.Marshal(params)
		t.Fatalf("schema should not expose description property: %s", encoded)
	}
	if _, ok := props["__arg1"]; ok {
		t.Fatalf("generic fallback schema should not expose __arg1: %#v", props)
	}
	if _, ok := props["speech"]; ok {
		t.Fatalf("generic fallback schema should not expose speech metadata: %#v", props)
	}
	input, ok := props["input"].(map[string]string)
	if !ok {
		t.Fatalf("unexpected input schema: %#v", props["input"])
	}
	if !strings.Contains(input["description"], "plain string input") {
		t.Fatalf("input description does not explain fallback input: %q", input["description"])
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("unexpected required type: %T", params["required"])
	}
	if len(required) != 1 || required[0] != "input" {
		t.Fatalf("required = %#v, want input", required)
	}
	for _, field := range required {
		if field == "speech" {
			t.Fatalf("speech should be optional, required = %#v", required)
		}
	}
}

func TestFunctionAgentToolsAsLLMOmitsSpeechMetadata(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{
			name:        "echo",
			description: "Echo text.",
			output:      "ok",
		}},
	}

	tools := agent.toolsAsLLM()
	params, ok := tools[0].Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("unexpected parameters type: %T", tools[0].Function.Parameters)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected properties: %#v", params["properties"])
	}
	if _, ok := props["speech"]; ok {
		t.Fatalf("schema should not expose speech metadata: %#v", props)
	}
}

type structuredStubTool struct {
	stubTool
	schema map[string]any
}

func (t *structuredStubTool) ArgsSchema() map[string]any { return t.schema }

func TestFunctionAgentToolsAsLLMUsesStructuredSchema(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&structuredStubTool{
			stubTool: stubTool{name: "structured", description: "Structured input."},
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"keys": map[string]any{"type": "array"},
				},
				"required": []string{"keys"},
			},
		}},
	}

	tools := agent.toolsAsLLM()
	params, ok := tools[0].Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("unexpected parameters type: %T", tools[0].Function.Parameters)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected properties: %#v", params["properties"])
	}
	if _, ok := props["keys"]; !ok {
		t.Fatalf("missing structured keys property: %#v", props)
	}
	if _, ok := props["__arg1"]; ok {
		t.Fatalf("structured schema should not expose __arg1: %#v", props)
	}
	if _, ok := props["description"]; ok {
		t.Fatalf("structured schema should not expose description property: %#v", props)
	}
	if _, ok := props["speech"]; ok {
		t.Fatalf("structured schema should not expose speech metadata: %#v", props)
	}
}

func TestPostActionScreenshotToolForwardsStructuredSchema(t *testing.T) {
	inner := &structuredStubTool{
		stubTool: stubTool{name: "touch_gesture", description: "Touch."},
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string"},
			},
		},
	}
	wrapped := newPostActionScreenshotTool(inner, &stubTool{name: "screenshot", output: `{"width":1,"height":1,"format":"jpeg","size":1,"data":"YQ=="}`}, 0)
	structured, ok := wrapped.(structuredInputTool)
	if !ok {
		t.Fatalf("wrapped tool does not implement structuredInputTool")
	}
	props := structured.ArgsSchema()["properties"].(map[string]any)
	if _, ok := props["type"]; !ok {
		t.Fatalf("forwarded schema missing inner properties: %#v", props)
	}
}

func TestBuildImagePartDefaultsMimeType(t *testing.T) {
	part := buildImagePart("", []byte("image-bytes"))
	imagePart, ok := part.(llms.ImageURLContent)
	if !ok {
		t.Fatalf("expected image url content, got %T", part)
	}
	if imagePart.URL != "data:image/png;base64,aW1hZ2UtYnl0ZXM=" {
		t.Fatalf("unexpected image url: %q", imagePart.URL)
	}
}

func TestFunctionAgentCompactsInvalidVisualObservation(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}
	observation := strings.Repeat("x", maxToolObservationRunes+10)

	toolContent, followups := agent.observationMessagesForStep(schema.AgentStep{
		Action:      schema.AgentAction{Tool: "screenshot"},
		Observation: observation,
	}, true)

	if followups != nil {
		t.Fatalf("expected no followup messages for invalid visual observation, got %#v", followups)
	}
	if toolContent == observation {
		t.Fatal("invalid visual observation was not compacted")
	}
	if !strings.Contains(toolContent, "[truncated 10 chars]") {
		t.Fatalf("unexpected compacted observation suffix: %q", toolContent[len(toolContent)-40:])
	}
}

func TestFunctionAgentPreservesFullSkillReadObservation(t *testing.T) {
	agent := &FunctionAgent{Tools: []langtools.Tool{&stubTool{name: "skill_read"}}}
	observation := strings.Repeat("x", maxToolObservationRunes+10)

	toolContent, followups := agent.observationMessagesForStep(schema.AgentStep{
		Action:      schema.AgentAction{Tool: "skill_read"},
		Observation: observation,
	}, true)

	if followups != nil {
		t.Fatalf("expected no followup messages, got %#v", followups)
	}
	if toolContent != observation {
		t.Fatalf("skill_read observation was unexpectedly compacted: got %d runes, want %d", len([]rune(toolContent)), len([]rune(observation)))
	}
}

func TestFunctionAgentCapsOversizedSkillReadObservation(t *testing.T) {
	agent := &FunctionAgent{Tools: []langtools.Tool{&stubTool{name: "skill_read"}}}
	observation := strings.Repeat("x", maxSkillReadObservationRunes+10)

	toolContent, _ := agent.observationMessagesForStep(schema.AgentStep{
		Action:      schema.AgentAction{Tool: "skill_read"},
		Observation: observation,
	}, true)

	if !strings.Contains(toolContent, "[truncated 10 chars]") {
		t.Fatalf("oversized skill_read observation was not capped: %q", toolContent[len(toolContent)-40:])
	}
}

func TestFunctionAgentRejectsEmptyVisualObservationData(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}
	observation := `{"width":800,"height":600,"format":"jpeg","size":0,"data":""}`
	step := schema.AgentStep{
		Action:      schema.AgentAction{Tool: "screenshot"},
		Observation: observation,
	}

	if agent.countVisualObservations([]schema.AgentStep{step}) != 0 {
		t.Fatal("empty screenshot data counted as a visual observation")
	}
	toolContent, followups := agent.observationMessagesForStep(step, true)
	if followups != nil {
		t.Fatalf("expected no followup messages for empty screenshot data, got %#v", followups)
	}
	if toolContent != observation {
		t.Fatalf("unexpected compacted observation: %q", toolContent)
	}
}

func TestFunctionAgentRejectsVisualObservationWithoutDimensions(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}
	observation := `{"width":0,"height":240,"format":"jpeg","size":4,"data":"` +
		base64.StdEncoding.EncodeToString([]byte("image-bytes")) + `"}`
	step := schema.AgentStep{
		Action:      schema.AgentAction{Tool: "screenshot"},
		Observation: observation,
	}

	if agent.countVisualObservations([]schema.AgentStep{step}) != 0 {
		t.Fatal("screenshot with invalid dimensions counted as a visual observation")
	}
	toolContent, followups := agent.observationMessagesForStep(step, true)
	if followups != nil {
		t.Fatalf("expected no followup messages for invalid screenshot dimensions, got %#v", followups)
	}
	if toolContent != observation {
		t.Fatalf("unexpected compacted observation: %q", toolContent)
	}
}

func TestFunctionAgentPostActionScreenshotWarnsWhenScreenDidNotChange(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "keyboard_tap", visual: true}},
	}
	observation := `{"action_output":"ok","screen_changed":false,"screen_stable":true,"stable_wait_ms":250,"width":800,"height":600,"format":"jpeg","size":4,"data":"` +
		base64.StdEncoding.EncodeToString([]byte("img1")) + `"}`
	step := schema.AgentStep{
		Action:      schema.AgentAction{Tool: "keyboard_tap"},
		Observation: observation,
	}

	toolContent, followups := agent.observationMessagesForStep(step, true)

	if !strings.Contains(toolContent, "No visible screen change was observed") {
		t.Fatalf("toolContent missing screen_changed warning: %q", toolContent)
	}
	if !strings.Contains(toolContent, "Do not assume the action succeeded") {
		t.Fatalf("toolContent missing success warning: %q", toolContent)
	}
	if !strings.Contains(toolContent, "The screen was stable when the screenshot was captured") {
		t.Fatalf("toolContent missing stable summary: %q", toolContent)
	}
	if len(followups) != 1 {
		t.Fatalf("expected one followup screenshot message, got %#v", followups)
	}
	if text := messageText(followups); !strings.Contains(text, "fixed normalized coordinate plane") {
		t.Fatalf("screenshot followup missing adjacent coordinate guidance: %q", text)
	}
	if len(followups[0].Parts) < 3 {
		t.Fatalf("unexpected screenshot followup parts: %#v", followups[0].Parts)
	}
	guidance, ok := followups[0].Parts[len(followups[0].Parts)-2].(llms.TextContent)
	if !ok || !strings.Contains(guidance.Text, "fixed normalized coordinate plane") {
		t.Fatalf("coordinate guidance must immediately precede the screenshot: %#v", followups[0].Parts)
	}
	if _, ok := followups[0].Parts[len(followups[0].Parts)-1].(llms.ImageURLContent); !ok {
		t.Fatalf("screenshot must immediately follow coordinate guidance: %#v", followups[0].Parts)
	}
}

func TestFunctionAgentPostActionScreenshotRequestsExplicitVerification(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "touch_gesture", visual: true}},
	}
	observation := `{"action_output":"ok","verification_required":true,"width":800,"height":600,"format":"jpeg","size":4,"data":"` +
		base64.StdEncoding.EncodeToString([]byte("img1")) + `"}`
	step := schema.AgentStep{
		Action:      schema.AgentAction{Tool: "touch_gesture"},
		Observation: observation,
	}

	toolContent, _ := agent.observationMessagesForStep(step, true)
	if !strings.Contains(toolContent, "verification_required=true") || !strings.Contains(toolContent, "public screenshot tool") {
		t.Fatalf("toolContent missing explicit verification request: %q", toolContent)
	}
}
