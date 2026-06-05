package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHumanHandoffTool_Name(t *testing.T) {
	tool := NewHumanHandoffTool()
	if got := tool.Name(); got != "request_human_handoff" {
		t.Errorf("Name() = %q, want %q", got, "request_human_handoff")
	}
}

func TestHumanHandoffTool_Description(t *testing.T) {
	tool := NewHumanHandoffTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
	if !strings.Contains(desc, "human intervention") {
		t.Error("Description() doesn't mention human intervention")
	}
	if !strings.Contains(desc, "returns immediately") {
		t.Error("Description() doesn't mention non-blocking behavior")
	}
}

func TestHumanHandoffTool_Call_Success(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"reason": "authentication",
		"details": "Login screen requires password",
		"suggested_action": "Please enter your credentials"
	}`

	ctx := context.Background()
	result, err := tool.Call(ctx, input)

	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}

	if !strings.Contains(result, "HUMAN_HANDOFF_REQUESTED") {
		t.Errorf("Result doesn't contain handoff marker: %q", result)
	}

	if !strings.Contains(result, "Login screen requires password") {
		t.Errorf("Result doesn't contain details: %q", result)
	}

	if !strings.Contains(result, "Please enter your credentials") {
		t.Errorf("Result doesn't contain suggested action: %q", result)
	}

	if !strings.Contains(result, "tell you when they have completed") {
		t.Errorf("Result doesn't contain continuation instruction: %q", result)
	}
}

func TestHumanHandoffTool_Call_AllReasons(t *testing.T) {
	validReasons := []string{
		"authentication",
		"captcha",
		"verification_code",
		"sensitive_operation",
		"black_screen",
		"ambiguous_situation",
		"unsupported_action",
		"stuck",
		"other",
	}

	for _, reason := range validReasons {
		t.Run(reason, func(t *testing.T) {
			tool := NewHumanHandoffTool()

			input := map[string]string{
				"reason":  reason,
				"details": "Test details for " + reason,
			}
			inputJSON, _ := json.Marshal(input)

			ctx := context.Background()
			result, err := tool.Call(ctx, string(inputJSON))

			if err != nil {
				t.Errorf("Call() error = %v, want nil for reason %q", err, reason)
			}

			if !strings.Contains(result, "HUMAN_HANDOFF_REQUESTED") {
				t.Errorf("Result doesn't contain handoff marker for reason %q: %q", reason, result)
			}
		})
	}
}

func TestHumanHandoffTool_Call_MissingReason(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"details": "Some details"
	}`

	ctx := context.Background()
	_, err := tool.Call(ctx, input)

	if err == nil {
		t.Error("Call() error = nil, want error for missing reason")
	}

	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("Error message doesn't mention reason: %v", err)
	}
}

func TestHumanHandoffTool_Call_MissingDetails(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"reason": "authentication"
	}`

	ctx := context.Background()
	_, err := tool.Call(ctx, input)

	if err == nil {
		t.Error("Call() error = nil, want error for missing details")
	}

	if !strings.Contains(err.Error(), "details") {
		t.Errorf("Error message doesn't mention details: %v", err)
	}
}

func TestHumanHandoffTool_Call_WhitespaceOnlyRequiredFields(t *testing.T) {
	tool := NewHumanHandoffTool()

	tests := []struct {
		name      string
		input     string
		wantInErr string
	}{
		{
			name:      "whitespace reason",
			input:     `{"reason":"   ","details":"some details"}`,
			wantInErr: "reason",
		},
		{
			name:      "whitespace details",
			input:     `{"reason":"authentication","details":"   "}`,
			wantInErr: "details",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Call(context.Background(), tt.input)
			if err == nil {
				t.Fatalf("Call() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("Error message doesn't mention %q: %v", tt.wantInErr, err)
			}
		})
	}
}

func TestHumanHandoffTool_Call_InvalidReason(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"reason": "invalid_reason",
		"details": "Some details"
	}`

	ctx := context.Background()
	_, err := tool.Call(ctx, input)

	if err == nil {
		t.Error("Call() error = nil, want error for invalid reason")
	}

	if !strings.Contains(err.Error(), "invalid reason") {
		t.Errorf("Error message doesn't indicate invalid reason: %v", err)
	}
}

func TestHumanHandoffTool_Call_InvalidJSON(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `not valid json`

	ctx := context.Background()
	_, err := tool.Call(ctx, input)

	if err == nil {
		t.Error("Call() error = nil, want error for invalid JSON")
	}
}

func TestHumanHandoffTool_Call_OptionalFields(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"reason": "stuck",
		"details": "Unable to proceed"
	}`

	ctx := context.Background()
	result, err := tool.Call(ctx, input)

	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}

	if !strings.Contains(result, "HUMAN_HANDOFF_REQUESTED") {
		t.Errorf("Result doesn't contain handoff marker: %q", result)
	}

	// Should have default instruction when no suggested_action is provided
	if !strings.Contains(result, "Tell the user") {
		t.Errorf("Result doesn't contain default instruction: %q", result)
	}
}

func TestHumanHandoffTool_Call_DefaultInstructions(t *testing.T) {
	tests := []struct {
		reason         string
		expectedPhrase string
	}{
		{"authentication", "enter their credentials"},
		{"captcha", "complete the verification"},
		{"verification_code", "complete the verification"},
		{"sensitive_operation", "review and confirm"},
		{"black_screen", "manually complete the action"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			tool := NewHumanHandoffTool()

			input := map[string]string{
				"reason":  tt.reason,
				"details": "Test details",
			}
			inputJSON, _ := json.Marshal(input)

			ctx := context.Background()
			result, err := tool.Call(ctx, string(inputJSON))

			if err != nil {
				t.Fatalf("Call() error = %v, want nil", err)
			}

			if !strings.Contains(result, tt.expectedPhrase) {
				t.Errorf("Result for %q doesn't contain expected phrase %q: %q",
					tt.reason, tt.expectedPhrase, result)
			}
		})
	}
}

func TestHumanHandoffTool_Call_ReturnsImmediately(t *testing.T) {
	tool := NewHumanHandoffTool()

	input := `{
		"reason": "authentication",
		"details": "Test handoff",
		"suggested_action": "Test action"
	}`

	ctx := context.Background()

	// Should return immediately without blocking
	result, err := tool.Call(ctx, input)

	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}

	if result == "" {
		t.Error("Call() returned empty result")
	}

	// Verify it contains instruction to wait for user
	if !strings.Contains(result, "take a screenshot to verify") {
		t.Errorf("Result doesn't mention verification after user confirms: %q", result)
	}
}
