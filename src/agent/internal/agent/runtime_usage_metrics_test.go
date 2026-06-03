package agent

import (
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
