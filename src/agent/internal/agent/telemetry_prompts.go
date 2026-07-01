package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"
)

type telemetryPromptContextKey struct{}

func contextWithTelemetryRole(ctx context.Context, role RoleName) context.Context {
	return context.WithValue(ctx, telemetryPromptContextKey{}, string(role))
}

func telemetryRoleFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(telemetryPromptContextKey{}).(string); ok {
		return value
	}
	return ""
}

type telemetryPromptCapture struct {
	mu    sync.Mutex
	calls []telemetryPromptCall
}

type telemetryPromptCall struct {
	ID              string
	Role            string
	StartedAt       time.Time
	EndedAt         time.Time
	Input           []map[string]interface{}
	Media           []telemetryPromptMedia
	Output          map[string]interface{}
	UsageDetails    map[string]int
	CostDetails     map[string]float64
	ModelParameters map[string]interface{}
	Metadata        map[string]interface{}
	Error           string
}

type telemetryPromptMedia struct {
	Placeholder string
	ContentType string
	Data        []byte
}

func newTelemetryPromptCapture(enabled bool) *telemetryPromptCapture {
	if !enabled {
		return nil
	}
	return &telemetryPromptCapture{}
}

func (c *telemetryPromptCapture) Record(ctx context.Context, startedAt, endedAt time.Time, messages []llms.MessageContent, options []llms.CallOption, res *llms.ContentResponse, err error, contextWindow int) {
	if c == nil {
		return
	}
	input, media := telemetryMessageInput(messages)
	call := telemetryPromptCall{
		ID:              uuid.NewString(),
		Role:            telemetryRoleFromContext(ctx),
		StartedAt:       startedAt.UTC(),
		EndedAt:         endedAt.UTC(),
		Input:           input,
		Media:           media,
		Output:          telemetryContentResponse(res),
		UsageDetails:    telemetryUsageDetails(res),
		CostDetails:     telemetryCostDetails(res),
		ModelParameters: telemetryModelParameters(options),
		Metadata:        telemetryPromptMetadata(options),
	}
	if contextWindow > 0 {
		if call.ModelParameters == nil {
			call.ModelParameters = map[string]interface{}{}
		}
		call.ModelParameters["context_window"] = contextWindow
	}
	if err != nil {
		call.Error = err.Error()
	}

	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
}

func (c *telemetryPromptCapture) Snapshot() []telemetryPromptCall {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]telemetryPromptCall(nil), c.calls...)
}

func telemetryMessageInput(messages []llms.MessageContent) ([]map[string]interface{}, []telemetryPromptMedia) {
	out := make([]map[string]interface{}, 0, len(messages))
	var media []telemetryPromptMedia
	for _, message := range messages {
		parts, partMedia := telemetryContentParts(message.Parts)
		item := map[string]interface{}{
			"role":  string(message.Role),
			"parts": parts,
		}
		out = append(out, item)
		media = append(media, partMedia...)
	}
	return out, media
}

func telemetryContentParts(parts []llms.ContentPart) ([]map[string]interface{}, []telemetryPromptMedia) {
	out := make([]map[string]interface{}, 0, len(parts))
	var media []telemetryPromptMedia
	for _, part := range parts {
		switch typed := part.(type) {
		case llms.TextContent:
			out = append(out, map[string]interface{}{
				"type": "text",
				"text": typed.Text,
			})
		case llms.ImageURLContent:
			item := map[string]interface{}{
				"type": "image_url",
				"url":  typed.URL,
			}
			if contentType, data, ok := telemetryDataURL(typed.URL); ok {
				promptMedia := newTelemetryPromptMedia(contentType, data)
				item["url"] = promptMedia.Placeholder
				item["mime_type"] = promptMedia.ContentType
				item["size"] = len(promptMedia.Data)
				item["sha256"] = telemetryMediaSHA256(promptMedia.Data)
				media = append(media, promptMedia)
			} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed.URL)), "data:") {
				item["url"] = "[data URL omitted: invalid or unsupported]"
			}
			if typed.Detail != "" {
				item["detail"] = typed.Detail
			}
			out = append(out, item)
		case llms.BinaryContent:
			promptMedia := newTelemetryPromptMedia(typed.MIMEType, typed.Data)
			out = append(out, map[string]interface{}{
				"type":      "binary",
				"mime_type": promptMedia.ContentType,
				"size":      len(promptMedia.Data),
				"sha256":    telemetryMediaSHA256(promptMedia.Data),
				"data":      promptMedia.Placeholder,
			})
			media = append(media, promptMedia)
		case llms.ToolCall:
			item := map[string]interface{}{
				"type":      runEventToolCall,
				"id":        typed.ID,
				"tool_type": typed.Type,
			}
			if typed.FunctionCall != nil {
				item["function"] = map[string]interface{}{
					"name":      typed.FunctionCall.Name,
					"arguments": typed.FunctionCall.Arguments,
				}
			}
			out = append(out, item)
		case llms.ToolCallResponse:
			out = append(out, map[string]interface{}{
				"type":         "tool_call_response",
				"tool_call_id": typed.ToolCallID,
				"name":         typed.Name,
				"content":      typed.Content,
			})
		default:
			out = append(out, map[string]interface{}{
				"type":  fmt.Sprintf("%T", part),
				"value": fmt.Sprint(part),
			})
		}
	}
	return out, media
}

func newTelemetryPromptMedia(contentType string, data []byte) telemetryPromptMedia {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return telemetryPromptMedia{
		Placeholder: "@@@aidenTelemetryMedia:" + uuid.NewString() + "@@@",
		ContentType: contentType,
		Data:        data,
	}
}

