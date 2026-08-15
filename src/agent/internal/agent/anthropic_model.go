package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tmc/langchaingo/llms"
)

const (
	defaultAnthropicBaseURL          = "https://api.anthropic.com/v1"
	anthropicAPIVersion              = "2023-06-01"
	defaultAnthropicStreamMaxRetries = 5
	defaultAnthropicStreamRetryDelay = 2 * time.Second
	defaultAnthropicProtocolRetries  = 1
	anthropicDaemonRawSSELimit       = 128 * 1024
)

type anthropicModel struct {
	baseURL          string
	model            string
	token            string
	useBearerAuth    bool
	httpClient       *http.Client
	rawLogger        RawHTTPLogger
	temperature      *float64
	reasoningEffort  string
	streamMaxRetries int
	streamRetryDelay time.Duration
	protocolRetries  int
	protocolDelay    time.Duration
}

type anthropicModelOption func(*anthropicModel)

func withAnthropicRawHTTPLogger(logger RawHTTPLogger) anthropicModelOption {
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

func withAnthropicStreamRetry(maxRetries int, delay time.Duration) anthropicModelOption {
	return func(m *anthropicModel) {
		if maxRetries >= 0 {
			m.streamMaxRetries = maxRetries
		}
		if delay >= 0 {
			m.streamRetryDelay = delay
		}
	}
}

func withAnthropicProtocolRetry(maxRetries int, delay time.Duration) anthropicModelOption {
	return func(m *anthropicModel) {
		if maxRetries >= 0 {
			m.protocolRetries = maxRetries
		}
		if delay >= 0 {
			m.protocolDelay = delay
		}
	}
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
		baseURL:          normalizeAnthropicBaseURL(baseURL),
		model:            strings.TrimSpace(model),
		token:            strings.TrimSpace(token),
		httpClient:       httpClient,
		streamMaxRetries: defaultAnthropicStreamMaxRetries,
		streamRetryDelay: defaultAnthropicStreamRetryDelay,
		protocolRetries:  defaultAnthropicProtocolRetries,
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
	if len(request.Tools) > 0 {
		request.ToolChoice = disableAnthropicParallelToolUse(request.ToolChoice)
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
	streamRetries := 0
	protocolRetries := 0
	for {
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
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, string(body))
			return nil, newProviderHTTPError(response.StatusCode, body)
		}

		if request.Stream {
			result, streamErr := m.decodeStreamingResponse(ctx, response.Body, response.Header, request.Model, response.StatusCode, callStarted, &callOpts, request.Thinking != nil)
			response.Body.Close()
			if streamErr == nil {
				return result, nil
			}
			retryLimit := m.streamMaxRetries
			retryDelay := m.streamRetryDelay
			var responseProtocolErr *anthropicResponseProtocolError
			if errors.As(streamErr, &responseProtocolErr) {
				retryLimit = m.protocolRetries
				retryDelay = m.protocolDelay
				if protocolRetries >= retryLimit || !shouldRetryAnthropicStreamError(streamErr) {
					return nil, streamErr
				}
				protocolRetries++
				log.Printf("[WARN] [anthropic] retrying semantic protocol error (%d/%d): %v", protocolRetries, retryLimit, streamErr)
				if err := waitBeforeAnthropicRetry(ctx, protocolRetries, retryDelay); err != nil {
					return nil, err
				}
				continue
			}
			if streamRetries >= retryLimit || !shouldRetryAnthropicStreamError(streamErr) {
				return nil, streamErr
			}
			streamRetries++
			if err := waitBeforeAnthropicRetry(ctx, streamRetries, retryDelay); err != nil {
				return nil, err
			}
			continue
		}

		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, "read response error: "+err.Error())
			return nil, fmt.Errorf("read Anthropic response: %w", err)
		}
		_ = m.logRawHTTP(ctx, request.Model, "response", response.StatusCode, string(body))
		var decoded anthropicResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode Anthropic response: %w", err)
		}
		normalized, recovery, protocolErr := normalizeAnthropicResponse(decoded, request.Thinking != nil)
		if protocolErr != nil {
			if protocolRetries >= m.protocolRetries {
				return nil, protocolErr
			}
			protocolRetries++
			log.Printf("[WARN] [anthropic] retrying semantic protocol error (%d/%d): %v", protocolRetries, m.protocolRetries, protocolErr)
			if err := waitBeforeAnthropicRetry(ctx, protocolRetries, m.protocolDelay); err != nil {
				return nil, err
			}
			continue
		}
		generationInfo := map[string]any{}
		if recovery != "" {
			log.Printf("[WARN] [anthropic] recovered response %s as user-visible text (response_id=%s)", recovery, decoded.ID)
			generationInfo["llm_anthropic_response_recovery"] = recovery
		}
		return aggregateAnthropicResponseWithGenerationInfo(normalized, callStarted, generationInfo), nil
	}
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

