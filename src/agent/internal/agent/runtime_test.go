package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	langtools "github.com/tmc/langchaingo/tools"
)

type testModelResolver struct {
	model llms.Model
	calls int
}

func (r *testModelResolver) Get() (llms.Model, error) {
	r.calls++
	return r.model, nil
}

func (r *testModelResolver) CallOptions() []chains.ChainCallOption {
	return nil
}

func TestRuntimeRun(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		Instruction: "Answer directly.",
	}

	resolver := &testModelResolver{
		model: fakellm.NewFakeLLM([]string{
			"completed",
		}),
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}), NewSkillIndex())
	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(result.Memory) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(result.Memory))
	}
}

type scriptedModel struct {
	responses    []*llms.ContentResponse
	callCount    int
	sawStreaming []bool
	messages     [][]llms.MessageContent
}

func (m *scriptedModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	m.sawStreaming = append(m.sawStreaming, callOptions.StreamingFunc != nil)
	m.messages = append(m.messages, messages)

	if callOptions.StreamingFunc != nil && m.callCount < len(m.responses) {
		content := m.responses[m.callCount].Choices[0].Content
		if content != "" {
			if err := callOptions.StreamingFunc(ctx, []byte("chunk:"+content)); err != nil {
				return nil, err
			}
		}
	}

	if m.callCount >= len(m.responses) {
		t := &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: ""}}}
		return t, nil
	}

	response := m.responses[m.callCount]
	m.callCount++
	return response, nil
}

func (m *scriptedModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

type stubTool struct {
	name        string
	description string
	output      string
	inputs      []string
}

func (t *stubTool) Name() string { return t.name }

func (t *stubTool) Description() string { return t.description }

func (t *stubTool) Call(_ context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	return t.output, nil
}

func TestRuntimeRunOpenRouterUsesToolsWithoutStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	if len(model.sawStreaming) != 2 || model.sawStreaming[0] || model.sawStreaming[1] {
		t.Fatalf("expected non-streaming tool calls, got %#v", model.sawStreaming)
	}
}

func TestRuntimeRunFakeProviderUsesFunctionAgentToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
}

func TestRuntimeRunOpenRouterStreamsOnlyWhenRequested(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "completed",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}),
		NewSkillIndex(),
	)

	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "hello",
		StreamWriter: &stream,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.sawStreaming) != 1 || !model.sawStreaming[0] {
		t.Fatalf("expected streaming call, got %#v", model.sawStreaming)
	}
	if stream.String() != "chunk:completed" {
		t.Fatalf("unexpected stream output: %q", stream.String())
	}
}

func TestRuntimeRunScreenshotAddsBinaryImageObservation(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "screenshot",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The screenshot shows a UI.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "screenshot",
		description: "Capture a screenshot from the connected display.",
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "屏幕上有什么？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The screenshot shows a UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(model.messages))
	}

	secondCall := model.messages[1]
	var foundToolResponse bool
	var foundBinaryImage bool

	for _, msg := range secondCall {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized, got raw payload")
					}
				}
			case llms.BinaryContent:
				foundBinaryImage = true
				if p.MIMEType != "image/jpeg" {
					t.Fatalf("unexpected MIME type: %q", p.MIMEType)
				}
				if !bytes.Equal(p.Data, jpegBytes) {
					t.Fatalf("unexpected image bytes: %#v", p.Data)
				}
			}
		}
	}

	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response in second model call")
	}
	if !foundBinaryImage {
		t.Fatalf("expected screenshot binary image in second model call")
	}
}
