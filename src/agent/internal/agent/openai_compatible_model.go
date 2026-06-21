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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
)

type openAICompatibleModel struct {
	baseURL    string
	model      string
	token      string
	httpClient *http.Client
	rawLogger  *llmRawHTTPLogger
}

type openAICompatibleModelOption func(*openAICompatibleModel)

// rawHTTPLogContextKey gates raw HTTP logging to the calls that opt in via
// contextWithRawHTTPLog. The main conversation loop sets it; background tasks
// (summarization, profile rebuilds, skill merge) share the same model instance
// and logger, so without this gate their requests would interleave with the
// main loop in the shared log file and break request/response pairing.
type rawHTTPLogContextKey struct{}

type rawHTTPLogFileTimeContextKey struct{}

// contextWithRawHTTPLog marks ctx so model calls made under it are written to
// the raw HTTP log. Only the main conversation loop should set this.
func contextWithRawHTTPLog(ctx context.Context) context.Context {
	return context.WithValue(ctx, rawHTTPLogContextKey{}, true)
}

func rawHTTPLogEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(rawHTTPLogContextKey{}).(bool)
	return enabled
}

func contextWithRawHTTPLogFileTime(ctx context.Context, fileTime time.Time) context.Context {
	return context.WithValue(ctx, rawHTTPLogFileTimeContextKey{}, fileTime)
}

func rawHTTPLogFileTime(ctx context.Context) (time.Time, bool) {
	fileTime, ok := ctx.Value(rawHTTPLogFileTimeContextKey{}).(time.Time)
	return fileTime, ok && !fileTime.IsZero()
}

func withOpenAICompatibleRawHTTPLogger(logger *llmRawHTTPLogger) openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.rawLogger = logger
	}
}

type llmRawHTTPLogger struct {
	dir       string
	runtimeID string
	now       func() time.Time
	mu        sync.Mutex
}

func newLLMRawHTTPLogger(logDir, runtimeID string) *llmRawHTTPLogger {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		return nil
	}
	return &llmRawHTTPLogger{
		dir:       logDir,
		runtimeID: runtimeID,
		now:       time.Now,
	}
}

func (l *llmRawHTTPLogger) Log(model, kind string, statusCode int, raw string) error {
	return l.LogWithFileTime(model, kind, statusCode, raw, time.Time{})
}

func (l *llmRawHTTPLogger) LogWithFileTime(model, kind string, statusCode int, raw string, fileTime time.Time) error {
	if l == nil || strings.TrimSpace(l.dir) == "" {
		return nil
	}
	now := l.currentTime()
	if fileTime.IsZero() {
		fileTime = now
	}

	// Compact JSON bodies to single line if possible
	compacted := new(bytes.Buffer)
	if err := json.Compact(compacted, []byte(raw)); err == nil {
		raw = compacted.String()
	} else {
		// Not JSON or malformed, escape newlines
		raw = strings.ReplaceAll(raw, "\n", "\\n")
		raw = strings.ReplaceAll(raw, "\r", "\\r")
	}

	// Create JSONL entry with ordered fields
	entry := struct {
		TS     string `json:"ts"`
		Kind   string `json:"kind"`
		Status int    `json:"status"`
		Body   string `json:"body"`
	}{
		TS:     now.Format("15:04:05"),
		Kind:   strings.TrimSpace(kind),
		Status: statusCode,
		Body:   raw,
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}

	// File name includes date, time (hour+minute), and runtime ID.
	dateTimeStr := fileTime.Format("200601021504")
	fileName := "llm-http-" + dateTimeStr + ".log"
	if l.runtimeID != "" {
		fileName = "llm-http-" + dateTimeStr + "-" + l.runtimeID + ".log"
	}

	path := filepath.Join(l.dir, fileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(entryBytes, '\n'))
	return err
}