func disableAnthropicParallelToolUse(choice any) map[string]any {
	converted, _ := choice.(map[string]any)
	if converted == nil {
		converted = map[string]any{"type": "auto"}
	} else {
		cloned := make(map[string]any, len(converted)+1)
		for key, value := range converted {
			cloned[key] = value
		}
		converted = cloned
	}
	converted["disable_parallel_tool_use"] = true
	return converted
}

type anthropicResponseProtocolError struct {
	message string
}

func (e *anthropicResponseProtocolError) Error() string {
	return "Anthropic response protocol error: " + e.message
}

func newAnthropicResponseProtocolError(message string) error {
	return &anthropicResponseProtocolError{message: message}
}

func normalizeAnthropicResponse(response anthropicResponse, thinkingEnabled bool) (anthropicResponse, string, error) {
	hasText := false
	hasThinking := false
	hasToolUse := false
	hasSignedThinking := false
	invalidToolUse := false
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			hasText = hasText || strings.TrimSpace(block.Text) != ""
		case "thinking":
			hasThinking = hasThinking || strings.TrimSpace(block.Thinking) != ""
			hasSignedThinking = hasSignedThinking || strings.TrimSpace(block.Signature) != ""
		case "tool_use":
			valid := strings.TrimSpace(block.ID) != "" && strings.TrimSpace(block.Name) != ""
			hasToolUse = hasToolUse || valid
			invalidToolUse = invalidToolUse || !valid
		}
	}
	if invalidToolUse {
		return response, "", newAnthropicResponseProtocolError("tool_use content is missing an id or name")
	}

	switch response.StopReason {
	case "tool_use":
		if !hasToolUse {
			return response, "", newAnthropicResponseProtocolError("tool_use stop_reason has no valid tool_use content")
		}
	case "end_turn":
		if hasText || hasToolUse {
			return response, "", nil
		}
		if hasThinking && !hasSignedThinking && !thinkingEnabled {
			for index := range response.Content {
				block := &response.Content[index]
				if block.Type != "thinking" {
					continue
				}
				block.Type = "text"
				block.Text = block.Thinking
				block.Thinking = ""
				block.Signature = ""
			}
			return response, "thinking_as_text", nil
		}
		return response, "", newAnthropicResponseProtocolError("end_turn response has no text or tool_use content")
	}

	return response, "", nil
}

func aggregateAnthropicResponseWithGenerationInfo(response anthropicResponse, callStarted time.Time, generationInfo map[string]any) *llms.ContentResponse {
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
	for key, value := range generationInfo {
		info[key] = value
	}
	choice.GenerationInfo = finalizeLLMGenerationInfo(info, callStarted)
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{choice}}
}

type anthropicStreamBlock struct {
	Type      string
	ID        string
	Name      string
	Text      strings.Builder
	Thinking  strings.Builder
	Signature strings.Builder
	InputJSON strings.Builder
	Stopped   bool
}

type anthropicStreamError struct {
	err             error
	outputDelivered bool
	retryable       bool
	protocol        bool
}

func (e *anthropicStreamError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *anthropicStreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newAnthropicStreamProtocolError(outputDelivered bool, format string, args ...any) error {
	return &anthropicStreamError{
		err:             fmt.Errorf("Anthropic stream protocol error: "+format, args...),
		outputDelivered: outputDelivered,
		retryable:       true,
		protocol:        true,
	}
}

type anthropicStreamBlockDiagnostic struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
}

type anthropicStreamFailureDiagnostic struct {
	StreamError       string                           `json:"stream_error"`
	RawSSE            string                           `json:"raw_sse"`
	RawSSETotalBytes  int                              `json:"raw_sse_total_bytes"`
	RawSSETruncated   bool                             `json:"raw_sse_truncated"`
	RawSSEBase64      string                           `json:"raw_sse_base64,omitempty"`
	ResponseID        string                           `json:"response_id"`
	UpstreamRequestID string                           `json:"upstream_request_id"`
	ResponseHeaders   map[string]string                `json:"response_headers"`
	EventTypeCounts   map[string]int                   `json:"event_type_counts"`
	ContentBlocks     []anthropicStreamBlockDiagnostic `json:"content_blocks"`
}

