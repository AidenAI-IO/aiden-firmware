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
	if !strings.Contains(desc, "handoff marker") {
		t.Error("Description() doesn't mention the handoff marker return contract")
	}
	if !strings.Contains(desc, `"reason":"authentication"`) || !strings.Contains(desc, `"details"`) {
		t.Error("Description() doesn't include a valid handoff JSON example")
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

	var payload struct {
		Status          string `json:"status"`
		Reason          string `json:"reason"`
		Details         string `json:"details"`
		SuggestedAction string `json:"suggested_action"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("Result is not structured JSON: %v\n%s", err, result)
	}
	if payload.Status != "HUMAN_HANDOFF_REQUESTED" || payload.Reason != "authentication" {
		t.Errorf("Unexpected payload: %#v", payload)
	}
}

func TestHumanHandoffTool_Call_AllReasons(t *testing.T) {
	for _, reason := range validHandoffReasonValues {
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

	var payload struct {
		Status          string `json:"status"`
		SuggestedAction string `json:"suggested_action,omitempty"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("Result is not structured JSON: %v\n%s", err, result)
	}
	if payload.Status != "HUMAN_HANDOFF_REQUESTED" {
		t.Errorf("status = %q, want HUMAN_HANDOFF_REQUESTED", payload.Status)
	}
	if payload.SuggestedAction != "" {
		t.Errorf("suggested_action = %q, want empty when omitted", payload.SuggestedAction)
	}
}

func TestHumanHandoffTool_Call_StructuredOutputForCommonReasons(t *testing.T) {
	for _, reason := range validHandoffReasonValues {
		t.Run(reason, func(t *testing.T) {
			tool := NewHumanHandoffTool()

			input := map[string]string{
				"reason":  reason,
				"details": "Test details",
			}
			inputJSON, _ := json.Marshal(input)

			ctx := context.Background()
			result, err := tool.Call(ctx, string(inputJSON))

			if err != nil {
				t.Fatalf("Call() error = %v, want nil", err)
			}

			var payload struct {
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Details string `json:"details"`
			}
			if err := json.Unmarshal([]byte(result), &payload); err != nil {
				t.Fatalf("Result is not structured JSON: %v\n%s", err, result)
			}
			if payload.Status != "HUMAN_HANDOFF_REQUESTED" || payload.Reason != reason || payload.Details != "Test details" {
				t.Errorf("Unexpected payload for %q: %#v", reason, payload)
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

	if !strings.Contains(result, "HUMAN_HANDOFF_REQUESTED") {
		t.Errorf("Result doesn't contain handoff marker: %q", result)
	}
}