func (l *llmRawHTTPLogger) currentTime() time.Time {
	if l == nil || l.now == nil {
		return time.Now()
	}
	return l.now()
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

type compatibleChatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content   any                       `json:"content,omitempty"`
			ToolCalls []compatibleToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

type compatibleToolCallDelta struct {
	Index    int                     `json:"index"`
	ID       string                  `json:"id,omitempty"`
	Type     string                  `json:"type,omitempty"`
	Function *compatibleFunctionCall `json:"function,omitempty"`
}

type compatibleStreamingLogResponse struct {
	Stream  bool                           `json:"stream"`
	Done    bool                           `json:"done"`
	Choices []compatibleStreamingLogChoice `json:"choices"`
	Usage   map[string]any                 `json:"usage,omitempty"`
	Error   string                         `json:"error,omitempty"`
	RawSSE  string                         `json:"raw_sse,omitempty"`
}

type compatibleStreamingLogChoice struct {
	Message      compatibleStreamingLogMessage `json:"message"`
	FinishReason string                        `json:"finish_reason"`
}

type compatibleStreamingLogMessage struct {
	Content   string               `json:"content"`
	ToolCalls []compatibleToolCall `json:"tool_calls,omitempty"`
}

func newOpenAICompatibleModel(baseURL, model, token string, httpClient *http.Client, opts ...openAICompatibleModelOption) llms.Model {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	result := &openAICompatibleModel{
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		token:      token,
		httpClient: httpClient,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(result)
		}
	}
	return result
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
		Stream:           callOpts.StreamingFunc != nil,
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

	ctx = m.withRawHTTPLogFileTime(ctx)

	// Log HTTP request body
	_ = m.logRawHTTP(ctx, reqPayload.Model, "request", 0, string(payloadBytes))

	endpoint := m.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		// statusCode 0 marks a failure with no HTTP response, keeping every
		// logged request paired with a response entry the viewer can match.
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", 0, "create request error: "+err.Error())
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		// Transport-level failure (timeout, connection refused, context
		// cancelled): no HTTP response arrives, so log status 0 with the error
		// rather than leaving the request entry unpaired.
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", 0, "transport error: "+err.Error())
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, string(body))
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	if reqPayload.Stream {
		return m.decodeStreamingResponse(ctx, resp.Body, callOpts.StreamingFunc, reqPayload.Model, resp.StatusCode)
	}

	var decoded compatibleChatResponse
	rawLoggingEnabled := m.rawLogger != nil && rawHTTPLogEnabled(ctx)
	if !rawLoggingEnabled {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
	} else {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			// HTTP status arrived but the body could not be read: log the real
			// status with the read error so the request still has a response.
			_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, "read response error: "+err.Error())
			return nil, fmt.Errorf("read response: %w", err)
		}
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, string(body))
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
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

	return result, nil
}

