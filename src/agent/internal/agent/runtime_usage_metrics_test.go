package agent

import (
	"context"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func usageResponse(prompt, completion, total int) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			GenerationInfo: map[string]any{
				"prompt_tokens":     prompt,
				"completion_tokens": completion,
				"total_tokens":      total,
			},
		}},
	}
}

// TestRecordUsageMetricsAccumulatesAcrossCalls verifies that the role-collaborative
// loop's multiple LLM calls sum into the run metrics instead of overwriting, while
// LastPromptTokens still tracks the most recent single call for the compression
// heuristic.
func TestRecordUsageMetricsAccumulatesAcrossCalls(t *testing.T) {
	metrics := &RunMetrics{}

	recordUsageMetrics(metrics, usageResponse(100, 20, 120)) // planner
	recordUsageMetrics(metrics, usageResponse(150, 30, 180)) // executor
	recordUsageMetrics(metrics, usageResponse(90, 10, 100))  // verifier

	if metrics.PromptTokens != 340 {
		t.Errorf("PromptTokens = %d, want 340 (100+150+90)", metrics.PromptTokens)
	}
	if metrics.CompletionTokens != 60 {
		t.Errorf("CompletionTokens = %d, want 60 (20+30+10)", metrics.CompletionTokens)
	}
	if metrics.TotalTokens != 400 {
		t.Errorf("TotalTokens = %d, want 400 (120+180+100)", metrics.TotalTokens)
	}
	if metrics.LastPromptTokens != 90 {
		t.Errorf("LastPromptTokens = %d, want 90 (last call only)", metrics.LastPromptTokens)
	}
}

func TestUsageTrackingModelCapturesFullPromptForTelemetry(t *testing.T) {
	capture := newTelemetryPromptCapture(true)
	model := &usageTrackingModel{
		inner:         &scriptedModel{responses: []*llms.ContentResponse{usageResponse(10, 5, 15)}},
		metrics:       &RunMetrics{},
		promptCapture: capture,
	}
	messages := []llms.MessageContent{
		{Role: llms.ChatMessageTypeSystem, Parts: []llms.ContentPart{llms.TextPart("full system prompt")}},
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("full user prompt")}},
	}

	_, err := model.GenerateContent(contextWithTelemetryRole(context.Background(), RolePlanner), messages,
		llms.WithTemperature(0.3), llms.WithMaxTokens(123))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	snapshot := capture.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("captured prompts = %d, want 1", len(snapshot))
	}
	if snapshot[0].Role != string(RolePlanner) {
		t.Fatalf("captured role = %q, want planner", snapshot[0].Role)
	}
	parts, ok := snapshot[0].Input[0]["parts"].([]map[string]interface{})
	if !ok || len(parts) != 1 || parts[0]["text"] != "full system prompt" {
		t.Fatalf("captured system prompt = %#v", snapshot[0].Input[0]["parts"])
	}
	if snapshot[0].UsageDetails["input"] != 10 || snapshot[0].UsageDetails["output"] != 5 || snapshot[0].UsageDetails["total"] != 15 {
		t.Fatalf("captured usage = %#v, want input/output/total 10/5/15", snapshot[0].UsageDetails)
	}
	if snapshot[0].ModelParameters["temperature"] != 0.3 || snapshot[0].ModelParameters["max_tokens"] != 123 {
		t.Fatalf("captured model parameters = %#v, want temperature/max_tokens", snapshot[0].ModelParameters)
	}
}
