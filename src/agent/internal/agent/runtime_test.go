package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestEffectiveMaxIterationsDefaultsAndUnlimited(t *testing.T) {
	if got := effectiveMaxIterations(-1); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(-1) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(0); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(0) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(10); got != 10 {
		t.Fatalf("effectiveMaxIterations(10) = %d, want 10", got)
	}
}

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

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())
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
	visual      bool
	inputs      []string
}

func (t *stubTool) Name() string { return t.name }

func (t *stubTool) Description() string { return t.description }

func (t *stubTool) ReturnsVisualObservation() bool { return t.visual }

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
		NewMemoryManager(""),
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
		NewMemoryManager(""),
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
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
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
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
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
	var foundImageURL bool

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
			case llms.ImageURLContent:
				foundImageURL = true
				expectedPrefix := "data:image/jpeg;base64,"
				if !strings.HasPrefix(p.URL, expectedPrefix) {
					t.Fatalf("unexpected image URL prefix: %q", p.URL)
				}
				if p.URL != expectedPrefix+base64.StdEncoding.EncodeToString(jpegBytes) {
					t.Fatalf("unexpected image URL payload: %q", p.URL)
				}
			}
		}
	}

	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL in second model call")
	}
}

func TestRuntimeRunScreenshotImageSurvivesCallbackToolWrapping(t *testing.T) {
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
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	var streamBuf bytes.Buffer
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input:        "屏幕上有什么？",
		StreamWriter: &streamBuf,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(model.messages) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(model.messages))
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized when wrapped by callbackTool, got raw payload")
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response when wrapped by callbackTool")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL when wrapped by callbackTool")
	}
}

func TestRuntimeRunKeyboardToolAddsPostActionImageObservation(t *testing.T) {
	jpegBytes := []byte("keyboard-post-action-jpeg")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "keyboard_tap",
							Arguments: `{"keys":["enter"]}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The keyboard action updated the UI.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "keyboard_tap",
		description: "Press and release keyboard keys.",
		visual:      true,
		output: `{"action_output":"ok","width":800,"height":600,"format":"jpeg","size":25,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use input tools when needed.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"keyboard_tap": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "press enter"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The keyboard action updated the UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected keyboard tool response to be summarized, got raw screenshot payload")
					}
					if !strings.Contains(p.Content, `keyboard_tap completed with output "ok"`) {
						t.Fatalf("unexpected keyboard tool response summary: %q", p.Content)
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected keyboard tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected keyboard post-action screenshot image URL in second model call")
	}
}

func TestRuntimeCallbackHandlerCapturesUsageMetrics(t *testing.T) {
	metrics := &RunMetrics{}
	handler := &runtimeCallbackHandler{metrics: metrics}

	handler.HandleLLMGenerateContentEnd(context.Background(), &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			GenerationInfo: map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 34,
				"total_tokens":      46,
			},
		}},
	})

	if metrics.PromptTokens != 12 || metrics.CompletionTokens != 34 || metrics.TotalTokens != 46 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestRuntimeRunCapturesUsageMetricsFromDirectModelCall(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{{
			Choices: []*llms.ContentChoice{{
				Content: "completed",
				GenerationInfo: map[string]any{
					"prompt_tokens":     600,
					"completion_tokens": 40,
					"total_tokens":      640,
				},
			}},
		}},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Metrics == nil || result.Metrics.PromptTokens != 600 || result.Metrics.CompletionTokens != 40 || result.Metrics.TotalTokens != 640 {
		t.Fatalf("unexpected metrics: %#v", result.Metrics)
	}
}

func TestRuntimeRunResetsPromptTokensWhenUsageUnavailable(t *testing.T) {
	manager := NewMemoryManager("")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "with usage",
					GenerationInfo: map[string]any{
						"prompt_tokens": 600,
					},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "without usage",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		manager,
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "first"}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 600 {
		t.Fatalf("expected first run prompt tokens 600, got %d", got)
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "second"}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 0 {
		t.Fatalf("expected missing usage to reset prompt tokens, got %d", got)
	}
}

