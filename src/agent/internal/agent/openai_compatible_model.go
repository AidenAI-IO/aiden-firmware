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
	baseURL             string
	model               string
	token               string
	httpClient          *http.Client
	rawLogger           *llmRawHTTPLogger
	explicitPromptCache bool
	routerMetadata      bool
	reasoningEffort     string
	// sessionIDProvider, when set, supplies the value for the x-session-id
	// request header. It is only wired up for the OpenRouter provider, whose
	// sticky routing uses the session id to keep multi-turn requests on the same
	// upstream endpoint so the prompt cache stays warm. Direct providers ignore
	// the header, so it stays opt-in to avoid sending it where it has no effect.
	sessionIDProvider func() string
}

type openAICompatibleModelOption func(*openAICompatibleModel)

// rawHTTPLogContextKey gates raw HTTP logging to the calls that opt in via
// contextWithRawHTTPLog. The main conversation loop sets it; background tasks
// (summarization, profile rebuilds, skill merge) share the same model instance
// and logger, so without this gate their requests would interleave with the
// main loop in the shared log file and break request/response pairing.
type rawHTTPLogContextKey struct{}

type rawHTTPLogFileTimeContextKey struct{}
type rawHTTPLogFileSessionIDContextKey struct{}

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

func contextWithRawHTTPLogFileSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, rawHTTPLogFileSessionIDContextKey{}, strings.TrimSpace(sessionID))
}

func rawHTTPLogFileSessionID(ctx context.Context) (string, bool) {
	sessionID, ok := ctx.Value(rawHTTPLogFileSessionIDContextKey{}).(string)
	return strings.TrimSpace(sessionID), ok
}

func withOpenAICompatibleRawHTTPLogger(logger *llmRawHTTPLogger) openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.rawLogger = logger
	}
}

// withOpenAICompatibleSessionSticky enables the x-session-id header sourced from
// provider. Only OpenRouter benefits from it (sticky routing for warm caches);
// leave it unset for other providers.
func withOpenAICompatibleSessionSticky(provider func() string) openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.sessionIDProvider = provider
	}
}

func withOpenAICompatibleExplicitPromptCache() openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.explicitPromptCache = true
	}
}

func withOpenAICompatibleRouterMetadata() openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.routerMetadata = true
	}
}

func withOpenAICompatibleReasoningEffort(effort string) openAICompatibleModelOption {
	return func(m *openAICompatibleModel) {
		m.reasoningEffort = strings.TrimSpace(effort)
	}
}

// openRouterSessionIDMaxLen mirrors OpenRouter's documented 256-char limit for
// the sticky-routing session id.
const openRouterSessionIDMaxLen = 256

type llmRawHTTPLogger struct {
	dir               string
	sessionID         string
	sessionIDProvider func() string
	now               func() time.Time
	mu                sync.Mutex
}

func newLLMRawHTTPLogger(logDir, sessionID string) *llmRawHTTPLogger {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		return nil
	}
	return &llmRawHTTPLogger{
		dir:       logDir,
		sessionID: strings.TrimSpace(sessionID),
		now:       time.Now,
	}
}

func (l *llmRawHTTPLogger) SetSessionIDProvider(provider func() string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessionIDProvider = provider
}

func (l *llmRawHTTPLogger) Log(model, kind string, statusCode int, raw string) error {
	return l.LogWithFileTime(model, kind, statusCode, raw, time.Time{})
}

func (l *llmRawHTTPLogger) LogWithFileTime(model, kind string, statusCode int, raw string, fileTime time.Time) error {
	return l.LogWithFileScope(model, kind, statusCode, raw, fileTime, l.currentSessionID())
}

