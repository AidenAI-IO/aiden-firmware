package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

type openAICompatibleModel struct {
	baseURL    string
	model      string
	token      string
	httpClient *http.Client
}

type compatibleChatRequest struct {
	Model            string              `json:"model"`
	Messages         []compatibleMessage `json:"messages"`
	Temperature      *float64            `json:"temperature,omitempty"`
	MaxTokens        int                 `json:"max_tokens,omitempty"`
	TopP             float64             `json:"top_p,omitempty"`
	Stop             []string            `json:"stop,omitempty"`
	FrequencyPenalty float64             `json:"frequency_penalty,omitempty"`
	PresencePenalty  float64             `json:"presence_penalty,omitempty"`
	Seed             int                 `json:"seed,omitempty"`
	Tools            []compatibleTool    `json:"tools,omitempty"`
	ToolChoice       any                 `json:"tool_choice,omitempty"`
	Stream           bool                `json:"stream,omitempty"`
	ResponseFormat   map[string]string   `json:"response_format,omitempty"`
}

type compatibleMessage struct {
	Role       string               `json:"role"`
	Content    any                  `json:"content,omitempty"`
	Name       string               `json:"name,omitempty"`
	ToolCalls  []compatibleToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type compatibleTool struct {
	Type     string                        `json:"type"`
	Function *compatibleFunctionDefinition `json:"function,omitempty"`
}

type compatibleFunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      bool   `json:"strict,omitempty"`
}

type compatibleToolCall struct {
	ID       string                  `json:"id,omitempty"`
	Type     string                  `json:"type"`
	Function *compatibleFunctionCall `json:"function,omitempty"`
}

type compatibleFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type compatibleContentPart struct {
	Type       string                `json:"type"`
	Text       string                `json:"text,omitempty"`
	ImageURL   *compatibleImageURL   `json:"image_url,omitempty"`
	InputAudio *compatibleInputAudio `json:"input_audio,omitempty"`
}

type compatibleImageURL struct {
	URL string `json:"url"`
}

type compatibleInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type compatibleChatResponse struct {
	Choices []struct {
		Message struct {
			Content   any                  `json:"content"`
			ToolCalls []compatibleToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

func newOpenAICompatibleModel(baseURL, model, token string, httpClient *http.Client) llms.Model {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &openAICompatibleModel{
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		token:      token,
		httpClient: httpClient,
	}
}

func (m *openAICompatibleModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return llms.GenerateFromSinglePrompt(ctx, m, prompt, options...)
}

func (m *openAICompatibleModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	callOpts := llms.CallOptions{}
	for _, option := range options {
		option(&callOpts)
	}

	requestMessages := make([]compatibleMessage, 0, len(messages))
	for _, message := range messages {
		converted, err := convertMessageContent(message)
		if err != nil {
			return nil, err
		}
		requestMessages = append(requestMessages, converted)
	}
	requestMessages = normalizeCompatibleMessages(requestMessages)

	reqPayload := compatibleChatRequest{
		Model:            firstNonEmpty(callOpts.Model, m.model),
		Messages:         requestMessages,
		MaxTokens:        callOpts.MaxTokens,
		TopP:             callOpts.TopP,
		Stop:             callOpts.StopWords,
		FrequencyPenalty: callOpts.FrequencyPenalty,
		PresencePenalty:  callOpts.PresencePenalty,
		Seed:             callOpts.Seed,
		Tools:            convertTools(callOpts.Tools, callOpts.Functions),
		ToolChoice:       normalizeToolChoice(callOpts.ToolChoice, callOpts.FunctionCallBehavior),
	}
	if callOpts.Temperature != 0 {
		reqPayload.Temperature = &callOpts.Temperature
	}
	if callOpts.JSONMode {
		reqPayload.ResponseFormat = map[string]string{"type": "json_object"}
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}

	endpoint := m.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var decoded compatibleChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("empty response choices")
	}

	choice := decoded.Choices[0]
	content := flattenResponseContent(choice.Message.Content)
	result := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content:    content,
			StopReason: choice.FinishReason,
			ToolCalls:  convertResponseToolCalls(choice.Message.ToolCalls),
		}},
	}
	if decoded.Usage != nil {
		result.Choices[0].GenerationInfo = map[string]any{
			"prompt_tokens":     decoded.Usage.PromptTokens,
			"completion_tokens": decoded.Usage.CompletionTokens,
			"total_tokens":      decoded.Usage.TotalTokens,
		}
	}
	if len(result.Choices[0].ToolCalls) > 0 {
		result.Choices[0].FuncCall = result.Choices[0].ToolCalls[0].FunctionCall
	}

	if callOpts.StreamingFunc != nil && content != "" {
		if err := callOpts.StreamingFunc(ctx, []byte(content)); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func convertMessageContent(message llms.MessageContent) (compatibleMessage, error) {
	msg := compatibleMessage{Role: compatibleRole(message.Role)}

	switch message.Role {
	case llms.ChatMessageTypeFunction:
		if len(message.Parts) == 1 {
			if part, ok := message.Parts[0].(llms.ToolCallResponse); ok {
				msg.Name = part.Name
				msg.Content = part.Content
				return msg, nil
			}
		}
		return compatibleMessage{}, fmt.Errorf("function message requires one tool response part")

	case llms.ChatMessageTypeTool:
		if len(message.Parts) == 1 {
			if part, ok := message.Parts[0].(llms.ToolCallResponse); ok {
				msg.ToolCallID = part.ToolCallID
				msg.Content = part.Content
				return msg, nil
			}
		}
		return compatibleMessage{}, fmt.Errorf("tool message requires one tool response part")
	}

	textParts := make([]compatibleContentPart, 0, len(message.Parts))
	toolCalls := make([]compatibleToolCall, 0)
	for _, part := range message.Parts {
		switch typed := part.(type) {
		case llms.TextContent:
			textParts = append(textParts, compatibleContentPart{
				Type: "text",
				Text: typed.Text,
			})
		case llms.ImageURLContent:
			textParts = append(textParts, compatibleContentPart{
				Type: "image_url",
				ImageURL: &compatibleImageURL{
					URL: typed.URL,
				},
			})
		case llms.BinaryContent:
			switch {
			case strings.HasPrefix(typed.MIMEType, "audio/"):
				textParts = append(textParts, compatibleContentPart{
					Type: "input_audio",
					InputAudio: &compatibleInputAudio{
						Data:   base64.StdEncoding.EncodeToString(typed.Data),
						Format: audioFormatFromMIME(typed.MIMEType),
					},
				})
			case strings.HasPrefix(typed.MIMEType, "image/"):
				textParts = append(textParts, compatibleContentPart{
					Type: "image_url",
					ImageURL: &compatibleImageURL{
						URL: "data:" + typed.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(typed.Data),
					},
				})
			default:
				return compatibleMessage{}, fmt.Errorf("unsupported binary MIME type: %s", typed.MIMEType)
			}
		case llms.ToolCall:
			toolCalls = append(toolCalls, compatibleToolCall{
				ID:   typed.ID,
				Type: typed.Type,
				Function: &compatibleFunctionCall{
					Name:      typed.FunctionCall.Name,
					Arguments: typed.FunctionCall.Arguments,
				},
			})
		default:
			return compatibleMessage{}, fmt.Errorf("unsupported content part type: %T", part)
		}
	}

	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	if len(textParts) == 1 && textParts[0].Type == "text" {
		msg.Content = textParts[0].Text
		return msg, nil
	}
	if len(textParts) > 0 {
		msg.Content = textParts
	}
	return msg, nil
}

func convertTools(tools []llms.Tool, functions []llms.FunctionDefinition) []compatibleTool {
	if len(tools) == 0 && len(functions) > 0 {
		tools = make([]llms.Tool, 0, len(functions))
		for _, function := range functions {
			fn := function
			tools = append(tools, llms.Tool{
				Type:     "function",
				Function: &fn,
			})
		}
	}

	converted := make([]compatibleTool, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, compatibleTool{
			Type: tool.Type,
			Function: &compatibleFunctionDefinition{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
				Strict:      tool.Function.Strict,
			},
		})
	}
	return converted
}