func TestRuntimePersistsMemoryUnderConfigDir(t *testing.T) {
	configDir := t.TempDir()

	firstRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer firstRuntime.Close()

	firstRuntime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"first"}),
	}

	firstResult, err := firstRuntime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if len(firstResult.Memory) != 2 {
		t.Fatalf("expected 2 memory entries after first run, got %d", len(firstResult.Memory))
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

	secondRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() second error = %v", err)
	}
	defer secondRuntime.Close()

	secondRuntime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"second"}),
	}

	secondResult, err := secondRuntime.Run(context.Background(), RunRequest{Input: "again"})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(secondResult.Memory) != 4 {
		t.Fatalf("expected 4 memory entries after reload, got %d", len(secondResult.Memory))
	}
	if secondResult.Memory[0].Role != "human" || secondResult.Memory[0].Content != "hello" {
		t.Fatalf("expected first persisted message to be restored, got %#v", secondResult.Memory[0])
	}
}

func TestRuntimeRunCompactsRealChatExchangesBeyondWindow(t *testing.T) {
	configDir := t.TempDir()
	responses := make([]string, 30)
	for i := range responses {
		responses[i] = "ok"
	}
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake", Responses: responses},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	inputs := []string{
		"我是硬件产品经理，平时用中文沟通，关注开发板 agent 端到端行为。",
		"记一下，以后处理蓝海报销App超过100元的提交或付款动作，必须先给风险摘要并等我确认。",
	}
	for i := 0; i < 21; i++ {
		inputs = append(inputs, "填充对话轮次")
	}
	for _, input := range inputs {
		if _, err := runtime.Run(context.Background(), RunRequest{Input: input}); err != nil {
			t.Fatalf("Run(%q) error = %v", input, err)
		}
	}

	events := readSessionEvents(t, filepath.Join(configDir, "memory", "session", "events.jsonl"))
	if len(events) > 20 {
		t.Fatalf("expected hot window events <= 20, got %d", len(events))
	}
	chunks, err := NewSessionMemoryStore(filepath.Join(configDir, "memory", "session")).RecallChunks(context.Background(), ChunkRecallQuery{Entities: []string{"蓝海报销App"}, Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected compacted chunk from real runs, got %d", len(chunks))
	}
}

func TestRuntimeRegistersMemoryRecallToolsWhenConfigDirSet(t *testing.T) {
	runtime, err := NewRuntime(Config{
		ConfigDir:     t.TempDir(),
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	if _, ok := runtime.tools.Get("recall_session_chunks"); !ok {
		t.Fatalf("expected runtime to register recall_session_chunks")
	}
	if _, ok := runtime.tools.Get("recall_memory"); !ok {
		t.Fatalf("expected runtime to register recall_memory")
	}
}

func TestRuntimeRunIncludesUserAttachments(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "processed",
				}},
			},
		},
	}

	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use the provided media when answering.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "Describe the uploaded media.",
		Attachments: []InputAttachment{
			{
				Kind:     AttachmentKindImage,
				Name:     "photo.png",
				MIMEType: "image/png",
				Data:     []byte{0x89, 0x50, 0x4e, 0x47},
			},
			{
				Kind:     AttachmentKindAudio,
				Name:     "note.wav",
				MIMEType: "audio/wav",
				Data:     []byte{0x52, 0x49, 0x46, 0x46},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "processed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(model.messages))
	}

	lastCall := model.messages[0]
	if len(lastCall) == 0 {
		t.Fatalf("expected messages in model call")
	}
	userMessage := lastCall[len(lastCall)-1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected final message to be human, got %q", userMessage.Role)
	}

	var textContent string
	var imageURL string
	var binaryMIMEs []string
	for _, part := range userMessage.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			textContent = p.Text
		case llms.ImageURLContent:
			imageURL = p.URL
		case llms.BinaryContent:
			binaryMIMEs = append(binaryMIMEs, p.MIMEType)
		}
	}

	if !strings.Contains(textContent, "photo.png") || !strings.Contains(textContent, "note.wav") {
		t.Fatalf("expected attachment names in prompt text, got %q", textContent)
	}
	if imageURL == "" || !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected image attachment as data URL, got %q", imageURL)
	}
	if len(binaryMIMEs) != 1 || binaryMIMEs[0] != "audio/wav" {
		t.Fatalf("unexpected binary attachment MIME types: %#v", binaryMIMEs)
	}
}

func TestRuntimeClearMemoryRemovesPersistedFile(t *testing.T) {
	configDir := t.TempDir()
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	runtime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"first"}),
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

	if err := runtime.ClearMemory(context.Background()); err != nil {
		t.Fatalf("ClearMemory() error = %v", err)
	}

	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Fatalf("expected memory file to be removed, stat err = %v", err)
	}
}
