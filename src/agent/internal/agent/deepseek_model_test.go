package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentmessages "aiden-agent/internal/agent/messages"
	"github.com/tmc/langchaingo/llms"
)

func TestDeepSeekVisionToolContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, effort := range []string{"", "none", "low", "high", "max"} {
			name := effort
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				thinking := effort != "" && effort != "none"
				const reasoning = "Inspect the screen before taking action."
				type capturedRequest struct {
					Model           string              `json:"model"`
					Thinking        compatibleThinking  `json:"thinking"`
					ReasoningEffort string              `json:"reasoning_effort"`
					Reasoning       json.RawMessage     `json:"reasoning"`
					Messages        []compatibleMessage `json:"messages"`
					Tools           []compatibleTool    `json:"tools"`
				}
				var requests []capturedRequest
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.String() != "https://api.deepseek.com/chat/completions" || req.Method != http.MethodPost {
						t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
					}
					if req.Header.Get("Authorization") != "Bearer deepseek-test-key" {
						t.Fatal("missing provider credential")
					}
					requests = append(requests, capturedRequest{})
					if err := json.NewDecoder(req.Body).Decode(&requests[len(requests)-1]); err != nil {
						t.Fatal(err)
					}
					message := map[string]any{"role": "assistant", "content": "done"}
					finish := "stop"
					if len(requests) == 1 {
						message["content"] = ""
						message["tool_calls"] = []map[string]any{{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "inspect", "arguments": "{}"}}}
						finish = "tool_calls"
					}
					if thinking {
						message["reasoning_content"] = reasoning
					}
					field := "message"
					if stream {
						field = "delta"
					}
					body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{field: message, "finish_reason": finish}}})
					if err != nil {
						t.Fatal(err)
					}
					if stream {
						body = []byte("data: " + string(body) + "\n\ndata: [DONE]\n\n")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
				})}
				cfg := ModelConfig{Provider: "deepseek", Model: "deepseek-flash", APIKey: "deepseek-test-key", ReasoningEffort: effort}
				built, err := buildDeepSeekModel(ModelBuildContext{HTTPClient: client}, cfg)
				if err != nil {
					t.Fatal(err)
				}
				manager := NewModelManager(cfg, ProxyConfig{})
				manager.model = built
				imagePath := filepath.Join(t.TempDir(), "screen.png")
				if err := os.WriteFile(imagePath, []byte("test-image"), 0600); err != nil {
					t.Fatal(err)
				}
				history := []agentmessages.Message{{
					Role: agentmessages.MessageRoleUser, Content: "Inspect this image",
					Attachments: []agentmessages.Attachment{{MIMEType: "image/png", FilePath: imagePath}},
				}}
				opts := []llms.CallOption{llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "inspect", Parameters: map[string]any{"type": "object"}}}})}
				if stream {
					opts = append(opts, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
				}
				response, err := manager.GenerateContentFromMessageList(context.Background(), history, opts...)
				if err != nil {
					t.Fatal(err)
				}
				if len(response.Choices) != 1 || len(response.Choices[0].ToolCalls) != 1 {
					t.Fatalf("missing tool call: %+v", response)
				}
				history = append(history, agentmessages.ConvertChoiceToContextManagerMessage(*response.Choices[0]), agentmessages.Message{
					Role:        agentmessages.MessageRoleToolResult,
					ToolResults: []agentmessages.ToolResult{{ToolCallID: "call_1", Name: "inspect", Content: "ok"}},
				})
				response, err = manager.GenerateContentFromMessageList(context.Background(), history, opts...)
				if err != nil || len(response.Choices) != 1 || response.Choices[0].Content != "done" {
					t.Fatalf("continuation = %+v, %v", response, err)
				}
				// The next user turn must also replay reasoning from the final
				// assistant reply, even though that reply did not call a tool.
				history = append(history, agentmessages.ConvertChoiceToContextManagerMessage(*response.Choices[0]), agentmessages.Message{Role: agentmessages.MessageRoleUser, Content: "Again"})
				if _, err := manager.GenerateContentFromMessageList(context.Background(), history, opts...); err != nil {
					t.Fatal(err)
				}
				wantEffort, wantThinking := "none", "disabled"
				if thinking {
					wantEffort, wantThinking = effort, "enabled"
				}
				for _, request := range requests {
					if request.Model != cfg.Model || request.Thinking.Type != wantThinking || request.ReasoningEffort != wantEffort || request.Reasoning != nil || len(request.Tools) != 1 {
						t.Fatalf("unexpected request profile: %+v", request)
					}
					for _, message := range request.Messages {
						if thinking && message.Role == "assistant" {
							if message.ReasoningContent == nil || *message.ReasoningContent != reasoning {
								t.Fatalf("assistant reasoning was not replayed: %+v", message)
							}
						} else if message.ReasoningContent != nil {
							t.Fatalf("unexpected reasoning field: %+v", message)
						}
					}
				}
				imageJSON, _ := json.Marshal(requests[0].Messages[0].Content)
				if !strings.Contains(string(imageJSON), "data:image/png;base64,dGVzdC1pbWFnZQ==") {
					t.Fatalf("image input missing: %s", imageJSON)
				}
				toolResult := requests[1].Messages[2]
				if toolResult.Role != "tool" || toolResult.ToolCallID != "call_1" || toolResult.Content != "ok" {
					t.Fatalf("tool result pairing lost: %+v", toolResult)
				}
			})
		}
	}
}

