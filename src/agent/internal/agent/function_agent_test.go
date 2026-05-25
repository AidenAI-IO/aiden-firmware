package agent

import (
	"encoding/base64"
	"encoding/json"
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

func TestFunctionAgentScratchpadKeepsOnlyLatestThreeScreenshotImages(t *testing.T) {
	agent := &FunctionAgent{
		Tools: []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
	}
	steps := make([]schema.AgentStep, 0, 5)
	for i := 1; i <= 5; i++ {
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

	messages := agent.constructFunctionScratchPad(steps)

	var imageURLs []string
	var toolResponses int
	var omittedNotices int
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ImageURLContent:
				imageURLs = append(imageURLs, p.URL)
			case llms.ToolCallResponse:
				toolResponses++
				if strings.Contains(p.Content, "only the latest 3 screenshot observations are attached") {
					omittedNotices++
				}
			}
		}
	}

	if toolResponses != 5 {
		t.Fatalf("expected all 5 tool responses to remain, got %d", toolResponses)
	}
	if omittedNotices != 2 {
		t.Fatalf("expected 2 older screenshot omission notices, got %d", omittedNotices)
	}
	if len(imageURLs) != 3 {
		t.Fatalf("expected 3 screenshot images in context, got %d (%#v)", len(imageURLs), imageURLs)
	}
	for i, url := range imageURLs {
		expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("image-"+strconv.Itoa(i+3)))
		if url != expected {
			t.Fatalf("image %d = %q, want %q", i, url, expected)
		}
	}
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
