package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	langtools "github.com/tmc/langchaingo/tools"
)

type stubSTTClient struct {
	transcript string
	inputs     [][]byte
}

func (s *stubSTTClient) TranscribeWAV(wavData []byte) (string, error) {
	s.inputs = append(s.inputs, append([]byte(nil), wavData...))
	return s.transcript, nil
}

func TestServerHandleChatReturnsToolHistory(t *testing.T) {
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
	server := NewServer(runtime, ":0")

	body := bytes.NewBufferString(`{"message":"当前音量是多少？"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Response != "The current audio volume is 42." {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) != 4 {
		t.Fatalf("expected 4 history entries, got %d", len(resp.History))
	}

	if resp.History[0].Type != "user" || resp.History[0].Content != "当前音量是多少？" {
		t.Fatalf("unexpected first history message: %#v", resp.History[0])
	}
	if resp.History[1].Type != "tool_call" || resp.History[1].ToolName != "audio_volume" || resp.History[1].ToolInput != "{}" {
		t.Fatalf("unexpected tool_call message: %#v", resp.History[1])
	}
	if resp.History[2].Type != "tool_result" || resp.History[2].ToolName != "audio_volume" || resp.History[2].Content != `{"volume":42}` {
		t.Fatalf("unexpected tool_result message: %#v", resp.History[2])
	}
	if resp.History[3].Type != "assistant" || resp.History[3].Content != "The current audio volume is 42." {
		t.Fatalf("unexpected assistant message: %#v", resp.History[3])
	}
}

func TestServerHistoryEndpointIncludesToolMessages(t *testing.T) {
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		history: []Message{
			{Type: "user", Content: "hello"},
			{Type: "tool_call", ToolName: "screenshot", ToolInput: "{}"},
			{Type: "tool_result", ToolName: "screenshot", Content: `{"width":100}`},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	server.handleHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var history []Message
	if err := json.NewDecoder(rec.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}
	if history[1].Type != "tool_call" || history[2].Type != "tool_result" {
		t.Fatalf("unexpected history payload: %#v", history)
	}
}

func TestServerHandleChatWithAudioAttachmentUsesSTT(t *testing.T) {
	stt := &stubSTTClient{transcript: "你好，帮我总结一下"}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: fakellm.NewFakeLLM([]string{"已处理"})},
		NewMemoryManager(),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := &Server{
		runtime:   runtime,
		history:   make([]Message, 0),
		sttClient: stt,
	}

	payload, err := json.Marshal(ChatRequest{
		Attachments: []MessageAttachment{{
			Kind:     AttachmentKindAudio,
			Name:     "recording.wav",
			MIMEType: "audio/wav",
			Data:     base64.StdEncoding.EncodeToString([]byte("RIFFtest")),
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(stt.inputs) != 1 {
		t.Fatalf("expected 1 STT invocation, got %d", len(stt.inputs))
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Response != "已处理" {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(resp.History))
	}
	if resp.History[0].Content != "你好，帮我总结一下" {
		t.Fatalf("expected transcript as user content, got %#v", resp.History[0])
	}
	if len(resp.History[0].Attachments) != 1 || resp.History[0].Attachments[0].Transcript != "你好，帮我总结一下" {
		t.Fatalf("expected transcript on audio attachment, got %#v", resp.History[0].Attachments)
	}
}

func TestServerToolCatalogEndpoint(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0")

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	rec := httptest.NewRecorder()

	server.handleToolCatalog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ToolCatalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(resp.Tools))
	}
	if resp.Tools[0].Name != "shell" {
		t.Fatalf("unexpected tool descriptor: %#v", resp.Tools[0])
	}
	if resp.Tools[0].HTTP.Path != "/api/tools/shell" {
		t.Fatalf("unexpected tool path: %#v", resp.Tools[0].HTTP)
	}
	if resp.Tools[0].ExampleInput != `{"command":"pwd"}` {
		t.Fatalf("unexpected example input: %q", resp.Tools[0].ExampleInput)
	}
}

func TestServerToolInvokeEndpointAcceptsStructuredJSON(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      `{"status":"ok"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0")

	body := bytes.NewBufferString(`{"input":{"command":"pwd"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/shell", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleToolInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool input: %#v", tool.inputs)
	}

	var resp ToolInvokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Tool.Name != "shell" || resp.RawInput != `{"command":"pwd"}` || resp.Output != `{"status":"ok"}` || resp.IsError {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestServerToolInvokeEndpointAcceptsPlainStringInput(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{
		Name:         "planner",
		Description:  "Planning skill",
		Instructions: "Plan before acting.",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)
	server := NewServer(runtime, ":0")

	body := bytes.NewBufferString(`{"input":"planner"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/activate_skill", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleToolInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ToolInvokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RawInput != "planner" {
		t.Fatalf("expected raw input planner, got %#v", resp)
	}
	if resp.Tool.Name != "activate_skill" {
		t.Fatalf("unexpected tool name: %#v", resp.Tool)
	}
	if resp.IsError {
		t.Fatalf("expected successful activation, got %#v", resp)
	}
}

func TestServerToolSkillsEndpointReturnsGeneratedSkills(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0")

	req := httptest.NewRequest(http.MethodGet, "https://device.example/api/tool-skills", nil)
	req.Header.Set("X-Forwarded-Host", "192.168.50.57:8080")
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()

	server.handleToolSkills(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ToolSkillsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Skills) != 1 {
		t.Fatalf("expected exactly 1 generated skill, got %d", len(resp.Skills))
	}

	skill := resp.Skills[0]
	if skill.Name != "aiden-http-tool-suite" {
		t.Fatalf("unexpected skill name: %#v", skill)
	}
	if len(skill.ToolNames) != 1 || skill.ToolNames[0] != "shell" {
		t.Fatalf("unexpected tool list: %#v", skill.ToolNames)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("/api/tools/{tool_name}")) {
		t.Fatalf("unexpected skill markdown: %q", skill.Markdown)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("http://192.168.50.57:8080")) {
		t.Fatalf("expected forwarded base URL in markdown: %q", skill.Markdown)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("NO_PROXY")) {
		t.Fatalf("expected proxy guidance in markdown: %q", skill.Markdown)
	}
}

func TestRequestBaseURLPrefersForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://local.invalid/api/tool-skills", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "device.example:8443, proxy.local")

	got := requestBaseURL(req)
	if got != "https://device.example:8443" {
		t.Fatalf("requestBaseURL = %q, want %q", got, "https://device.example:8443")
	}
}