func (d *anthropicStreamFailureDiagnostic) setRawSSE(rawSSE []byte, rawSSELimit int) {
	rawSample := rawSSE
	truncated := false
	if rawSSELimit >= 0 && len(rawSample) > rawSSELimit {
		rawSample = rawSample[:rawSSELimit]
		truncated = true
	}
	rawSSEValidUTF8 := utf8.Valid(rawSSE)
	if rawSSEValidUTF8 {
		for len(rawSample) > 0 && !utf8.Valid(rawSample) {
			rawSample = rawSample[:len(rawSample)-1]
		}
	}

	d.RawSSE = string(rawSample)
	d.RawSSETotalBytes = len(rawSSE)
	d.RawSSETruncated = truncated
	d.RawSSEBase64 = ""
	if !rawSSEValidUTF8 {
		d.RawSSEBase64 = base64.StdEncoding.EncodeToString(rawSample)
	}
}

func normalizeAnthropicResponseHeaders(headers http.Header) map[string]string {
	normalized := make(map[string]string, len(headers))
	for key, values := range headers {
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.Join(values, ", ")
	}
	return normalized
}

func anthropicUpstreamRequestID(headers map[string]string) string {
	for _, key := range []string{
		"request-id",
		"x-request-id",
		"anthropic-request-id",
		"x-amzn-requestid",
		"x-amz-request-id",
		"x-goog-request-id",
		"x-trace-id",
		"trace-id",
	} {
		if value := strings.TrimSpace(headers[key]); value != "" {
			return value
		}
	}
	return ""
}

func anthropicStreamBlockDiagnostics(blocks map[int]*anthropicStreamBlock) []anthropicStreamBlockDiagnostic {
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	diagnostics := make([]anthropicStreamBlockDiagnostic, 0, len(indexes))
	for _, index := range indexes {
		diagnostics = append(diagnostics, anthropicStreamBlockDiagnostic{
			Index: index,
			Type:  blocks[index].Type,
		})
	}
	return diagnostics
}

func isAnthropicStreamProtocolFailure(err error) bool {
	var streamErr *anthropicStreamError
	if errors.As(err, &streamErr) && streamErr.protocol {
		return true
	}
	var responseProtocolErr *anthropicResponseProtocolError
	return errors.As(err, &responseProtocolErr)
}

func firstOpenAnthropicStreamBlock(blocks map[int]*anthropicStreamBlock) (int, bool) {
	openIndex := 0
	found := false
	for index, block := range blocks {
		if block.Stopped || found && index >= openIndex {
			continue
		}
		openIndex = index
		found = true
	}
	return openIndex, found
}