func (l *llmRawHTTPLogger) LogWithFileScope(model, kind string, statusCode int, raw string, fileTime time.Time, sessionID string) error {
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

	fileName := "llm-http-" + fileTime.Format("200601021504") + ".log"
	if sid := strings.TrimSpace(sessionID); sid != "" {
		if err := validateSessionIDPathComponent(sid); err == nil {
			fileName = "llm-http-" + sid + ".log"
		}
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

func (l *llmRawHTTPLogger) currentSessionID() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	provider := l.sessionIDProvider
	fallback := l.sessionID
	l.mu.Unlock()
	if provider != nil {
		if sessionID := strings.TrimSpace(provider()); sessionID != "" {
			return sessionID
		}
	}
	return strings.TrimSpace(fallback)
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
	Reasoning        *reasoningConfig    `json:"reasoning,omitempty"`
	ReasoningEffort  string              `json:"reasoning_effort,omitempty"`
}

type reasoningConfig struct {
	Effort  string `json:"effort,omitempty"`
	Exclude bool   `json:"exclude,omitempty"`
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
	Type         string                  `json:"type"`
	Text         string                  `json:"text,omitempty"`
	ImageURL     *compatibleImageURL     `json:"image_url,omitempty"`
	InputAudio   *compatibleInputAudio   `json:"input_audio,omitempty"`
	CacheControl *compatibleCacheControl `json:"cache_control,omitempty"`
}

type compatibleCacheControl struct {
	Type string `json:"type"`
}

type compatibleImageURL struct {
	URL string `json:"url"`
}

type compatibleInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// compatibleUsage mirrors the OpenAI-compatible usage block. cached_tokens
// comes from prompt_tokens_details and powers prompt-cache hit-rate metrics
// (cache hit rate = cached_tokens / prompt_tokens).
type compatibleUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

func (u *compatibleUsage) generationInfo() map[string]any {
	if u == nil {
		return nil
	}
	info := map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		info["cached_tokens"] = u.PromptTokensDetails.CachedTokens
	}
	if u.ReasoningTokens > 0 {
		info["reasoning_tokens"] = u.ReasoningTokens
	}
	return info
}

