package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com/v1"
	anthropicAPIVersion     = "2023-06-01"
)

type anthropicModel struct {
	baseURL         string
	model           string
	token           string
	useBearerAuth   bool
	httpClient      *http.Client
	rawLogger       *llmRawHTTPLogger
	temperature     *float64
	reasoningEffort string
}

type anthropicModelOption func(*anthropicModel)

func withAnthropicRawHTTPLogger(logger *llmRawHTTPLogger) anthropicModelOption {
	return func(m *anthropicModel) { m.rawLogger = logger }
}

func withAnthropicTemperature(temperature *float64) anthropicModelOption {
	return func(m *anthropicModel) { m.temperature = temperature }
}

func withAnthropicBearerAuth() anthropicModelOption {
	return func(m *anthropicModel) { m.useBearerAuth = true }
}

func withAnthropicReasoningEffort(effort string) anthropicModelOption {
	return func(m *anthropicModel) { m.reasoningEffort = strings.TrimSpace(effort) }
}

type anthropicRequest struct {
	Model         string                    `json:"model"`
	System        []anthropicContentBlock   `json:"system,omitempty"`
	Messages      []anthropicRequestMessage `json:"messages"`
	MaxTokens     int                       `json:"max_tokens"`
	StopSequences []string                  `json:"stop_sequences,omitempty"`
	Temperature   *float64                  `json:"temperature,omitempty"`
	TopP          float64                   `json:"top_p,omitempty"`
	TopK          int                       `json:"top_k,omitempty"`
	Tools         []anthropicTool           `json:"tools,omitempty"`
	ToolChoice    any                       `json:"tool_choice,omitempty"`
	Thinking      *anthropicThinking        `json:"thinking,omitempty"`
	OutputConfig  *anthropicOutputConfig    `json:"output_config,omitempty"`
	Stream        bool                      `json:"stream,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicRequestMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	Signature    string                 `json:"signature,omitempty"`
	Source       *anthropicImageSource  `json:"source,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        any                    `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      any                    `json:"content,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model"`
	Role       string                  `json:"role"`
	StopReason string                  `json:"stop_reason"`
	Content    []anthropicContentBlock `json:"content"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u anthropicUsage) generationInfo() map[string]any {
	promptTokens := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	info := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": u.OutputTokens,
		"total_tokens":      promptTokens + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		info["cached_tokens"] = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		info["cache_creation_input_tokens"] = u.CacheCreationInputTokens
	}
	return info
}

func IsAnthropicModel(provider, model string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), "anthropic") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "anthropic/")
}

func newAnthropicModel(baseURL, model, token string, httpClient *http.Client, opts ...anthropicModelOption) llms.Model {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	result := &anthropicModel{
		baseURL:    normalizeAnthropicBaseURL(baseURL),
		model:      strings.TrimSpace(model),
		token:      strings.TrimSpace(token),
		httpClient: httpClient,
	}
	for _, option := range opts {
		if option != nil {
			option(result)
		}
	}
	return result
}

func normalizeAnthropicBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return defaultAnthropicBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err == nil && (parsed.Path == "" || parsed.Path == "/") {
		return baseURL + "/v1"
	}
	return baseURL
}

func (m *anthropicModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return llms.GenerateFromSinglePrompt(ctx, m, prompt, options...)
}

func (m *anthropicModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	callStarted := time.Now()
	callOpts := llms.CallOptions{}
	for _, option := range options {
		option(&callOpts)
	}

	system, converted, err := convertAnthropicMessages(messages)
	if err != nil {
		return nil, err
	}
	request := anthropicRequest{
		Model:         firstNonEmpty(callOpts.Model, m.model),
		System:        system,
		Messages:      mergeConsecutiveAnthropicMessages(converted),
		MaxTokens:     callOpts.MaxTokens,
		StopSequences: callOpts.StopWords,
		TopP:          callOpts.TopP,
		TopK:          callOpts.TopK,
		Tools:         convertAnthropicTools(callOpts.Tools, callOpts.Functions),
		ToolChoice:    convertAnthropicToolChoice(callOpts.ToolChoice, callOpts.FunctionCallBehavior),
		Stream:        callOpts.StreamingFunc != nil || callOpts.StreamingReasoningFunc != nil,
	}
	if request.MaxTokens <= 0 {
		request.MaxTokens = 2048
	}
	if m.temperature != nil {
		request.Temperature = m.temperature
	} else if callOpts.Temperature != 0 {
		request.Temperature = &callOpts.Temperature
	}
	if effort := normalizeAnthropicReasoningEffort(m.reasoningEffort); effort != "" && len(request.Tools) == 0 {
		request.Thinking = &anthropicThinking{Type: "adaptive"}
		request.OutputConfig = &anthropicOutputConfig{Effort: effort}
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal Anthropic request: %w", err)
	}
	ctx = m.withRawHTTPLogScope(ctx)
	_ = m.logRawHTTP(ctx, request.Model, "request", 0, string(payload))

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		_ = m.logRawHTTP(ctx, request.Model, "response", 0, "create request error: "+err.Error())
		return nil, fmt.Errorf("create Anthropic request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("anthropic-version", anthropicAPIVersion)
	if m.token != "" && m.useBearerAuth {
		httpRequest.Header.Set("Authorization", "Bearer "+m.token)
	} else if m.token != "" {
		httpRequest.Header.Set("x-api-key", m.token)
	}

	response, err := m.httpClient.Do(httpRequest)
	if err != nil {
		_ = m.logRawHTTP(ctx, request.Model, "response", 0, "transport error: "+err.Error())
		return nil, fmt.Errorf("send Anthropic request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, string(body))
		return nil, newProviderHTTPError(response.StatusCode, body)
	}

	if request.Stream {
		return m.decodeStreamingResponse(ctx, response.Body, request.Model, response.StatusCode, callStarted, &callOpts)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, "read response error: "+err.Error())
		return nil, fmt.Errorf("read Anthropic response: %w", err)
	}
	_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, string(body))
	var decoded anthropicResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode Anthropic response: %w", err)
	}
	return aggregateAnthropicResponse(decoded, callStarted), nil
}

func convertAnthropicMessages(messages []llms.MessageContent) ([]anthropicContentBlock, []anthropicRequestMessage, error) {
	var system []anthropicContentBlock
	converted := make([]anthropicRequestMessage, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case llms.ChatMessageTypeSystem:
			blocks, err := convertAnthropicSystemParts(message.Parts)
			if err != nil {
				return nil, nil, err
			}
			system = append(system, blocks...)
		case llms.ChatMessageTypeHuman, llms.ChatMessageTypeGeneric:
			blocks, err := convertAnthropicUserParts(message.Parts)
			if err != nil {
				return nil, nil, err
			}
			if len(blocks) > 0 {
				converted = append(converted, anthropicRequestMessage{Role: "user", Content: blocks})
			}
		case llms.ChatMessageTypeAI:
			blocks, err := convertAnthropicAssistantParts(message.Parts)
			if err != nil {
				return nil, nil, err
			}
			if len(blocks) > 0 {
				converted = append(converted, anthropicRequestMessage{Role: "assistant", Content: blocks})
			}
		case llms.ChatMessageTypeTool, llms.ChatMessageTypeFunction:
			blocks, err := convertAnthropicToolResultParts(message.Parts)
			if err != nil {
				return nil, nil, err
			}
			if len(blocks) > 0 {
				converted = append(converted, anthropicRequestMessage{Role: "user", Content: blocks})
			}
		default:
			return nil, nil, fmt.Errorf("unsupported Anthropic message role: %s", message.Role)
		}
	}
	return system, converted, nil
}

func convertAnthropicSystemParts(parts []llms.ContentPart) ([]anthropicContentBlock, error) {
	blocks := make([]anthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		block, err := anthropicTextBlock(part)
		if err != nil {
			return nil, fmt.Errorf("convert Anthropic system message: %w", err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func anthropicTextBlock(part llms.ContentPart) (anthropicContentBlock, error) {
	cacheControl := (*anthropicCacheControl)(nil)
	if cached, ok := part.(llms.CachedContent); ok {
		part = cached.ContentPart
		if cached.CacheControl != nil && strings.TrimSpace(cached.CacheControl.Type) != "" {
			cacheControl = &anthropicCacheControl{Type: cached.CacheControl.Type}
		}
	}
	text, ok := part.(llms.TextContent)
	if !ok {
		return anthropicContentBlock{}, fmt.Errorf("expected text content, got %T", part)
	}
	return anthropicContentBlock{Type: "text", Text: text.Text, CacheControl: cacheControl}, nil
}

func convertAnthropicUserParts(parts []llms.ContentPart) ([]anthropicContentBlock, error) {
	blocks := make([]anthropicContentBlock, 0, len(parts))
	for _, original := range parts {
		part := original
		cacheControl := (*anthropicCacheControl)(nil)
		if cached, ok := part.(llms.CachedContent); ok {
			part = cached.ContentPart
			if cached.CacheControl != nil && strings.TrimSpace(cached.CacheControl.Type) != "" {
				cacheControl = &anthropicCacheControl{Type: cached.CacheControl.Type}
			}
		}
		switch typed := part.(type) {
		case llms.TextContent:
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: typed.Text, CacheControl: cacheControl})
		case llms.BinaryContent:
			if !strings.HasPrefix(typed.MIMEType, "image/") {
				return nil, fmt.Errorf("unsupported Anthropic binary MIME type: %s", typed.MIMEType)
			}
			blocks = append(blocks, anthropicContentBlock{Type: "image", Source: &anthropicImageSource{
				Type: "base64", MediaType: typed.MIMEType, Data: base64.StdEncoding.EncodeToString(typed.Data),
			}, CacheControl: cacheControl})
		case llms.ImageURLContent:
			source, err := anthropicImageSourceFromURL(typed.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, anthropicContentBlock{Type: "image", Source: source, CacheControl: cacheControl})
		case llms.ToolCallResponse:
			blocks = append(blocks, anthropicContentBlock{Type: "tool_result", ToolUseID: typed.ToolCallID, Content: typed.Content})
		default:
			return nil, fmt.Errorf("unsupported Anthropic user content part: %T", part)
		}
	}
	return blocks, nil
}

func anthropicImageSourceFromURL(value string) (*anthropicImageSource, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data:") {
		header, data, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(header, ";base64") {
			return nil, fmt.Errorf("unsupported Anthropic image data URL")
		}
		mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		return &anthropicImageSource{Type: "base64", MediaType: mediaType, Data: data}, nil
	}
	if value == "" {
		return nil, fmt.Errorf("Anthropic image URL is empty")
	}
	return &anthropicImageSource{Type: "url", URL: value}, nil
}

func convertAnthropicAssistantParts(parts []llms.ContentPart) ([]anthropicContentBlock, error) {
	blocks := make([]anthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		switch typed := part.(type) {
		case llms.TextContent:
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: typed.Text})
		case llms.ToolCall:
			if typed.FunctionCall == nil {
				return nil, fmt.Errorf("Anthropic tool call %q has no function", typed.ID)
			}
			var input any = map[string]any{}
			arguments := strings.TrimSpace(typed.FunctionCall.Arguments)
			if arguments != "" {
				if err := json.Unmarshal([]byte(arguments), &input); err != nil {
					return nil, fmt.Errorf("decode Anthropic tool arguments for %q: %w", typed.FunctionCall.Name, err)
				}
			}
			blocks = append(blocks, anthropicContentBlock{
				Type: "tool_use", ID: typed.ID, Name: typed.FunctionCall.Name, Input: input,
			})
		default:
			return nil, fmt.Errorf("unsupported Anthropic assistant content part: %T", part)
		}
	}
	return blocks, nil
}

func convertAnthropicToolResultParts(parts []llms.ContentPart) ([]anthropicContentBlock, error) {
	blocks := make([]anthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		result, ok := part.(llms.ToolCallResponse)
		if !ok {
			return nil, fmt.Errorf("unsupported Anthropic tool result content part: %T", part)
		}
		blocks = append(blocks, anthropicContentBlock{
			Type: "tool_result", ToolUseID: result.ToolCallID, Content: result.Content,
		})
	}
	return blocks, nil
}

func mergeConsecutiveAnthropicMessages(messages []anthropicRequestMessage) []anthropicRequestMessage {
	if len(messages) < 2 {
		return messages
	}
	merged := make([]anthropicRequestMessage, 0, len(messages))
	for _, message := range messages {
		if len(merged) > 0 && merged[len(merged)-1].Role == message.Role {
			merged[len(merged)-1].Content = append(merged[len(merged)-1].Content, message.Content...)
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func convertAnthropicTools(tools []llms.Tool, functions []llms.FunctionDefinition) []anthropicTool {
	if len(tools) == 0 && len(functions) > 0 {
		tools = make([]llms.Tool, 0, len(functions))
		for i := range functions {
			function := functions[i]
			tools = append(tools, llms.Tool{Type: "function", Function: &function})
		}
	}
	converted := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Function == nil || strings.TrimSpace(tool.Function.Name) == "" {
			continue
		}
		inputSchema := tool.Function.Parameters
		if inputSchema == nil {
			inputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted = append(converted, anthropicTool{
			Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: inputSchema,
		})
	}
	return converted
}

func convertAnthropicToolChoice(choice any, behavior llms.FunctionCallBehavior) any {
	if choice == nil && behavior != "" {
		choice = string(behavior)
	}
	switch typed := choice.(type) {
	case nil:
		return nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required", "any":
			return map[string]any{"type": "any"}
		default:
			return nil
		}
	case llms.ToolChoice:
		if typed.Function != nil && strings.TrimSpace(typed.Function.Name) != "" {
			return map[string]any{"type": "tool", "name": typed.Function.Name}
		}
		return convertAnthropicToolChoice(typed.Type, "")
	case map[string]any:
		if function, ok := typed["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && strings.TrimSpace(name) != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		}
		if name, ok := typed["name"].(string); ok && strings.TrimSpace(name) != "" {
			return map[string]any{"type": "tool", "name": name}
		}
		if kind, ok := typed["type"].(string); ok {
			return convertAnthropicToolChoice(kind, "")
		}
	case map[string]string:
		if name := strings.TrimSpace(typed["name"]); name != "" {
			return map[string]any{"type": "tool", "name": name}
		}
		if kind := strings.TrimSpace(typed["type"]); kind != "" {
			return convertAnthropicToolChoice(kind, "")
		}
	}
	return nil
}

func aggregateAnthropicResponse(response anthropicResponse, callStarted time.Time) *llms.ContentResponse {
	var textParts []string
	var thinkingParts []string
	toolCalls := make([]llms.ToolCall, 0)
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			thinkingParts = append(thinkingParts, block.Thinking)
		case "tool_use":
			arguments, err := json.Marshal(block.Input)
			if err != nil {
				arguments = []byte("{}")
			}
			toolCalls = append(toolCalls, llms.ToolCall{
				ID: block.ID, Type: "function", FunctionCall: &llms.FunctionCall{Name: block.Name, Arguments: string(arguments)},
			})
		}
	}
	choice := &llms.ContentChoice{
		Content: strings.Join(textParts, ""), ReasoningContent: strings.Join(thinkingParts, ""),
		StopReason: response.StopReason, ToolCalls: toolCalls,
	}
	if len(toolCalls) > 0 {
		choice.FuncCall = toolCalls[0].FunctionCall
	}
	info := response.Usage.generationInfo()
	info["llm_output_chars"] = len(choice.Content)
	info["llm_tool_call_count"] = len(choice.ToolCalls)
	info["llm_finish_reason"] = response.StopReason
	info["llm_response_id"] = response.ID
	choice.GenerationInfo = finalizeLLMGenerationInfo(info, callStarted)
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{choice}}
}

type anthropicStreamBlock struct {
	Type      string
	ID        string
	Name      string
	Text      strings.Builder
	Thinking  strings.Builder
	InputJSON strings.Builder
}

func (m *anthropicModel) decodeStreamingResponse(ctx context.Context, body io.Reader, model string, statusCode int, callStarted time.Time, opts *llms.CallOptions) (*llms.ContentResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	blocks := map[int]*anthropicStreamBlock{}
	usage := anthropicUsage{}
	responseID := ""
	stopReason := ""
	var raw strings.Builder
	firstContent := false
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type         string                `json:"type"`
			Index        int                   `json:"index"`
			Message      anthropicResponse     `json:"message"`
			ContentBlock anthropicContentBlock `json:"content_block"`
			Delta        map[string]any        `json:"delta"`
			Usage        anthropicUsage        `json:"usage"`
			Error        map[string]any        `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			_ = m.logRawHTTP(ctx, model, "response", 0, raw.String())
			return nil, fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		switch event.Type {
		case "message_start":
			responseID = event.Message.ID
			usage = event.Message.Usage
		case "content_block_start":
			block := &anthropicStreamBlock{Type: event.ContentBlock.Type, ID: event.ContentBlock.ID, Name: event.ContentBlock.Name}
			if event.ContentBlock.Text != "" {
				block.Text.WriteString(event.ContentBlock.Text)
			}
			if event.ContentBlock.Thinking != "" {
				block.Thinking.WriteString(event.ContentBlock.Thinking)
			}
			blocks[event.Index] = block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return nil, fmt.Errorf("Anthropic stream delta references missing block %d", event.Index)
			}
			deltaType, _ := event.Delta["type"].(string)
			switch deltaType {
			case "text_delta":
				chunk, _ := event.Delta["text"].(string)
				block.Text.WriteString(chunk)
				if chunk != "" && opts.StreamingFunc != nil {
					if !firstContent {
						firstContent = true
					}
					if err := opts.StreamingFunc(ctx, []byte(chunk)); err != nil {
						return nil, err
					}
				}
			case "thinking_delta":
				chunk, _ := event.Delta["thinking"].(string)
				block.Thinking.WriteString(chunk)
				if chunk != "" && opts.StreamingReasoningFunc != nil {
					if err := opts.StreamingReasoningFunc(ctx, []byte(chunk), nil); err != nil {
						return nil, err
					}
				}
			case "input_json_delta":
				chunk, _ := event.Delta["partial_json"].(string)
				block.InputJSON.WriteString(chunk)
			}
		case "message_delta":
			if value, ok := event.Delta["stop_reason"].(string); ok {
				stopReason = value
			}
			if event.Usage.OutputTokens > 0 {
				usage.OutputTokens = event.Usage.OutputTokens
			}
		case "error":
			_ = m.logRawHTTP(ctx, model, "response", statusCode, raw.String())
			encoded, _ := json.Marshal(map[string]any{"error": event.Error})
			return nil, newProviderHTTPError(statusCode, encoded)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = m.logRawHTTP(ctx, model, "response", 0, raw.String())
		return nil, fmt.Errorf("read Anthropic stream: %w", err)
	}
	response := anthropicResponse{ID: responseID, Model: model, StopReason: stopReason, Usage: usage}
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		block := blocks[index]
		converted := anthropicContentBlock{Type: block.Type, ID: block.ID, Name: block.Name}
		switch block.Type {
		case "text":
			converted.Text = block.Text.String()
		case "thinking":
			converted.Thinking = block.Thinking.String()
		case "tool_use":
			input := any(map[string]any{})
			if rawInput := strings.TrimSpace(block.InputJSON.String()); rawInput != "" {
				if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
					return nil, fmt.Errorf("decode Anthropic streamed tool input: %w", err)
				}
			}
			converted.Input = input
		}
		response.Content = append(response.Content, converted)
	}
	if encoded, err := json.Marshal(response); err == nil {
		_ = m.logRawHTTP(ctx, model, "response", statusCode, string(encoded))
	} else {
		_ = m.logRawHTTP(ctx, model, "response", statusCode, raw.String())
	}
	result := aggregateAnthropicResponse(response, callStarted)
	if firstContent {
		result.Choices[0].GenerationInfo["llm_stream"] = true
	}
	return result, nil
}