func (m *anthropicModel) decodeStreamingResponse(ctx context.Context, body io.Reader, responseHeaders http.Header, model string, statusCode int, callStarted time.Time, opts *llms.CallOptions, thinkingEnabled bool) (result *llms.ContentResponse, resultErr error) {
	var raw bytes.Buffer
	scanner := bufio.NewScanner(io.TeeReader(body, &raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	blocks := map[int]*anthropicStreamBlock{}
	eventTypeCounts := map[string]int{}
	normalizedResponseHeaders := normalizeAnthropicResponseHeaders(responseHeaders)
	usage := anthropicUsage{}
	responseID := ""
	stopReason := ""
	firstContent := false
	var firstContentAt int64
	outputDelivered := false
	messageStarted := false
	messageDeltaSeen := false
	messageStopped := false
	defer func() {
		if resultErr == nil {
			return
		}
		logStatusCode := statusCode
		var statusErr interface{ HTTPStatusCode() int }
		if errors.As(resultErr, &statusErr) && statusErr.HTTPStatusCode() != 0 {
			logStatusCode = statusErr.HTTPStatusCode()
		}
		failureDiagnostic := anthropicStreamFailureDiagnostic{
			StreamError:       resultErr.Error(),
			ResponseID:        responseID,
			UpstreamRequestID: anthropicUpstreamRequestID(normalizedResponseHeaders),
			ResponseHeaders:   normalizedResponseHeaders,
			EventTypeCounts:   eventTypeCounts,
			ContentBlocks:     anthropicStreamBlockDiagnostics(blocks),
		}
		failureDiagnostic.setRawSSE(raw.Bytes(), -1)
		failureBody, _ := json.Marshal(failureDiagnostic)
		_ = m.logRawHTTP(ctx, model, "response", logStatusCode, string(failureBody))
		if isAnthropicStreamProtocolFailure(resultErr) {
			daemonDiagnostic := failureDiagnostic
			daemonDiagnostic.setRawSSE(raw.Bytes(), anthropicDaemonRawSSELimit)
			daemonBody, _ := json.Marshal(daemonDiagnostic)
			log.Printf("[ERROR] [anthropic] stream protocol failure %s", daemonBody)
		}
	}()
	for scanner.Scan() {
		line := scanner.Text()
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
			return nil, newAnthropicStreamProtocolError(outputDelivered, "decode event: %v", err)
		}
		eventTypeCounts[event.Type]++
		if messageStopped {
			return nil, newAnthropicStreamProtocolError(outputDelivered, "event %q received after message_stop", event.Type)
		}
		switch event.Type {
		case "message_start":
			if messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "duplicate message_start")
			}
			messageStarted = true
			responseID = event.Message.ID
			usage = event.Message.Usage
		case "content_block_start":
			if !messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_start received before message_start")
			}
			if messageDeltaSeen {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_start received after message_delta")
			}
			if _, exists := blocks[event.Index]; exists {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "duplicate content_block_start for block %d", event.Index)
			}
			block := &anthropicStreamBlock{Type: event.ContentBlock.Type, ID: event.ContentBlock.ID, Name: event.ContentBlock.Name}
			if event.ContentBlock.Text != "" {
				block.Text.WriteString(event.ContentBlock.Text)
			}
			if event.ContentBlock.Thinking != "" {
				block.Thinking.WriteString(event.ContentBlock.Thinking)
			}
			if event.ContentBlock.Signature != "" {
				block.Signature.WriteString(event.ContentBlock.Signature)
			}
			blocks[event.Index] = block
		case "content_block_delta":
			if !messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_delta received before message_start")
			}
			if messageDeltaSeen {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_delta received after message_delta")
			}
			block := blocks[event.Index]
			if block == nil {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_delta references missing block %d", event.Index)
			}
			if block.Stopped {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_delta received after content_block_stop for block %d", event.Index)
			}
			deltaType, _ := event.Delta["type"].(string)
			switch deltaType {
			case "text_delta":
				chunk, _ := event.Delta["text"].(string)
				block.Text.WriteString(chunk)
				if chunk != "" && !firstContent {
					firstContent = true
					firstContentAt = time.Since(callStarted).Milliseconds()
				}
				if chunk != "" && opts.StreamingFunc != nil {
					if err := opts.StreamingFunc(ctx, []byte(chunk)); err != nil {
						return nil, err
					}
					outputDelivered = true
				}
			case "thinking_delta":
				chunk, _ := event.Delta["thinking"].(string)
				block.Thinking.WriteString(chunk)
				if chunk != "" && thinkingEnabled && opts.StreamingReasoningFunc != nil {
					if err := opts.StreamingReasoningFunc(ctx, []byte(chunk), nil); err != nil {
						return nil, err
					}
					outputDelivered = true
				}
			case "input_json_delta":
				chunk, _ := event.Delta["partial_json"].(string)
				block.InputJSON.WriteString(chunk)
			case "signature_delta":
				chunk, _ := event.Delta["signature"].(string)
				block.Signature.WriteString(chunk)
			case "citations_delta":
				// Citations annotate an existing text block and do not change its text content.
			default:
				return nil, newAnthropicStreamProtocolError(outputDelivered, "unknown content_block_delta type %q for block %d", deltaType, event.Index)
			}
		case "content_block_stop":
			if !messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_stop received before message_start")
			}
			if messageDeltaSeen {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_stop received after message_delta")
			}
			block := blocks[event.Index]
			if block == nil {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "content_block_stop references missing block %d", event.Index)
			}
			if block.Stopped {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "duplicate content_block_stop for block %d", event.Index)
			}
			block.Stopped = true
		case "message_delta":
			if !messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "message_delta received before message_start")
			}
			if index, found := firstOpenAnthropicStreamBlock(blocks); found {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "message_delta received with open content block %d", index)
			}
			if value, ok := event.Delta["stop_reason"].(string); ok {
				stopReason = value
			}
			if event.Usage.OutputTokens > 0 {
				usage.OutputTokens = event.Usage.OutputTokens
			}
			messageDeltaSeen = true
		case "message_stop":
			if !messageStarted {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "message_stop received before message_start")
			}
			if index, found := firstOpenAnthropicStreamBlock(blocks); found {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "message_stop received with open content block %d", index)
			}
			if !messageDeltaSeen {
				return nil, newAnthropicStreamProtocolError(outputDelivered, "message_stop received before message_delta")
			}
			messageStopped = true
		case "ping":
		case "error":
			encoded, _ := json.Marshal(map[string]any{"error": event.Error})
			providerErr := newProviderHTTPError(anthropicErrorHTTPStatus(event.Error), encoded)
			return nil, &anthropicStreamError{err: providerErr, outputDelivered: outputDelivered}
		default:
			return nil, newAnthropicStreamProtocolError(outputDelivered, "unknown event type %q", event.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &anthropicStreamError{
			err:             fmt.Errorf("read Anthropic stream: %w", err),
			outputDelivered: outputDelivered,
			retryable:       true,
		}
	}
	if !messageStarted {
		return nil, newAnthropicStreamProtocolError(outputDelivered, "ended before message_start")
	}
	if !messageStopped {
		return nil, newAnthropicStreamProtocolError(outputDelivered, "ended before message_stop")
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
			converted.Signature = block.Signature.String()
		case "tool_use":
			input := any(map[string]any{})
			if rawInput := strings.TrimSpace(block.InputJSON.String()); rawInput != "" {
				if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
					return nil, newAnthropicStreamProtocolError(outputDelivered, "decode tool input for block %d: %v", index, err)
				}
			}
			converted.Input = input
		}
		response.Content = append(response.Content, converted)
	}
	encodedResponse, encodeErr := json.Marshal(response)
	normalized, recovery, protocolErr := normalizeAnthropicResponse(response, thinkingEnabled)
	if protocolErr != nil {
		return nil, &anthropicStreamError{err: protocolErr, outputDelivered: outputDelivered, retryable: true, protocol: true}
	}
	response = normalized
	if recovery != "" {
		log.Printf("[WARN] [anthropic] recovered streamed response %s as user-visible text (response_id=%s)", recovery, response.ID)
	}
	if recovery != "" && opts.StreamingFunc != nil {
		recoveredText := anthropicResponseText(response)
		if recoveredText != "" {
			if err := opts.StreamingFunc(ctx, []byte(recoveredText)); err != nil {
				return nil, err
			}
			outputDelivered = true
			if !firstContent {
				firstContent = true
				firstContentAt = time.Since(callStarted).Milliseconds()
			}
		}
	}
	if encodeErr == nil {
		_ = m.logRawHTTP(ctx, model, "response", statusCode, string(encodedResponse))
	} else {
		_ = m.logRawHTTP(ctx, model, "response", statusCode, raw.String())
	}
	generationInfo := map[string]any{}
	if firstContent {
		generationInfo["llm_stream"] = true
		generationInfo["llm_time_to_first_content_ms"] = firstContentAt
	}
	if recovery != "" {
		generationInfo["llm_anthropic_response_recovery"] = recovery
	}
	result = aggregateAnthropicResponseWithGenerationInfo(response, callStarted, generationInfo)
	return result, nil
}