func telemetryDataURL(raw string) (string, []byte, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", nil, false
	}
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return "", nil, false
	}
	header := raw[len("data:"):comma]
	segments := strings.Split(header, ";")
	if len(segments) < 2 || !strings.EqualFold(strings.TrimSpace(segments[len(segments)-1]), "base64") {
		return "", nil, false
	}
	contentType := strings.TrimSpace(segments[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data, err := base64.StdEncoding.DecodeString(raw[comma+1:])
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw[comma+1:])
		if err != nil {
			return "", nil, false
		}
	}
	return contentType, data, true
}

func telemetryMediaSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func telemetryContentResponse(res *llms.ContentResponse) map[string]interface{} {
	if res == nil {
		return nil
	}
	choices := make([]map[string]interface{}, 0, len(res.Choices))
	for _, choice := range res.Choices {
		if choice == nil {
			continue
		}
		item := map[string]interface{}{
			"content":            choice.Content,
			"reasoning_content":  choice.ReasoningContent,
			"stop_reason":        choice.StopReason,
			"generation_info":    choice.GenerationInfo,
			"tool_calls":         choice.ToolCalls,
			"function_call":      choice.FuncCall,
			"function_call_name": "",
		}
		if choice.FuncCall != nil {
			item["function_call_name"] = choice.FuncCall.Name
		}
		choices = append(choices, item)
	}
	return map[string]interface{}{"choices": choices}
}

func telemetryUsageDetails(res *llms.ContentResponse) map[string]int {
	if res == nil || len(res.Choices) == 0 || res.Choices[0] == nil {
		return nil
	}
	info := res.Choices[0].GenerationInfo
	if info == nil {
		return nil
	}
	usage := map[string]int{}
	if v, ok := usageMetricInt(info["prompt_tokens"]); ok {
		usage["input"] = v
	}
	if v, ok := usageMetricInt(info["completion_tokens"]); ok {
		usage["output"] = v
	}
	if v, ok := usageMetricInt(info["total_tokens"]); ok {
		usage["total"] = v
	}
	if v, ok := usageMetricInt(info["cached_tokens"]); ok {
		usage["cached"] = v
	}
	if usage["total"] == 0 && (usage["input"] > 0 || usage["output"] > 0) {
		usage["total"] = usage["input"] + usage["output"]
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

func telemetryCostDetails(res *llms.ContentResponse) map[string]float64 {
	if res == nil || len(res.Choices) == 0 || res.Choices[0] == nil {
		return nil
	}
	info := res.Choices[0].GenerationInfo
	if info == nil {
		return nil
	}
	return telemetryCostDetailsFromMap(info)
}

func telemetryCostDetailsFromMap(info map[string]any) map[string]float64 {
	if len(info) == 0 {
		return nil
	}
	cost := map[string]float64{}
	if v, ok := costMetricFloat(firstMapValue(info, "input_cost", "prompt_cost")); ok {
		cost["input"] = v
	}
	if v, ok := costMetricFloat(firstMapValue(info, "output_cost", "completion_cost")); ok {
		cost["output"] = v
	}
	if v, ok := costMetricFloat(firstMapValue(info, "total_cost", "cost", "estimated_cost", "cost_usd")); ok {
		cost["total"] = v
	}
	if cost["total"] == 0 && (cost["input"] > 0 || cost["output"] > 0) {
		cost["total"] = cost["input"] + cost["output"]
	}
	if len(cost) == 0 {
		return nil
	}
	return cost
}

func telemetryModelParameters(options []llms.CallOption) map[string]interface{} {
	var opts llms.CallOptions
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	return telemetryModelParametersFromCallOptions(opts)
}

func telemetryModelParametersFromCallOptions(opts llms.CallOptions) map[string]interface{} {
	params := map[string]interface{}{}
	if opts.MaxTokens > 0 {
		params["max_response_tokens"] = opts.MaxTokens
	}
	if opts.Temperature != 0 {
		params["temperature"] = opts.Temperature
	}
	if opts.TopP != 0 {
		params["top_p"] = opts.TopP
	}
	if opts.TopK != 0 {
		params["top_k"] = opts.TopK
	}
	if opts.Seed != 0 {
		params["seed"] = opts.Seed
	}
	if opts.CandidateCount > 0 {
		params["candidate_count"] = opts.CandidateCount
	}
	if opts.N > 0 {
		params["n"] = opts.N
	}
	if opts.JSONMode {
		params["json"] = true
	}
	if opts.ResponseMIMEType != "" {
		params["response_mime_type"] = opts.ResponseMIMEType
	}
	if len(opts.StopWords) > 0 {
		params["stop_words"] = append([]string(nil), opts.StopWords...)
	}
	if opts.ToolChoice != nil {
		params["tool_choice"] = fmt.Sprint(opts.ToolChoice)
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func telemetryPromptMetadata(options []llms.CallOption) map[string]interface{} {
	opts := llms.CallOptions{}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(&opts)
	}
	meta := map[string]interface{}{}
	if len(opts.Tools) > 0 {
		meta["tools_count"] = len(opts.Tools)
		meta["tool_schemas"] = telemetryToolDefinitions(opts.Tools)
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func telemetryToolDefinitions(tools []llms.Tool) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		item := map[string]interface{}{}
		if tool.Type != "" {
			item["type"] = tool.Type
		}
		if tool.Function != nil {
			function := map[string]interface{}{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
			}
			if tool.Function.Parameters != nil {
				function["parameters"] = telemetryJSONClone(tool.Function.Parameters)
			}
			if tool.Function.Strict {
				function["strict"] = true
			}
			item["function"] = function
		}
		out = append(out, item)
	}
	return out
}

func telemetryJSONClone(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	var out interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Sprint(value)
	}
	return out
}

func firstMapValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func costMetricFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