func normalizeToolChoice(toolChoice any, functionBehavior llms.FunctionCallBehavior) any {
	if toolChoice != nil {
		return toolChoice
	}
	if functionBehavior != "" {
		return functionBehavior
	}
	return nil
}

func compatibleRole(role llms.ChatMessageType) string {
	switch role {
	case llms.ChatMessageTypeSystem:
		return "system"
	case llms.ChatMessageTypeAI:
		return "assistant"
	case llms.ChatMessageTypeHuman, llms.ChatMessageTypeGeneric:
		return "user"
	case llms.ChatMessageTypeFunction:
		return "function"
	case llms.ChatMessageTypeTool:
		return "tool"
	default:
		return "user"
	}
}

func convertResponseToolCalls(toolCalls []compatibleToolCall) []llms.ToolCall {
	converted := make([]llms.ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		converted = append(converted, llms.ToolCall{
			ID:   toolCall.ID,
			Type: toolCall.Type,
			FunctionCall: &llms.FunctionCall{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			},
		})
	}
	return converted
}

func flattenResponseContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			partMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := partMap["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func normalizeCompatibleMessages(messages []compatibleMessage) []compatibleMessage {
	if len(messages) == 0 {
		return messages
	}

	systemSegments := make([]string, 0, 2)
	normalized := make([]compatibleMessage, 0, len(messages))

	for _, message := range messages {
		if message.Role != "system" {
			normalized = append(normalized, message)
			continue
		}

		switch content := message.Content.(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				systemSegments = append(systemSegments, content)
			}
		case []compatibleContentPart:
			for _, part := range content {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					systemSegments = append(systemSegments, part.Text)
				}
			}
		}
	}

	if len(systemSegments) == 0 {
		return normalized
	}

	mergedSystem := compatibleMessage{
		Role:    "system",
		Content: strings.Join(systemSegments, "\n\n"),
	}
	return append([]compatibleMessage{mergedSystem}, normalized...)
}

func audioFormatFromMIME(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch mimeType {
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/webm":
		return "webm"
	case "audio/ogg":
		return "ogg"
	default:
		if idx := strings.IndexByte(mimeType, '/'); idx >= 0 && idx < len(mimeType)-1 {
			return strings.TrimPrefix(mimeType[idx+1:], "x-")
		}
		return "wav"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