func (m *anthropicModel) withRawHTTPLogScope(ctx context.Context) context.Context {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return ctx
	}
	if _, ok := rawHTTPLogFileTime(ctx); !ok {
		ctx = contextWithRawHTTPLogFileTime(ctx, m.rawLogger.currentTime())
	}
	if _, ok := rawHTTPLogFileSessionID(ctx); !ok {
		ctx = contextWithRawHTTPLogFileSessionID(ctx, m.rawLogger.currentSessionID())
	}
	return ctx
}

func (m *anthropicModel) logRawHTTP(ctx context.Context, model, kind string, statusCode int, raw string) error {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return nil
	}
	fileTime, _ := rawHTTPLogFileTime(ctx)
	sessionID, _ := rawHTTPLogFileSessionID(ctx)
	return m.rawLogger.LogWithFileScope(model, kind, statusCode, raw, fileTime, sessionID)
}

func buildAnthropicModelOptions(m *ModelManager, cfg ModelConfig) []anthropicModelOption {
	var options []anthropicModelOption
	if cfg.LogRawHTTP && strings.TrimSpace(m.rawHTTPLogDir) != "" {
		logger := newLLMRawHTTPLogger(m.rawHTTPLogDir, "")
		logger.SetSessionIDProvider(m.rawHTTPLogSessionID)
		logger.SetStorageMonitor(m.currentStorageMonitor())
		options = append(options, withAnthropicRawHTTPLogger(logger))
	}
	if cfg.Temperature != nil {
		options = append(options, withAnthropicTemperature(cfg.Temperature))
	}
	if cfg.ReasoningEffort != "" {
		options = append(options, withAnthropicReasoningEffort(cfg.ReasoningEffort))
	}
	return options
}

func normalizeAnthropicReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func resolveAnthropicBaseURL(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	if environment := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); environment != "" {
		return environment
	}
	return defaultAnthropicBaseURL
}

func resolveAnthropicToken(configured string) (string, bool) {
	if token := strings.TrimSpace(resolveProviderAPIKey(configured)); token != "" {
		environment, isReference := providerAPIKeyEnv(configured)
		return token, isReference && strings.EqualFold(environment, "ANTHROPIC_AUTH_TOKEN")
	}
	if token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")); token != "" {
		return token, true
	}
	if token := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); token != "" {
		return token, false
	}
	return "", false
}