func (m *openAICompatibleModel) decodeStreamingResponse(ctx context.Context, body io.Reader, stream func(context.Context, []byte) error, requestModel string, statusCode int) (*llms.ContentResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var content strings.Builder
	stopReason := ""
	toolCalls := map[int]*compatibleToolCall{}
	var generationInfo map[string]any
	var rawStream strings.Builder
	hasRawStream := false
	streamDone := false
	rawLoggingEnabled := m.rawLogger != nil && rawHTTPLogEnabled(ctx)
	logRawStream := func(scanErr error) {
		if !rawLoggingEnabled {
			return
		}
		// Pair every request with a response, even when the stream yields no
		// data or fails mid-read. Successful streams are logged as a readable
		// Chat Completions-style JSON response instead of raw SSE framing. On
		// failures, keep the partial parsed response and attach raw_sse for
		// debugging the malformed or interrupted stream.
		logStatusCode := statusCode
		rawSSE := ""
		if scanErr != nil {
			logStatusCode = 0
			if hasRawStream {
				rawSSE = rawStream.String()
			}
		}
		body := formatStreamingRawHTTPLogBody(content.String(), stopReason, orderedCompatibleToolCalls(toolCalls), generationInfo, streamDone, scanErr, rawSSE)
		_ = m.logRawHTTP(ctx, requestModel, "response", logStatusCode, body)
	}
	var scanErr error
	defer func() { logRawStream(scanErr) }()

	for scanner.Scan() {
		rawLine := scanner.Text()
		if rawLoggingEnabled {
			if hasRawStream {
				rawStream.WriteByte('\n')
			}
			rawStream.WriteString(rawLine)
			hasRawStream = true
		}
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			streamDone = true
			break
		}

		var event compatibleChatStreamResponse
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			scanErr = fmt.Errorf("decode stream event: %w", err)
			return nil, scanErr
		}
		if event.Usage != nil {
			generationInfo = map[string]any{
				"prompt_tokens":     event.Usage.PromptTokens,
				"completion_tokens": event.Usage.CompletionTokens,
				"total_tokens":      event.Usage.TotalTokens,
			}
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]
		if choice.FinishReason != "" {
			stopReason = choice.FinishReason
		}

		chunk := flattenResponseContent(choice.Delta.Content)
		if chunk != "" {
			content.WriteString(chunk)
			if stream != nil {
				if err := stream(ctx, []byte(chunk)); err != nil {
					scanErr = err
					return nil, scanErr
				}
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := toolCalls[delta.Index]
			if call == nil {
				call = &compatibleToolCall{Type: "function"}
				toolCalls[delta.Index] = call
			}
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Type != "" {
				call.Type = delta.Type
			}
			if delta.Function != nil {
				if call.Function == nil {
					call.Function = &compatibleFunctionCall{}
				}
				if delta.Function.Name != "" {
					call.Function.Name += delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					call.Function.Arguments += delta.Function.Arguments
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		scanErr = err
		return nil, fmt.Errorf("read stream response: %w", err)
	}

	orderedToolCalls := orderedCompatibleToolCalls(toolCalls)

	choice := &llms.ContentChoice{
		Content:    content.String(),
		StopReason: stopReason,
		ToolCalls:  convertResponseToolCalls(orderedToolCalls),
	}
	if len(choice.ToolCalls) > 0 {
		choice.FuncCall = choice.ToolCalls[0].FunctionCall
	}
	if generationInfo != nil {
		choice.GenerationInfo = generationInfo
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{choice}}, nil
}

func orderedCompatibleToolCalls(toolCalls map[int]*compatibleToolCall) []compatibleToolCall {
	orderedToolCalls := make([]compatibleToolCall, 0, len(toolCalls))
	toolCallIndexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		toolCallIndexes = append(toolCallIndexes, index)
	}
	sort.Ints(toolCallIndexes)
	for _, index := range toolCallIndexes {
		if call := toolCalls[index]; call != nil {
			orderedToolCalls = append(orderedToolCalls, *call)
		}
	}
	return orderedToolCalls
}

func formatStreamingRawHTTPLogBody(content, finishReason string, toolCalls []compatibleToolCall, usage map[string]any, done bool, streamErr error, rawSSE string) string {
	resp := compatibleStreamingLogResponse{
		Stream: true,
		Done:   done,
		Choices: []compatibleStreamingLogChoice{{
			Message: compatibleStreamingLogMessage{
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
	if streamErr != nil {
		resp.Error = streamErr.Error()
		resp.RawSSE = rawSSE
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"stream":true,"done":%t,"error":%q}`, done, err.Error())
	}
	return string(body)
}

func (m *openAICompatibleModel) logRawHTTP(ctx context.Context, modelName, kind string, statusCode int, raw string) error {
	if m == nil || m.rawLogger == nil {
		return nil
	}
	// Only log calls that opted in (the main conversation loop). Background
	// tasks share this model and logger; logging them would interleave entries
	// in the shared file and break the viewer's request/response pairing.
	if !rawHTTPLogEnabled(ctx) {
		return nil
	}
	if fileTime, ok := rawHTTPLogFileTime(ctx); ok {
		return m.rawLogger.LogWithFileTime(modelName, kind, statusCode, raw, fileTime)
	}
	return m.rawLogger.Log(modelName, kind, statusCode, raw)
}

func (m *openAICompatibleModel) withRawHTTPLogFileTime(ctx context.Context) context.Context {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return ctx
	}
	if _, ok := rawHTTPLogFileTime(ctx); ok {
		return ctx
	}
	return contextWithRawHTTPLogFileTime(ctx, m.rawLogger.currentTime())
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