type compatibleChatResponse struct {
	ID      string `json:"id,omitempty"`
	Choices []struct {
		Message struct {
			Content   any                  `json:"content"`
			ToolCalls []compatibleToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage              *compatibleUsage `json:"usage,omitempty"`
	OpenRouterMetadata map[string]any   `json:"openrouter_metadata,omitempty"`
}

type compatibleChatStreamResponse struct {
	ID      string `json:"id,omitempty"`
	Choices []struct {
		Delta struct {
			Content   any                       `json:"content,omitempty"`
			ToolCalls []compatibleToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage              *compatibleUsage `json:"usage,omitempty"`
	OpenRouterMetadata map[string]any   `json:"openrouter_metadata,omitempty"`
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
	callStarted := time.Now()
	generationInfo := map[string]any{}
	requestPrepareStart := time.Now()
	callOpts := llms.CallOptions{}
	for _, option := range options {
		option(&callOpts)
	}

	requestMessages := make([]compatibleMessage, 0, len(messages))
	for _, message := range messages {
		converted, err := convertMessageContent(message, m.explicitPromptCache)
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
	// Apply reasoning policy: only include reasoning fields when explicitly configured.
	// Empty string = auto mode = omit from request (let model/provider decide).
	// This prevents sending unsupported parameters to providers that don't support reasoning.
	if m.reasoningEffort != "" {
		reqPayload.Reasoning = &reasoningConfig{
			Effort:  m.reasoningEffort,
			Exclude: m.reasoningEffort == "none",
		}
		reqPayload.ReasoningEffort = m.reasoningEffort
	}
	generationInfo["llm_request_prepare_ms"] = time.Since(requestPrepareStart).Milliseconds()

	marshalStart := time.Now()
	payloadBytes, err := json.Marshal(reqPayload)
	generationInfo["llm_json_marshal_ms"] = time.Since(marshalStart).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	generationInfo["llm_request_bytes"] = len(payloadBytes)
	generationInfo["llm_stream"] = reqPayload.Stream

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
	if m.sessionIDProvider != nil {
		if sid := strings.TrimSpace(m.sessionIDProvider()); sid != "" {
			if len(sid) > openRouterSessionIDMaxLen {
				sid = sid[:openRouterSessionIDMaxLen]
			}
			req.Header.Set("x-session-id", sid)
		}
	}
	if m.routerMetadata {
		req.Header.Set("X-OpenRouter-Metadata", "enabled")
	}

	doStart := time.Now()
	resp, err := m.httpClient.Do(req)
	generationInfo["llm_http_to_headers_ms"] = time.Since(doStart).Milliseconds()
	if err != nil {
		// Transport-level failure (timeout, connection refused, context
		// cancelled): no HTTP response arrives, so log status 0 with the error
		// rather than leaving the request entry unpaired.
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", 0, "transport error: "+err.Error())
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	generationInfo["llm_http_status"] = resp.StatusCode
	if contentType := strings.TrimSpace(resp.Header.Get("Content-Type")); contentType != "" {
		generationInfo["llm_response_content_type"] = contentType
	}
	if generationID := strings.TrimSpace(resp.Header.Get("X-Generation-Id")); generationID != "" {
		generationInfo["openrouter_generation_id"] = generationID
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, string(body))
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	if reqPayload.Stream {
		return m.decodeStreamingResponse(ctx, resp.Body, callOpts.StreamingFunc, reqPayload.Model, resp.StatusCode, callStarted, generationInfo)
	}

	var decoded compatibleChatResponse
	rawLoggingEnabled := m.rawLogger != nil && rawHTTPLogEnabled(ctx)
	if !rawLoggingEnabled {
		generationInfo["llm_response_read_ms"] = int64(0)
		decodeStart := time.Now()
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			generationInfo["llm_response_decode_ms"] = time.Since(decodeStart).Milliseconds()
			return nil, fmt.Errorf("decode response: %w", err)
		}
		generationInfo["llm_response_decode_ms"] = time.Since(decodeStart).Milliseconds()
	} else {
		readStart := time.Now()
		body, err := io.ReadAll(resp.Body)
		generationInfo["llm_response_read_ms"] = time.Since(readStart).Milliseconds()
		if err != nil {
			// HTTP status arrived but the body could not be read: log the real
			// status with the read error so the request still has a response.
			_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, "read response error: "+err.Error())
			return nil, fmt.Errorf("read response: %w", err)
		}
		_ = m.logRawHTTP(ctx, reqPayload.Model, "response", resp.StatusCode, string(body))
		decodeStart := time.Now()
		if err := json.Unmarshal(body, &decoded); err != nil {
			generationInfo["llm_response_decode_ms"] = time.Since(decodeStart).Milliseconds()
			return nil, fmt.Errorf("decode response: %w", err)
		}
		generationInfo["llm_response_decode_ms"] = time.Since(decodeStart).Milliseconds()
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
	if len(result.Choices[0].ToolCalls) > 0 {
		result.Choices[0].FuncCall = result.Choices[0].ToolCalls[0].FunctionCall
	}
	generationInfo["llm_output_chars"] = len(content)
	generationInfo["llm_tool_call_count"] = len(result.Choices[0].ToolCalls)
	if choice.FinishReason != "" {
		generationInfo["llm_finish_reason"] = choice.FinishReason
	}
	if decoded.ID != "" {
		generationInfo["llm_response_id"] = decoded.ID
	}
	addOpenRouterMetadataGenerationInfo(generationInfo, decoded.OpenRouterMetadata)
	result.Choices[0].GenerationInfo = finalizeLLMGenerationInfo(mergeGenerationInfo(decoded.Usage.generationInfo(), generationInfo), callStarted)

	return result, nil
}

func (m *openAICompatibleModel) decodeStreamingResponse(ctx context.Context, body io.Reader, stream func(context.Context, []byte) error, requestModel string, statusCode int, callStarted time.Time, generationInfo map[string]any) (*llms.ContentResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if generationInfo == nil {
		generationInfo = map[string]any{}
	}
	var content strings.Builder
	stopReason := ""
	toolCalls := map[int]*compatibleToolCall{}
	var usageInfo map[string]any
	var rawStream strings.Builder
	hasRawStream := false
	streamDone := false
	streamReadStart := time.Now()
	firstSSESeen := false
	firstContentSeen := false
	sseEvents := 0
	contentChunks := 0
	commentCount := 0
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
		body := formatStreamingRawHTTPLogBody(content.String(), stopReason, orderedCompatibleToolCalls(toolCalls), usageInfo, streamDone, scanErr, rawSSE)
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
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			commentCount++
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if !firstSSESeen {
			generationInfo["llm_time_to_first_sse_ms"] = time.Since(callStarted).Milliseconds()
			firstSSESeen = true
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			streamDone = true
			break
		}
		sseEvents++

		var event compatibleChatStreamResponse
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			scanErr = fmt.Errorf("decode stream event: %w", err)
			return nil, scanErr
		}
		if info := event.Usage.generationInfo(); info != nil {
			usageInfo = info
		}
		addOpenRouterMetadataGenerationInfo(generationInfo, event.OpenRouterMetadata)
		if event.ID != "" {
			generationInfo["llm_response_id"] = event.ID
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
			if !firstContentSeen {
				generationInfo["llm_time_to_first_content_ms"] = time.Since(callStarted).Milliseconds()
				firstContentSeen = true
			}
			contentChunks++
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
	generationInfo["llm_stream_read_ms"] = time.Since(streamReadStart).Milliseconds()
	generationInfo["llm_stream_sse_events"] = sseEvents
	generationInfo["llm_stream_content_chunks"] = contentChunks
	generationInfo["llm_stream_comment_count"] = commentCount
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
	generationInfo["llm_output_chars"] = content.Len()
	generationInfo["llm_tool_call_count"] = len(choice.ToolCalls)
	if stopReason != "" {
		generationInfo["llm_finish_reason"] = stopReason
	}
	choice.GenerationInfo = finalizeLLMGenerationInfo(mergeGenerationInfo(usageInfo, generationInfo), callStarted)
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{choice}}, nil
}

func mergeGenerationInfo(base map[string]any, extras map[string]any) map[string]any {
	if len(base) == 0 && len(extras) == 0 {
		return nil
	}
	merged := map[string]any{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extras {
		merged[key] = value
	}
	return merged
}

func finalizeLLMGenerationInfo(info map[string]any, callStarted time.Time) map[string]any {
	if info == nil {
		info = map[string]any{}
	}
	totalMs := time.Since(callStarted).Milliseconds()
	info["llm_total_ms"] = totalMs
	if completionTokens, ok := usageMetricInt(info["completion_tokens"]); ok && completionTokens > 0 {
		info["llm_ms_per_output_token"] = float64(totalMs) / float64(completionTokens)
	}
	if promptTokens, ok := usageMetricInt(info["prompt_tokens"]); ok && promptTokens > 0 {
		if ttftMs, ok := usageMetricInt(info["llm_time_to_first_content_ms"]); ok && ttftMs >= 0 {
			info["llm_ttft_per_input_token"] = float64(ttftMs) / float64(promptTokens)
		}
	}
	return info
}

func addOpenRouterMetadataGenerationInfo(info map[string]any, metadata map[string]any) {
	if info == nil || len(metadata) == 0 {
		return
	}
	info["openrouter_metadata"] = metadata
	copyFirstOpenRouterMetadataValue(info, metadata, "openrouter_provider_name", "provider_name", "provider", "selected_provider")
	copyFirstOpenRouterMetadataValue(info, metadata, "openrouter_strategy", "strategy", "routing_strategy")
	copyFirstOpenRouterMetadataValue(info, metadata, "openrouter_region", "region")
	copyFirstOpenRouterMetadataValue(info, metadata, "openrouter_pipeline", "pipeline")
	copyFirstOpenRouterMetadataValue(info, metadata, "openrouter_attempt", "attempt")
	if attempts, ok := metadata["attempts"].([]any); ok {
		info["openrouter_attempts_count"] = len(attempts)
	}
}

func copyFirstOpenRouterMetadataValue(info map[string]any, metadata map[string]any, target string, keys ...string) {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				continue
			}
		case nil:
			continue
		}
		info[target] = value
		return
	}
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
		sessionID, _ := rawHTTPLogFileSessionID(ctx)
		return m.rawLogger.LogWithFileScope(modelName, kind, statusCode, raw, fileTime, sessionID)
	}
	return m.rawLogger.Log(modelName, kind, statusCode, raw)
}

func (m *openAICompatibleModel) withRawHTTPLogFileTime(ctx context.Context) context.Context {
	if m == nil || m.rawLogger == nil || !rawHTTPLogEnabled(ctx) {
		return ctx
	}
	if _, ok := rawHTTPLogFileTime(ctx); ok {
		if _, hasSessionID := rawHTTPLogFileSessionID(ctx); hasSessionID {
			return ctx
		}
		return contextWithRawHTTPLogFileSessionID(ctx, m.rawLogger.currentSessionID())
	}
	ctx = contextWithRawHTTPLogFileTime(ctx, m.rawLogger.currentTime())
	return contextWithRawHTTPLogFileSessionID(ctx, m.rawLogger.currentSessionID())
}

func convertMessageContent(message llms.MessageContent, explicitPromptCache bool) (compatibleMessage, error) {
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
	for i, part := range message.Parts {
		switch typed := part.(type) {
		case llms.TextContent:
			converted := compatibleContentPart{
				Type: "text",
				Text: typed.Text,
			}
			if explicitPromptCache && message.Role == llms.ChatMessageTypeSystem && i == 0 && len(message.Parts) > 1 {
				converted.CacheControl = &compatibleCacheControl{Type: "ephemeral"}
			}
			textParts = append(textParts, converted)
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
					Arguments: normalizeCompatibleToolArguments(typed.FunctionCall.Arguments),
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

func normalizeCompatibleToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	return encodeToolArguments(arguments)
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
	systemParts := make([]compatibleContentPart, 0, 2)
	preserveSystemParts := false
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
				systemParts = append(systemParts, compatibleContentPart{Type: "text", Text: content})
			}
		case []compatibleContentPart:
			for _, part := range content {
				if part.CacheControl != nil {
					preserveSystemParts = true
				}
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					systemSegments = append(systemSegments, part.Text)
				}
				systemParts = append(systemParts, part)
			}
		}
	}

	if len(systemSegments) == 0 && !preserveSystemParts {
		return mergeConsecutiveSameRoleMessages(normalized)
	}
	if preserveSystemParts {
		return mergeConsecutiveSameRoleMessages(append([]compatibleMessage{{Role: "system", Content: systemParts}}, normalized...))
	}

	mergedSystem := compatibleMessage{
		Role:    "system",
		Content: strings.Join(systemSegments, "\n\n"),
	}
	return mergeConsecutiveSameRoleMessages(append([]compatibleMessage{mergedSystem}, normalized...))
}