func anthropicResponseText(response anthropicResponse) string {
	var result strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}

func anthropicErrorHTTPStatus(eventError map[string]any) int {
	errorType, _ := eventError["type"].(string)
	switch strings.ToLower(strings.TrimSpace(errorType)) {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return 529
	case "api_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func shouldRetryAnthropicStreamError(err error) bool {
	var streamErr *anthropicStreamError
	if !errors.As(err, &streamErr) || streamErr.outputDelivered {
		return false
	}
	if streamErr.retryable {
		return true
	}
	var statusErr interface{ HTTPStatusCode() int }
	return errors.As(err, &statusErr) && shouldRetryHTTPStatus(statusErr.HTTPStatusCode())
}

func waitBeforeAnthropicRetry(ctx context.Context, retryNumber int, retryDelay time.Duration) error {
	delay := time.Duration(retryNumber) * retryDelay
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *anthropicModel) withRawHTTPLogScope(ctx context.Context) context.Context {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return ctx
	}
	return m.rawLogger.BeginScope(ctx)
}

func (m *anthropicModel) logRawHTTP(ctx context.Context, model, kind string, statusCode int, raw string) error {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return nil
	}
	return m.rawLogger.Log(ctx, RawHTTPLogEntry{
		Model:      model,
		Kind:       kind,
		StatusCode: statusCode,
		Raw:        raw,
	})
}

func buildAnthropicModelOptions(ctx ModelBuildContext, cfg ModelConfig) []anthropicModelOption {
	var options []anthropicModelOption
	if ctx.RawHTTPLogger != nil {
		options = append(options, withAnthropicRawHTTPLogger(ctx.RawHTTPLogger))
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