func TestDeepSeekRejectsUnsupportedAPIModes(t *testing.T) {
	for _, apiMode := range []string{"responses", "responses_stateful"} {
		cfg := Config{ModelProviders: map[string]ModelProvider{"account": {Type: "deepseek"}}, Model: ModelConfig{Provider: "account", Model: "deepseek-flash", APIMode: apiMode}}
		if err := cfg.Validate(); err == nil {
			t.Fatal("validation accepted unsupported API mode")
		}
		if _, err := buildDeepSeekModel(ModelBuildContext{}, cfg.Model); err == nil {
			t.Fatal("builder accepted unsupported API mode")
		}
	}
}

func TestDeepSeekReasoningPreservesAssistantBoundaries(t *testing.T) {
	for _, deepSeek := range []bool{false, true} {
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var payload compatibleChatRequest
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if deepSeek {
				if len(payload.Messages) != 4 || payload.Messages[1].ReasoningContent == nil || *payload.Messages[1].ReasoningContent != "first thought" || payload.Messages[2].ReasoningContent == nil || *payload.Messages[2].ReasoningContent != "" {
					t.Fatalf("assistant reasoning/boundaries lost: %+v", payload.Messages)
				}
			} else {
				if len(payload.Messages) != 3 || payload.Thinking != nil {
					t.Fatalf("generic transport changed: %+v", payload)
				}
				for _, message := range payload.Messages {
					if message.ReasoningContent != nil {
						t.Fatal("DeepSeek reasoning leaked into another provider")
					}
				}
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}, nil
		})}
		m := &openAICompatibleModel{baseURL: "https://example.test", model: "test", reasoningEffort: "high", deepSeek: deepSeek, httpClient: client}
		_, err := m.GenerateContentFromMessageList(context.Background(), []agentmessages.Message{
			{Role: agentmessages.MessageRoleUser, Content: "hello"},
			{Role: agentmessages.MessageRoleAssistant, Content: "first", ReasoningContent: "first thought"},
			{Role: agentmessages.MessageRoleAssistant, Content: "second"},
			{Role: agentmessages.MessageRoleUser, Content: "continue"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeepSeekNamedProviderConfig(t *testing.T) {
	t.Setenv("AIDEN_TEST_DEEPSEEK_KEY", "test-key")
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(`[model_providers.account]
type = "DeepSeek"
api_key = "$AIDEN_TEST_DEEPSEEK_KEY"

[model]
provider = "account"
model = "deepseek-flash"
`), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeConfig.Model.Provider != "deepseek" || runtimeConfig.Model.ReasoningEffort != "none" {
		t.Fatalf("unexpected runtime config: %+v", runtimeConfig.Model)
	}
	m, err := NewModelManager(runtimeConfig.Model, ProxyConfig{}).build()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.(*openAICompatibleModel); got.token != "test-key" || got.baseURL != deepseekBaseURL || !got.deepSeek || got.reasoningEffort != "none" {
		t.Fatal("named provider did not resolve to DeepSeek request profile")
	}
	editorConfig, err := LoadResolvedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if editorConfig.Model.Provider != "account" || editorConfig.Model.ReasoningEffort != "" {
		t.Fatalf("runtime defaults leaked into editor: %+v", editorConfig.Model)
	}
}
