package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aiden-agent/internal/logging"
	"github.com/tmc/langchaingo/llms"
)

// visionDecisionResponseError means the provider returned a response but it
// never satisfied the operation's contract. It is not a negative observation.
type visionDecisionResponseError struct {
	Operation string
	Attempts  int
	Err       error
}

func (e *visionDecisionResponseError) Error() string {
	return fmt.Sprintf("vision %s response invalid after %d attempts: %v", e.Operation, e.Attempts, e.Err)
}

func (e *visionDecisionResponseError) Unwrap() error { return e.Err }

// requestVisionDecision is the boundary between model content and an executable
// decision. The parser defines required fields, types, enums and action bounds.
// A retry reuses the same observation and cannot type, tap or restore HID.
// JSON mode only controls syntax; it never replaces the operation's contract.
func requestVisionDecision[T any](ctx context.Context, v *llmTextInputVision, operation, prompt string, parse func(string) (T, error), screenshots ...screenshotResult) (T, int, error) {
	var zero T
	if len(screenshots) == 0 {
		return zero, 0, fmt.Errorf("screenshot data missing")
	}
	parts := []llms.ContentPart{llms.TextPart(prompt)}
	for _, screenshot := range screenshots {
		if strings.TrimSpace(screenshot.Data) == "" {
			return zero, 0, fmt.Errorf("screenshot data missing")
		}
		parts = append(parts, llms.ImageURLPart("data:image/jpeg;base64,"+screenshot.Data))
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 1; attempt <= textInputVisionParseAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, attempt - 1, err
		}
		attemptParts := append([]llms.ContentPart(nil), parts...)
		if lastErr != nil {
			attemptParts[0] = llms.TextPart(prompt + fmt.Sprintf("\n\nThe previous response failed validation: %s. Analyze the same screenshots and return one complete JSON object with the required fields and types.", truncateForLog(lastErr.Error(), 512)))
		}
		messages := []llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeSystem, "Analyze device screenshots. Return only the JSON decision object specified in the user prompt. Include every required decision field; use the documented negative or unknown result when the image is inconclusive."),
			{Role: llms.ChatMessageTypeHuman, Parts: attemptParts},
		}
		// Keep provider options stable across validation retries. A malformed
		// business response is not evidence that an API option is unsupported.
		response, err := v.generateContent(ctx, operation, messages, llms.WithJSONMode(), llms.WithMaxTokens(textInputVisionMaxTokens))
		if err != nil {
			return zero, attempt, err
		}
		raw, responseErr := visionDecisionContent(response)
		logging.Infof("agent", "text_input", "phase=vision_response operation=%q attempt=%d response=%q", operation, attempt, truncateForLog(raw, 2048))
		if responseErr == nil {
			var decision T
			decision, responseErr = parse(raw)
			if responseErr == nil {
				return decision, attempt, nil
			}
		}
		lastErr = responseErr
		logging.Warnf("agent", "text_input", "phase=vision_contract operation=%q attempt=%d error=%q", operation, attempt, responseErr.Error())
	}
	return zero, textInputVisionParseAttempts, &visionDecisionResponseError{Operation: operation, Attempts: textInputVisionParseAttempts, Err: lastErr}
}

func visionDecisionContent(response *llms.ContentResponse) (string, error) {
	if response == nil || len(response.Choices) != 1 || response.Choices[0] == nil {
		return "", fmt.Errorf("expected one model response choice")
	}
	choice := response.Choices[0]
	raw := stripJSONCodeFence(choice.Content)
	if len(choice.ToolCalls) != 0 || choice.FuncCall != nil {
		return raw, fmt.Errorf("expected a JSON decision, received a tool call")
	}
	switch strings.ToLower(choice.StopReason) {
	case "", "stop", "end_turn", "stop_sequence", "completed":
		// Empty stop reasons are used by providers without completion metadata.
	default:
		return raw, fmt.Errorf("model response did not complete normally: %s", choice.StopReason)
	}
	if raw == "" {
		return raw, fmt.Errorf("empty model response content")
	}
	return raw, nil
}

// Unknown fields are allowed for forward compatibility. Required fields must
// be present and non-null; decoding then enforces their declared Go types.
// This deliberately distinguishes an explicit false/0 from a missing decision.
func decodeVisionJSONObject(raw string, dst any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return fmt.Errorf("expected one JSON object: %w", err)
	}
	if fields == nil {
		return fmt.Errorf("expected one JSON object, got null")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return fmt.Errorf("missing %s: field is required and must not be null", key)
		}
	}
	return json.Unmarshal([]byte(raw), dst)
}