// mergeConsecutiveSameRoleMessages merges consecutive messages with the same role.
// This is required by some providers (Anthropic Claude, Google Gemini) that enforce
// strict user/assistant alternation. Tool messages are never merged as they require
// specific tool_call_id pairing.
func mergeConsecutiveSameRoleMessages(messages []compatibleMessage) []compatibleMessage {
	if len(messages) <= 1 {
		return messages
	}

	result := make([]compatibleMessage, 0, len(messages))
	result = append(result, messages[0])

	for i := 1; i < len(messages); i++ {
		current := messages[i]
		previous := &result[len(result)-1]

		// Only merge if roles match and neither is a tool message
		canMerge := current.Role == previous.Role &&
			current.Role != "tool" &&
			len(current.ToolCalls) == 0 &&
			len(previous.ToolCalls) == 0

		if !canMerge {
			result = append(result, current)
			continue
		}

		// Merge content based on type
		prevContent, prevIsString := previous.Content.(string)
		currContent, currIsString := current.Content.(string)

		if prevIsString && currIsString {
			// Both are strings: join with double newline
			previous.Content = prevContent + "\n\n" + currContent
		} else if prevIsString && !currIsString {
			// Previous is string, current is parts: convert previous to parts and merge
			parts := []compatibleContentPart{{Type: "text", Text: prevContent}}
			if currParts, ok := current.Content.([]compatibleContentPart); ok {
				parts = append(parts, currParts...)
			}
			previous.Content = parts
		} else if !prevIsString && currIsString {
			// Previous is parts, current is string: append as text part
			if prevParts, ok := previous.Content.([]compatibleContentPart); ok {
				prevParts = append(prevParts, compatibleContentPart{Type: "text", Text: currContent})
				previous.Content = prevParts
			}
		} else {
			// Both are parts: concatenate arrays
			if prevParts, ok := previous.Content.([]compatibleContentPart); ok {
				if currParts, ok := current.Content.([]compatibleContentPart); ok {
					previous.Content = append(prevParts, currParts...)
				}
			}
		}
	}

	return result
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
