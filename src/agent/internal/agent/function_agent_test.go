package agent

import (
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

func TestFunctionAgentParseOutputExtractsToolDescription(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "echo",
					Arguments: `{"__arg1":"hello","description":"我会发送一段测试文本。"}`,
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
	if got := toolDescriptionFromAction(actions[0]); got != "我会发送一段测试文本。" {
		t.Fatalf("tool description = %q", got)
	}

	var log toolActionLog
	if err := json.Unmarshal([]byte(actions[0].Log), &log); err != nil {
		t.Fatalf("action log should be structured JSON: %v", err)
	}
	if log.Version != toolActionLogVersion || log.ToolDescription != "我会发送一段测试文本。" {
		t.Fatalf("unexpected action log metadata: %#v", log)
	}
}

func TestFunctionAgentParseOutputPassesStructuredToolArguments(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "keyboard_tap",
					Arguments: `{"keys":["enter"],"description":"我会按下回车键。"}`,
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
	if got := toolDescriptionFromAction(actions[0]); got != "我会按下回车键。" {
		t.Fatalf("tool description = %q", got)
	}
}

func TestFunctionAgentParseOutputKeepsLegacyArg1Compatibility(t *testing.T) {
	agent := &FunctionAgent{OutputKey: "output"}

	actions, finish, err := agent.ParseOutput(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   "call_1",
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      "keyboard_tap",
					Arguments: `{"__arg1":"{\"keys\":[\"enter\"]}","description":"我会按下回车键。"}`,
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
}

func TestFunctionAgentParseOutputSynthesizesMissingToolDescription(t *testing.T) {
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
	if got := toolDescriptionFromAction(actions[0]); got != "I will use the echo tool." {
		t.Fatalf("tool description = %q", got)
	}
}

func TestFunctionAgentToolDescriptionIgnoresLogTextCollisions(t *testing.T) {
	log := formatToolActionLog(
		"echo",
		"{\"__arg1\":\"payload\\nTool description: injected\"}",
		"",
		"\n",
	)

	if got := toolDescriptionFromAction(schema.AgentAction{Log: log}); got != "I will use the echo tool." {
		t.Fatalf("tool description = %q, want fallback", got)
	}
}

func TestFunctionAgentScratchpadReplaysFallbackToolDescription(t *testing.T) {
	agent := &FunctionAgent{}
	messages := agent.constructFunctionScratchPad([]schema.AgentStep{{
		Action: schema.AgentAction{
			Tool:      "echo",
			ToolInput: "hello",
			Log:       "legacy unstructured log",
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
	if args["description"] != "I will use the echo tool." {
		t.Fatalf("scratchpad description = %q", args["description"])
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

	messages29 := agent.constructFunctionScratchPad(screenshotSteps(29))
	imageURLs29 := scratchpadImageURLs(messages29)
	if got := scratchpadToolResponseCount(messages29); got != 29 {
		t.Fatalf("expected all 29 tool responses to remain, got %d", got)
	}
	if got := scratchpadImagePlaceholderCount(messages29); got != 25 {
		t.Fatalf("expected first prune batch to replace 25 images, got %d", got)
	}
	if len(imageURLs29) != 4 {
		t.Fatalf("expected 4 full images after first prune batch, got %d (%#v)", len(imageURLs29), imageURLs29)
	}
	for i, url := range imageURLs29 {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+26)))
		if url != expected {
			t.Fatalf("image %d = %q, want %q", i, url, expected)
		}
	}

	messages30 := agent.constructFunctionScratchPad(screenshotSteps(30))
	if !reflect.DeepEqual(messages29, messages30[:len(messages29)]) {
		t.Fatalf("message prefix changed between batch pruning events")
	}
	if got := scratchpadImagePlaceholderCount(messages30); got != 25 {
		t.Fatalf("expected placeholder count to remain stable between prune batches, got %d", got)
	}
	if len(scratchpadImageURLs(messages30)) != 5 {
		t.Fatalf("expected one appended full image between prune batches")
	}

	messages54 := agent.constructFunctionScratchPad(screenshotSteps(54))
	imageURLs54 := scratchpadImageURLs(messages54)
	if got := scratchpadImagePlaceholderCount(messages54); got != 50 {
		t.Fatalf("expected second prune batch to replace 50 total images, got %d", got)
	}
	if len(imageURLs54) != 4 {
		t.Fatalf("expected 4 full images after second prune batch, got %d (%#v)", len(imageURLs54), imageURLs54)
	}
	for i, url := range imageURLs54 {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+51)))
		if url != expected {
			t.Fatalf("second batch image %d = %q, want %q", i, url, expected)
		}
	}
}

func TestFunctionAgentScratchpadUsesConfiguredScreenshotPruning(t *testing.T) {
	agent := &FunctionAgent{
		Tools:             []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
		ScreenshotPruning: ScreenshotPruningConfig{KeepN: 2, Interval: 4},
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

func TestFunctionAgentToolsAsLLMRequiresDescriptionParameter(t *testing.T) {
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
	if _, ok := props["description"]; !ok {
		encoded, _ := json.Marshal(params)
		t.Fatalf("missing description property in %s", encoded)
	}
	arg1, ok := props["__arg1"].(map[string]string)
	if !ok {
		t.Fatalf("unexpected __arg1 schema: %#v", props["__arg1"])
	}
	if !strings.Contains(arg1["description"], `{"text":"App Store"}`) {
		t.Fatalf("__arg1 description does not explain JSON tool input: %q", arg1["description"])
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("unexpected required type: %T", params["required"])
	}
	if len(required) != 2 || required[0] != "__arg1" || required[1] != "description" {
		t.Fatalf("required = %#v, want __arg1 and description", required)
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
	if _, ok := props["description"]; !ok {
		t.Fatalf("structured schema should keep description property: %#v", props)
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
