package agent

import (
	"context"
	"encoding/base64"
	"fmt"
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
	ID           string
	Role         string
	StartedAt    time.Time
	EndedAt      time.Time
	Input        []map[string]interface{}
	Output       map[string]interface{}
	UsageDetails map[string]int
	Error        string
}

func newTelemetryPromptCapture(enabled bool) *telemetryPromptCapture {
	if !enabled {
		return nil
	}
	return &telemetryPromptCapture{}
}

func (c *telemetryPromptCapture) Record(ctx context.Context, startedAt, endedAt time.Time, messages []llms.MessageContent, res *llms.ContentResponse, err error) {
	if c == nil {
		return
	}
	call := telemetryPromptCall{
		ID:           uuid.NewString(),
		Role:         telemetryRoleFromContext(ctx),
		StartedAt:    startedAt.UTC(),
		EndedAt:      endedAt.UTC(),
		Input:        telemetryMessageInput(messages),
		Output:       telemetryContentResponse(res),
		UsageDetails: telemetryUsageDetails(res),
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

func telemetryMessageInput(messages []llms.MessageContent) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		item := map[string]interface{}{
			"role":  string(message.Role),
			"parts": telemetryContentParts(message.Parts),
		}
		out = append(out, item)
	}
	return out
}

func telemetryContentParts(parts []llms.ContentPart) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(parts))
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
			if typed.Detail != "" {
				item["detail"] = typed.Detail
			}
			out = append(out, item)
		case llms.BinaryContent:
			out = append(out, map[string]interface{}{
				"type":      "binary",
				"mime_type": typed.MIMEType,
				"data":      base64.StdEncoding.EncodeToString(typed.Data),
			})
		case llms.ToolCall:
			item := map[string]interface{}{
				"type":      "tool_call",
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
	return out
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
	if usage["total"] == 0 && (usage["input"] > 0 || usage["output"] > 0) {
		usage["total"] = usage["input"] + usage["output"]
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}
